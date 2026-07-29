"""Unit tests for ImageProcessor + TextProcessor with injected fakes.

We do NOT touch Ollama / S3 in these tests; providers are passed in directly.
"""

from __future__ import annotations

import io

import pytest
from PIL import Image

from mem_worker.processors.base import FileRef
from mem_worker.processors.image import (
    ImageProcessor,
    _extract_exif,
    _parse_exif_dt,
    _parse_gps,
)
from mem_worker.processors.text import TextProcessor, _chunk_text, _decode_text
from mem_worker.providers.base import Message, ProviderError

# ---------------------------------------------------------------------------
# Fakes
# ---------------------------------------------------------------------------


class FakeEmbedder:
    name = "fake:embed"

    def __init__(self, dim: int = 4):
        # dim defaults to 4 for text tests; the image path needs 512 to clear
        # ImageProcessor's visual-dim guard (CLIP ViT-B/32 schema = vector(512)).
        self.dim = dim
        self.calls: list[list[str]] = []

    def embed_text(self, texts):
        self.calls.append(list(texts))
        return [[float(len(t))] + [0.0] * (self.dim - 1) for t in texts]

    def embed_image(self, images):
        raise NotImplementedError


class FakeLLM:
    name = "fake:llm"

    def __init__(self, reply="SUMMARY"):
        self.reply = reply
        self.calls: list[list[Message]] = []

    def complete(self, messages, **kwargs):
        self.calls.append(messages)
        return self.reply

    def stream(self, messages, **kwargs):
        yield self.reply


class FakeVLM:
    name = "fake:vlm"

    def __init__(self, caption="a photo of a cat"):
        self._caption = caption
        self.calls = 0

    def caption(self, image, **kwargs):
        self.calls += 1
        return self._caption

    def vqa(self, image, question, **kwargs):
        return f"answer-to:{question}"


# ---------------------------------------------------------------------------
# Text utilities
# ---------------------------------------------------------------------------


def test_decode_text_utf8():
    assert _decode_text("héllo".encode()) == "héllo"


def test_decode_text_with_bom():
    assert _decode_text("﻿hi".encode()) == "hi"


def test_decode_text_latin1_fallback():
    raw = bytes([0xE9])  # 'é' in latin-1
    out = _decode_text(raw)
    assert out  # never raises, never empty


def test_chunk_text_basic():
    text = "abcdefghij"
    chunks = list(_chunk_text(text, size=4, overlap=1))
    # step = 3; chunks at 0, 3, 6 -> "abcd", "defg", "ghij" (10 chars covered).
    # No extra tail chunk because the previous chunk already reached the end.
    assert chunks == ["abcd", "defg", "ghij"]
    # All characters covered (with overlap)
    assert "".join(chunks).replace("d", "d", 1)  # sanity


def test_chunk_text_yields_tail_when_needed():
    text = "abcdefgh"  # 8 chars
    chunks = list(_chunk_text(text, size=5, overlap=1))
    # step=4: i=0 -> "abcde" (reaches 5); i=4 -> "efgh" (reaches 8, returns).
    assert chunks == ["abcde", "efgh"]


def test_chunk_text_bad_args():
    with pytest.raises(ValueError):
        list(_chunk_text("abc", size=0, overlap=0))
    with pytest.raises(ValueError):
        list(_chunk_text("abc", size=3, overlap=3))
    with pytest.raises(ValueError):
        list(_chunk_text("abc", size=3, overlap=-1))


def test_chunk_text_empty_string():
    assert list(_chunk_text("", size=10, overlap=2)) == []


# ---------------------------------------------------------------------------
# TextProcessor
# ---------------------------------------------------------------------------


