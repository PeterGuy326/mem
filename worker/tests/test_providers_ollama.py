"""Tests for the Ollama provider adapter (HTTP shape only).

We monkeypatch :func:`requests.post` to avoid any network traffic and assert
the request payloads + response parsing are correct.
"""

from __future__ import annotations

import base64
import json
from typing import Any

import pytest

from mem_worker.providers import ProviderError, parse_spec
from mem_worker.providers.base import Message
from mem_worker.providers.ollama import (
    OllamaEmbeddingProvider,
    OllamaLLMProvider,
    OllamaVLMProvider,
)
from mem_worker.providers.registry import (
    get_embedding_provider,
    get_llm_provider,
    get_vlm_provider,
)

# ---------------------------------------------------------------------------
# Fake requests.Response / requests.post
# ---------------------------------------------------------------------------


class _FakeResp:
    def __init__(self, payload: Any, status: int = 200, lines: list[str] | None = None):
        self._payload = payload
        self.status_code = status
        self.text = json.dumps(payload) if not isinstance(payload, str) else payload
        self._lines = lines or []

    def json(self):
        return self._payload

    def iter_lines(self):
        for ln in self._lines:
            yield ln.encode("utf-8") if isinstance(ln, str) else ln


class _Capture:
    """Record every call to ``requests.post`` and return scripted responses."""

    def __init__(self):
        self.calls: list[dict] = []
        self._scripted: list[_FakeResp] = []

    def script(self, *responses: _FakeResp):
        self._scripted.extend(responses)

    def __call__(self, url, json=None, timeout=None, stream=False):  # noqa: A002
        self.calls.append({"url": url, "json": json, "timeout": timeout, "stream": stream})
        if not self._scripted:
            return _FakeResp({"error": "no scripted response"}, status=500)
        return self._scripted.pop(0)


@pytest.fixture
def capture(monkeypatch):
    cap = _Capture()
    monkeypatch.setattr("mem_worker.providers.ollama.requests.post", cap)
    return cap


# ---------------------------------------------------------------------------
# Spec parsing
# ---------------------------------------------------------------------------


def test_parse_spec_basic():
    assert parse_spec("ollama:nomic-embed-text") == ("ollama", "nomic-embed-text")
    assert parse_spec("openai:gpt-4o-mini") == ("openai", "gpt-4o-mini")
    # Model with colon (qwen2.5:7b) preserved
    assert parse_spec("ollama:qwen2.5:7b") == ("ollama", "qwen2.5:7b")


@pytest.mark.parametrize("bad", ["", "ollama", ":model", "vendor:", "  :  "])
def test_parse_spec_rejects_bad(bad):
    with pytest.raises(ProviderError):
        parse_spec(bad)


# ---------------------------------------------------------------------------
# Registry routing
# ---------------------------------------------------------------------------


def test_registry_returns_ollama_classes():
    e = get_embedding_provider("ollama:nomic-embed-text")
    assert isinstance(e, OllamaEmbeddingProvider)
    assert e.model == "nomic-embed-text"
    assert e.dim == 768  # known model hint matches the fixed mem corpus contract
    assert e.name == "ollama:nomic-embed-text"

    llm = get_llm_provider("ollama:qwen2.5:7b")
    assert isinstance(llm, OllamaLLMProvider)
    assert llm.model == "qwen2.5:7b"

    vlm = get_vlm_provider("ollama:minicpm-v")
    assert isinstance(vlm, OllamaVLMProvider)


def test_registry_rejects_unknown_vendor():
    with pytest.raises(ProviderError):
        get_embedding_provider("cohere:embed-multilingual-v3")


# ---------------------------------------------------------------------------
# Embedding
# ---------------------------------------------------------------------------


def test_embed_text_request_shape(capture, monkeypatch):
    monkeypatch.setenv("OLLAMA_BASE_URL", "http://ollama.test:11434")
    first_vector = [0.0] * 768
    second_vector = [0.0] * 768
    first_vector[0] = 0.1
    second_vector[0] = 0.4
    capture.script(_FakeResp({"embeddings": [first_vector, second_vector]}))
    p = OllamaEmbeddingProvider(model="nomic-embed-text")
    vectors = p.embed_text(["hello", "world"])

    assert vectors == [first_vector, second_vector]
    assert len(capture.calls) == 1

    first = capture.calls[0]
    assert first["url"] == "http://ollama.test:11434/api/embed"
    assert first["json"] == {
        "model": "nomic-embed-text",
        "input": ["hello", "world"],
        "dimensions": 768,
        "truncate": False,
    }
    assert p.dim == 768


def test_embed_text_empty_batch_avoids_http(capture):
    p = OllamaEmbeddingProvider()
    assert p.embed_text([]) == []
    assert capture.calls == []


