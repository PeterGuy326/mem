"""Unit tests for ImageProcessor + TextProcessor with injected fakes.

We do NOT touch Ollama / S3 in these tests; providers are passed in directly.
"""

from __future__ import annotations

import io

import pytest
from PIL import Image

from mem_worker.processors.base import FileRef
from mem_worker.processors.image import ImageProcessor
from mem_worker.processors.text import TextProcessor, _chunk_text, _decode_text
from mem_worker.providers.base import Message


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
    assert _decode_text("héllo".encode("utf-8")) == "héllo"


def test_decode_text_with_bom():
    assert _decode_text("﻿hi".encode("utf-8")) == "hi"


def test_decode_text_latin1_fallback():
    raw = bytes([0xe9])                              # 'é' in latin-1
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
    text = "abcdefgh"   # 8 chars
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

    body = ("hello world. " * 200).encode("utf-8")    # > 200 chars triggers summary
    fref = FileRef(
        file_id="f1", storage_uri="file:///x.txt", mime="text/plain",
        sha256="", user_id="u", data=body,
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


def test_text_processor_short_doc_skips_summary():
    embedder = FakeEmbedder()
    llm = FakeLLM()
    proc = TextProcessor(embedder=embedder, llm=llm)
    fref = FileRef(
        file_id="f1", storage_uri="file:///x.txt", mime="text/plain",
        sha256="", user_id="u", data=b"tiny",
    )
    r = proc.process(fref)
    assert r.summary is None
    assert llm.calls == []


def test_text_processor_empty_payload():
    proc = TextProcessor(embedder=FakeEmbedder(), llm=FakeLLM())
    fref = FileRef(
        file_id="f1", storage_uri="file:///x.txt", mime="text/plain",
        sha256="", user_id="u", data=b"   \n\n  ",
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
        file_id="img1", storage_uri="file:///a.png", mime="image/png",
        sha256="", user_id="u", data=_png_bytes(),
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


def test_image_processor_handles_undecodable_bytes():
    proc = ImageProcessor(vlm=FakeVLM(), embedder=FakeEmbedder())
    fref = FileRef(
        file_id="img1", storage_uri="file:///a.png", mime="image/png",
        sha256="", user_id="u", data=b"NOT-A-PNG",
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
        file_id="img1", storage_uri="file:///a.png", mime="image/png",
        sha256="", user_id="u", data=_png_bytes(),
    )
    r = proc.process(fref)
    # Image still decoded, but caption empty + embedding not produced.
    assert r.metadata["format"] == "PNG"
    assert r.caption is None
    assert "vlm_error" in r.metadata
    assert "visual" not in r.embeddings


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
        parts.append("(%s) Tj" % esc(ln))
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
        len(objs) + 1, xref_pos)
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
        file_id="pdf1", storage_uri="file:///lease.pdf", mime="application/pdf",
        sha256="", user_id="u", data=_pdf_bytes(lines),
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
        file_id="pdf2", storage_uri="file:///x.pdf", mime="application/pdf",
        sha256="", user_id="u", data=b"%PDF-1.4 not really a pdf",
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

    def __init__(self, text="hello world", language="en", duration=1.5, boom=False):
        from mem_worker.providers.base import Transcription
        self._t = Transcription(text=text, language=language, duration=duration)
        self._boom = boom

    def transcribe(self, audio, **kw):
        if self._boom:
            from mem_worker.providers.base import ProviderError
            raise ProviderError("asr down")
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
        file_id="aud1", storage_uri="file:///jfk.flac", mime="audio/flac",
        sha256="", user_id="u", data=b"FAKE-AUDIO-BYTES",
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


def test_audio_processor_asr_failure_is_non_fatal():
    from mem_worker.processors.audio import AudioProcessor

    proc = AudioProcessor(asr=FakeASR(boom=True), embedder=FakeEmbedder(), llm=FakeLLM())
    fref = FileRef(
        file_id="aud2", storage_uri="file:///x.mp3", mime="audio/mpeg",
        sha256="", user_id="u", data=b"\xff\xfb",
    )
    r = proc.process(fref)
    assert r.processor == "audio"
    assert "asr_error" in r.metadata
    assert "text" not in r.embeddings


def test_audio_processor_empty_transcript_marker():
    from mem_worker.processors.audio import AudioProcessor

    proc = AudioProcessor(asr=FakeASR(text="   "), embedder=FakeEmbedder(), llm=FakeLLM())
    fref = FileRef(
        file_id="aud3", storage_uri="file:///silent.wav", mime="audio/wav",
        sha256="", user_id="u", data=b"RIFF....",
    )
    r = proc.process(fref)
    assert r.metadata.get("transcript_empty") is True
    assert "text" not in r.embeddings