def test_text_processor_chunks_and_embeds(monkeypatch):
    embedder = FakeEmbedder()
    llm = FakeLLM(reply="A short summary.")
    proc = TextProcessor(embedder=embedder, llm=llm)

    body = ("hello world. " * 200).encode("utf-8")  # > 200 chars triggers summary
    fref = FileRef(
        file_id="f1",
        storage_uri="file:///x.txt",
        mime="text/plain",
        sha256="",
        user_id="u",
        data=body,
    )
    r = proc.process(fref)
    assert r.processor == "text"
    assert "text" in r.embeddings
    assert r.embeddings["text"].provider == "fake:embed"
    assert r.embeddings["text"].dim == 4
    assert r.embeddings["text"].rows[0].chunk_text  # has chunk text
    assert r.embeddings["text"].rows[0].index == 0
    assert r.summary == "A short summary."
    assert r.metadata["chunk_count"] == len(r.embeddings["text"].rows)
    # Embedder should have been called exactly once with the full chunk list
    assert len(embedder.calls) == 1
    assert embedder.calls[0] == [row.chunk_text for row in r.embeddings["text"].rows]


def test_text_processor_can_disable_default_annotation_llm(env):
    env(MEM_DEFAULT_LLM="")
    proc = TextProcessor(embedder=FakeEmbedder())
    fref = FileRef(
        file_id="f1",
        storage_uri="file:///x.txt",
        mime="text/plain",
        sha256="",
        user_id="u",
        data=("long source text. " * 100).encode("utf-8"),
    )
    r = proc.process(fref)
    assert "text" in r.embeddings
    assert r.summary is None
    assert "summary_error" not in r.metadata


def test_text_processor_short_doc_skips_summary():
    embedder = FakeEmbedder()
    llm = FakeLLM()
    proc = TextProcessor(embedder=embedder, llm=llm)
    fref = FileRef(
        file_id="f1",
        storage_uri="file:///x.txt",
        mime="text/plain",
        sha256="",
        user_id="u",
        data=b"tiny",
    )
    r = proc.process(fref)
    assert r.summary is None
    assert llm.calls == []


def test_text_processor_empty_payload():
    proc = TextProcessor(embedder=FakeEmbedder(), llm=FakeLLM())
    fref = FileRef(
        file_id="f1",
        storage_uri="file:///x.txt",
        mime="text/plain",
        sha256="",
        user_id="u",
        data=b"   \n\n  ",
    )
    r = proc.process(fref)
    assert r.embeddings == {}
    assert r.metadata.get("decode_empty") is True


# ---------------------------------------------------------------------------
# ImageProcessor
# ---------------------------------------------------------------------------


def _png_bytes() -> bytes:
    """Produce a tiny valid PNG."""
    img = Image.new("RGB", (4, 3), color=(255, 0, 0))
    buf = io.BytesIO()
    img.save(buf, format="PNG")
    return buf.getvalue()


def test_image_processor_runs_vlm_and_embeds():
    vlm = FakeVLM(caption="red rectangle")
    embedder = FakeEmbedder(dim=512)  # match the visual schema so the vec isn't skipped
    proc = ImageProcessor(vlm=vlm, embedder=embedder)

    fref = FileRef(
        file_id="img1",
        storage_uri="file:///a.png",
        mime="image/png",
        sha256="",
        user_id="u",
        data=_png_bytes(),
    )
    r = proc.process(fref)
    assert r.processor == "image"
    assert r.caption == "red rectangle"
    assert vlm.calls == 1
    assert "visual" in r.embeddings
    assert r.embeddings["visual"].rows[0].chunk_text == "red rectangle"
    assert r.metadata["format"] == "PNG"
    assert r.metadata["width"] == 4
    assert r.metadata["height"] == 3


def test_image_processor_provider_probe_stops_after_caption():
    class MustNotRunEmbedder:
        name = "unexpected:embedder"

        def embed_image(self, images):
            raise AssertionError("provider probe must not run visual embedding")

        def embed_text(self, texts):
            raise AssertionError("provider probe must not run text embedding")

    vlm = FakeVLM(caption="a red probe image")
    proc = ImageProcessor(vlm=vlm, embedder=MustNotRunEmbedder())
    fref = FileRef(
        file_id="provider-probe",
        storage_uri="data:image/png;base64,...",
        mime="image/png",
        sha256="",
        user_id="",
        data=_png_bytes(),
        options={"provider_probe": True},
    )

    r = proc.process(fref)

    assert r.caption == "a red probe image"
    assert r.metadata["provider_probe"] is True
    assert r.embeddings == {}
    assert r.entities == []
    assert vlm.calls == 1