def test_embed_text_rejects_wrong_batch_size(capture):
    capture.script(_FakeResp({"embeddings": [[0.0] * 768]}))
    p = OllamaEmbeddingProvider()
    with pytest.raises(ProviderError, match="got 1 vectors for 2 inputs"):
        p.embed_text(["one", "two"])


def test_embed_text_rejects_wrong_dimension(capture):
    capture.script(_FakeResp({"embeddings": [[0.1, 0.2]]}))
    p = OllamaEmbeddingProvider()
    with pytest.raises(ProviderError, match="got 2, want 768"):
        p.embed_text(["one"])


def test_embed_text_rejects_non_numeric_values(capture):
    vector = [0.0] * 768
    vector[100] = True
    capture.script(_FakeResp({"embeddings": [vector]}))
    p = OllamaEmbeddingProvider()
    with pytest.raises(ProviderError, match="non-numeric"):
        p.embed_text(["one"])


def test_embed_text_rejects_non_finite_values(capture):
    vector = [0.0] * 768
    vector[100] = float("nan")
    capture.script(_FakeResp({"embeddings": [vector]}))
    p = OllamaEmbeddingProvider()
    with pytest.raises(ProviderError, match="non-finite"):
        p.embed_text(["one"])


@pytest.mark.parametrize("dimensions", [0, -1, True, 768.0, "768"])
def test_embed_text_rejects_invalid_dimension_configuration(dimensions):
    with pytest.raises(ProviderError, match="dimensions must be positive"):
        OllamaEmbeddingProvider(dimensions=dimensions)


def test_embed_text_raises_on_http_error(capture):
    capture.script(_FakeResp({"error": "model not found"}, status=404))
    p = OllamaEmbeddingProvider(model="missing")
    with pytest.raises(ProviderError):
        p.embed_text(["x"])


def test_embed_image_not_supported():
    p = OllamaEmbeddingProvider()
    with pytest.raises(NotImplementedError):
        p.embed_image([b"\x00"])


# ---------------------------------------------------------------------------
# LLM
# ---------------------------------------------------------------------------


def test_llm_complete_request_shape(capture):
    capture.script(_FakeResp({"message": {"role": "assistant", "content": "ok"}}))
    p = OllamaLLMProvider(model="qwen2.5:7b")
    out = p.complete([Message(role="user", content="hi")])
    assert out == "ok"

    call = capture.calls[0]
    assert call["url"].endswith("/api/chat")
    body = call["json"]
    assert body["model"] == "qwen2.5:7b"
    assert body["stream"] is False
    assert body["messages"] == [{"role": "user", "content": "hi"}]


def test_llm_stream_yields_chunks(capture):
    lines = [
        json.dumps({"message": {"content": "Hello"}}),
        json.dumps({"message": {"content": " "}}),
        json.dumps({"message": {"content": "world"}}),
        json.dumps({"done": True}),
        "",  # blank line should be skipped
        "not-json",  # malformed line should be skipped
    ]
    capture.script(_FakeResp({}, lines=lines))
    p = OllamaLLMProvider()
    pieces = list(p.stream([Message(role="user", content="hi")]))
    assert pieces == ["Hello", " ", "world"]
    assert capture.calls[0]["json"]["stream"] is True
    assert capture.calls[0]["stream"] is True


# ---------------------------------------------------------------------------
# VLM
# ---------------------------------------------------------------------------


def test_vlm_caption_sends_base64_image(capture):
    capture.script(_FakeResp({"message": {"content": "a cat on grass"}}))
    p = OllamaVLMProvider(model="minicpm-v")
    image = b"\x89PNG\r\n\x1a\n" + b"\x00" * 16
    out = p.caption(image)
    assert out == "a cat on grass"

    body = capture.calls[0]["json"]
    assert body["model"] == "minicpm-v"
    msg = body["messages"][0]
    assert msg["role"] == "user"
    assert msg["content"] == OllamaVLMProvider.DEFAULT_CAPTION_PROMPT
    assert msg["images"] == [base64.b64encode(image).decode("ascii")]


def test_vlm_caption_passes_custom_prompt(capture):
    capture.script(_FakeResp({"message": {"content": "structured labels"}}))
    p = OllamaVLMProvider(model="minicpm-v")

    out = p.caption(b"\x00", prompt="Return concise semantic tags.")

    assert out == "structured labels"
    assert capture.calls[0]["json"]["messages"][0]["content"] == ("Return concise semantic tags.")


def test_vlm_vqa_uses_question_as_prompt(capture):
    capture.script(_FakeResp({"message": {"content": "blue"}}))
    p = OllamaVLMProvider()
    out = p.vqa(b"\x00", question="what color is the sky?")
    assert out == "blue"
    body = capture.calls[0]["json"]
    assert body["messages"][0]["content"] == "what color is the sky?"
