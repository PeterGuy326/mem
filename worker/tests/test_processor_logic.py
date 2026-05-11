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
    dim = 4

    def __init__(self):
        self.calls: list[list[str]] = []

    def embed_text(self, texts):
        self.calls.append(list(texts))
        return [[float(len(t)), 0.0, 0.0, 0.0] for t in texts]

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
    embedder = FakeEmbedder()
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