def test_image_processor_handles_undecodable_bytes():
    proc = ImageProcessor(vlm=FakeVLM(), embedder=FakeEmbedder())
    fref = FileRef(
        file_id="img1",
        storage_uri="file:///a.png",
        mime="image/png",
        sha256="",
        user_id="u",
        data=b"NOT-A-PNG",
    )
    r = proc.process(fref)
    assert r.caption is None
    assert "decode_error" in r.metadata


def test_image_processor_handles_vlm_failure():
    class BoomVLM:
        name = "boom"

        def caption(self, image, **kw):
            raise RuntimeError("ollama down")

        def vqa(self, image, question, **kw):
            raise RuntimeError

    proc = ImageProcessor(vlm=BoomVLM(), embedder=FakeEmbedder())
    fref = FileRef(
        file_id="img1",
        storage_uri="file:///a.png",
        mime="image/png",
        sha256="",
        user_id="u",
        data=_png_bytes(),
    )
    r = proc.process(fref)
    # Image still decoded, but caption empty + embedding not produced.
    assert r.metadata["format"] == "PNG"
    assert r.caption is None
    assert "vlm_error" in r.metadata
    assert "visual" not in r.embeddings


def test_parse_exif_datetime_preserves_explicit_offset():
    parsed = _parse_exif_dt("2026:07:29 08:15:30", "+08:00")

    assert parsed is not None
    assert parsed.isoformat() == "2026-07-29T08:15:30+08:00"


def test_parse_exif_datetime_without_offset_remains_naive():
    parsed = _parse_exif_dt("2026:07:29 08:15:30")

    assert parsed is not None
    assert parsed.tzinfo is None


def test_extract_exif_reads_real_offset_and_gps_ifd():
    image = Image.new("RGB", (2, 2), "red")
    exif = Image.Exif()
    exif[36867] = "2026:07:29 08:15:30"  # DateTimeOriginal
    exif[36881] = "+08:00"  # OffsetTimeOriginal
    exif[34853] = {  # GPSInfo
        1: "N",
        2: (31.0, 13.0, 49.44),
        3: "E",
        4: (121.0, 28.0, 25.32),
    }
    encoded = io.BytesIO()
    image.save(encoded, format="JPEG", exif=exif)
    decoded = Image.open(io.BytesIO(encoded.getvalue()))
    decoded.load()

    metadata = _extract_exif(decoded)

    assert metadata["timeline_at"] == "2026-07-29T08:15:30+08:00"
    assert metadata["gps"]["lat"] == pytest.approx(31.2304)
    assert metadata["gps"]["lng"] == pytest.approx(121.4737)


@pytest.mark.parametrize(
    ("missing_key", "replacement"),
    [
        (1, None),
        (3, None),
        (1, "Q"),
        (3, "Q"),
    ],
    ids=[
        "missing-latitude-ref",
        "missing-longitude-ref",
        "invalid-latitude-ref",
        "invalid-longitude-ref",
    ],
)
def test_parse_gps_requires_valid_hemisphere_refs(missing_key, replacement):
    gps = {
        1: "N",
        2: (31.0, 13.0, 49.44),
        3: "E",
        4: (121.0, 28.0, 25.32),
    }
    if replacement is None:
        del gps[missing_key]
    else:
        gps[missing_key] = replacement

    assert _parse_gps(gps) == (None, None)


@pytest.mark.parametrize(
    ("component_key", "components"),
    [
        (2, (31.0, 60.0, 0.0)),
        (2, (31.0, 0.0, 60.0)),
        (4, (121.0, -1.0, 0.0)),
        (4, (121.0, 0.0, float("inf"))),
        (2, (91.0, 0.0, 0.0)),
        (4, (180.0, 0.0, 0.1)),
    ],
    ids=[
        "latitude-minutes",
        "latitude-seconds",
        "longitude-negative-minutes",
        "longitude-non-finite-seconds",
        "latitude-degrees",
        "longitude-boundary-components",
    ],
)
def test_parse_gps_rejects_invalid_dms_components(component_key, components):
    gps = {
        1: "N",
        2: (31.0, 13.0, 49.44),
        3: "E",
        4: (121.0, 28.0, 25.32),
    }
    gps[component_key] = components

    assert _parse_gps(gps) == (None, None)


# ---------------------------------------------------------------------------
# PDFProcessor
# ---------------------------------------------------------------------------


def _pdf_bytes(lines):
    """Build a minimal valid single-page PDF with an extractable text layer."""

    def esc(s):
        return s.replace("\\", r"\\").replace("(", r"\(").replace(")", r"\)")

    parts = ["BT", "/F1 14 Tf", "72 720 Td", "16 TL"]
    for i, ln in enumerate(lines):
        if i:
            parts.append("T*")
        parts.append(f"({esc(ln)}) Tj")
    parts.append("ET")
    stream = "\n".join(parts).encode("latin-1")

    objs = [
        b"<</Type /Catalog /Pages 2 0 R>>",
        b"<</Type /Pages /Kids [3 0 R] /Count 1>>",
        b"<</Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] "
        b"/Resources <</Font <</F1 4 0 R>>>> /Contents 5 0 R>>",
        b"<</Type /Font /Subtype /Type1 /BaseFont /Helvetica>>",
        b"<</Length %d>>\nstream\n" % len(stream) + stream + b"\nendstream",
    ]
    out = bytearray(b"%PDF-1.4\n")
    offsets = []
    for i, body in enumerate(objs, start=1):
        offsets.append(len(out))
        out += b"%d 0 obj\n" % i + body + b"\nendobj\n"
    xref_pos = len(out)
    out += b"xref\n0 %d\n0000000000 65535 f \n" % (len(objs) + 1)
    for off in offsets:
        out += b"%010d 00000 n \n" % off
    out += b"trailer\n<</Size %d /Root 1 0 R>>\nstartxref\n%d\n%%%%EOF\n" % (
        len(objs) + 1,
        xref_pos,
    )
    return bytes(out)


def test_pdf_processor_extracts_text_and_runs_text_pipeline():
    from mem_worker.processors.pdf import PDFProcessor

    lines = [
        "Residential Lease Agreement",
        "Monthly rent: 1800 RMB due on the first day of each month",
        "Security deposit equals two months rent, refundable on move-out",
        "The tenant shall not sublet the premises without written consent",
    ]
    emb, llm = FakeEmbedder(), FakeLLM(reply="A lease summary.")
    proc = PDFProcessor(embedder=emb, llm=llm)
    fref = FileRef(
        file_id="pdf1",
        storage_uri="file:///lease.pdf",
        mime="application/pdf",
        sha256="",
        user_id="u",
        data=_pdf_bytes(lines),
    )
    r = proc.process(fref)

    assert r.processor == "pdf"
    assert r.metadata["page_count"] == 1
    assert r.metadata["extracted_char_length"] > 0
    assert r.metadata["source_mime"] == "application/pdf"
    # Text pipeline ran: embeddings produced + summary delegated to the LLM.
    assert "text" in r.embeddings and r.embeddings["text"].rows
    assert r.summary == "A lease summary."
    assert "1800 RMB" in emb.calls[0][0]


def test_pdf_processor_malformed_is_non_fatal():
    from mem_worker.processors.pdf import PDFProcessor

    proc = PDFProcessor(embedder=FakeEmbedder(), llm=FakeLLM())
    fref = FileRef(
        file_id="pdf2",
        storage_uri="file:///x.pdf",
        mime="application/pdf",
        sha256="",
        user_id="u",
        data=b"%PDF-1.4 not really a pdf",
    )
    r = proc.process(fref)
    assert r.processor == "pdf"
    # Either it parsed zero pages (text_empty) or failed to parse — both must
    # surface a marker and must NOT raise / produce embeddings.
    assert "parse_error" in r.metadata or r.metadata.get("text_empty")
    assert "text" not in r.embeddings


# ---------------------------------------------------------------------------
# AudioProcessor
# ---------------------------------------------------------------------------


class FakeASR:
    name = "fake:asr"

    def __init__(
        self,
        text="hello world",
        language="en",
        duration=1.5,
        error: Exception | None = None,
    ):
        from mem_worker.providers.base import Transcription

        self._t = Transcription(text=text, language=language, duration=duration)
        self._error = error

    def transcribe(self, audio, **kw):
        if self._error is not None:
            raise self._error
        return self._t


def test_audio_processor_transcribes_and_runs_text_pipeline():
    from mem_worker.processors.audio import AudioProcessor

    # >=200 chars so TextProcessor's summary path runs (short clips skip it).
    transcript = (
        "And so my fellow Americans ask not what your country can do for you "
        "ask what you can do for your country. My fellow citizens of the world "
        "ask not what America will do for you but what together we can do for "
        "the freedom of man across the whole of this great planet."
    )
    asr = FakeASR(text=transcript, language="en", duration=11.0)
    emb, llm = FakeEmbedder(), FakeLLM(reply="A patriotic call to service.")
    proc = AudioProcessor(asr=asr, embedder=emb, llm=llm)
    fref = FileRef(
        file_id="aud1",
        storage_uri="file:///jfk.flac",
        mime="audio/flac",
        sha256="",
        user_id="u",
        data=b"FAKE-AUDIO-BYTES",
    )
    r = proc.process(fref)

    assert r.processor == "audio"
    assert r.metadata["language"] == "en"
    assert r.metadata["duration_sec"] == 11.0
    assert r.metadata["transcript_char_length"] == len(transcript)
    assert r.metadata["asr_provider"] == "fake:asr"
    # Text pipeline ran on the transcript.
    assert "text" in r.embeddings and r.embeddings["text"].rows
    assert r.summary == "A patriotic call to service."
    assert "country" in emb.calls[0][0]


@pytest.mark.parametrize(
    "failure",
    [
        pytest.param(
            ProviderError("private ASR upstream response"),
            id="provider-error",
        ),
        pytest.param(
            NotImplementedError("private unsupported-ASR detail"),
            id="not-implemented",
        ),
        pytest.param(
            RuntimeError("private unexpected ASR response"),
            id="unexpected-error",
        ),
    ],
)
def test_audio_processor_asr_failure_is_non_fatal(failure: Exception):
    from mem_worker.processors.audio import AudioProcessor

    proc = AudioProcessor(
        asr=FakeASR(error=failure),
        embedder=FakeEmbedder(),
        llm=FakeLLM(),
    )
    fref = FileRef(
        file_id="aud2",
        storage_uri="file:///x.mp3",
        mime="audio/mpeg",
        sha256="",
        user_id="u",
        data=b"\xff\xfb",
    )
    r = proc.process(fref)
    assert r.processor == "audio"
    assert r.metadata["asr_error"] == "provider_unavailable"
    assert str(failure) not in repr(r.metadata)
    assert "text" not in r.embeddings


def test_audio_processor_empty_transcript_marker():
    from mem_worker.processors.audio import AudioProcessor

    proc = AudioProcessor(asr=FakeASR(text="   "), embedder=FakeEmbedder(), llm=FakeLLM())
    fref = FileRef(
        file_id="aud3",
        storage_uri="file:///silent.wav",
        mime="audio/wav",
        sha256="",
        user_id="u",
        data=b"RIFF....",
    )
    r = proc.process(fref)
    assert r.metadata.get("transcript_empty") is True
    assert "text" not in r.embeddings
