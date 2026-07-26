"""Ollama HTTP adapter.

Ollama is the *default local* provider for embeddings, LLM, and VLM. We talk
to it via its REST API (https://github.com/ollama/ollama/blob/main/docs/api.md).

This adapter is intentionally thin — it has no retry policy, no token
accounting, no streaming buffering. Those concerns live one level up.

W1 scope:
  * embed_text  via POST /api/embeddings   (default: nomic-embed-text)
  * complete    via POST /api/chat
  * stream      via POST /api/chat with stream=true
  * caption     via POST /api/chat with multi-modal "images" field
  * vqa         same path as caption, different prompt

embed_image is NOT supported — Ollama embedding endpoints are text-only.
The pipeline should use a CLIP adapter (W2) or fall back to captioning +
text embedding when only Ollama is available.
"""

from __future__ import annotations

import base64
import json
from typing import Any, Iterator, Optional

import requests

from ..config import get_settings
from ..logging import get_logger
from .base import EmbeddingProvider, LLMProvider, Message, ProviderError, Vector, VLMProvider

log = get_logger(__name__)


# ---------------------------------------------------------------------------
# Shared HTTP helper
# ---------------------------------------------------------------------------

def _post_json(
    base_url: str,
    path: str,
    payload: dict,
    timeout: float,
    *,
    stream: bool = False,
) -> requests.Response:
    url = base_url.rstrip("/") + path
    try:
        resp = requests.post(url, json=payload, timeout=timeout, stream=stream)
    except requests.RequestException as exc:
        raise ProviderError(f"ollama: HTTP request to {url} failed: {exc}") from exc
    if resp.status_code >= 400:
        raise ProviderError(
            f"ollama: {url} returned {resp.status_code}: {resp.text[:300]}"
        )
    return resp


# ---------------------------------------------------------------------------
# Embedding
# ---------------------------------------------------------------------------

class OllamaEmbeddingProvider:
    """Text embedding via Ollama's ``/api/embeddings`` endpoint.

    The dim is set lazily after the first successful call (since it depends
    on the model). We seed a sensible default for the canonical model.
    """

    # Known model -> dim hints (best-effort, not exhaustive).
    _DIM_HINTS = {
        "nomic-embed-text": 768,
        "mxbai-embed-large": 1024,
        "all-minilm": 384,
        "bge-m3": 1024,
    }

    def __init__(
        self,
        model: str = "nomic-embed-text",
        base_url: Optional[str] = None,
        timeout: Optional[float] = None,
    ):
        settings = get_settings()
        self.model = model
        self.base_url = base_url or settings.ollama_base_url
        self.timeout = timeout if timeout is not None else settings.ollama_timeout
        self.name = f"ollama:{model}"
        self.dim = self._DIM_HINTS.get(model.split(":")[0], 0)

    def embed_text(self, texts: list[str]) -> list[Vector]:
        """Encode a batch of strings.

        Ollama's ``/api/embeddings`` accepts a single ``prompt`` per call,
        so we loop. This is fine for W1 throughput; W2 can switch to the
        newer ``/api/embed`` batched endpoint if available.
        """
        out: list[Vector] = []
        for text in texts:
            resp = _post_json(
                self.base_url,
                "/api/embeddings",
                {"model": self.model, "prompt": text},
                self.timeout,
            )
            data = resp.json()
            vec = data.get("embedding")
            if not isinstance(vec, list):
                raise ProviderError(f"ollama: malformed embed response: {data!r}")
            out.append([float(x) for x in vec])
            # Real-response dim wins over the static hint; this also keeps
            # us honest when a model is swapped out without restart.
            self.dim = len(vec)
        return out

    def embed_image(self, images: list[bytes]) -> list[Vector]:
        raise NotImplementedError(
            "Ollama does not provide image embeddings; use a CLIP adapter "
            "or caption+text-embed as a fallback."
        )


# ---------------------------------------------------------------------------
# LLM
# ---------------------------------------------------------------------------

class OllamaLLMProvider:
    """Chat completion via Ollama's ``/api/chat`` endpoint."""

    def __init__(
        self,
        model: str = "qwen2.5:7b",
        base_url: Optional[str] = None,
        timeout: Optional[float] = None,
    ):
        settings = get_settings()
        self.model = model
        self.base_url = base_url or settings.ollama_base_url
        self.timeout = timeout if timeout is not None else settings.ollama_timeout
        self.name = f"ollama:{model}"

    def _payload(self, messages: list[Message], **kwargs: Any) -> dict:
        return {
            "model": self.model,
            "messages": [{"role": m.role, "content": m.content} for m in messages],
            "stream": False,
            "options": kwargs.get("options", {}),
        }

    def complete(self, messages: list[Message], **kwargs: Any) -> str:
        payload = self._payload(messages, **kwargs)
        resp = _post_json(self.base_url, "/api/chat", payload, self.timeout)
        data = resp.json()
        msg = data.get("message") or {}
        content = msg.get("content")
        if not isinstance(content, str):
            raise ProviderError(f"ollama: malformed chat response: {data!r}")
        return content

    def stream(self, messages: list[Message], **kwargs: Any) -> Iterator[str]:
        payload = self._payload(messages, **kwargs)
        payload["stream"] = True
        resp = _post_json(self.base_url, "/api/chat", payload, self.timeout, stream=True)
        for line in resp.iter_lines():
            if not line:
                continue
            try:
                chunk = json.loads(line)
            except json.JSONDecodeError:
                continue
            piece = (chunk.get("message") or {}).get("content")
            if piece:
                yield piece


# ---------------------------------------------------------------------------
# VLM
# ---------------------------------------------------------------------------

class OllamaVLMProvider:
    """Vision-language model via Ollama (e.g. ``minicpm-v``, ``llava``).

    Ollama accepts images on the chat endpoint as base64-encoded entries in
    the message's ``images`` array. We always send a single image per turn.
    """

    DEFAULT_CAPTION_PROMPT = (
        "请只用简体中文准确描述这张图片中直接可见的内容，包括主要对象、"
        "动作、场景和显著物体。不要根据季节、地点、身份或画面外信息进行推测。"
    )

    def __init__(
        self,
        model: str = "minicpm-v",
        base_url: Optional[str] = None,
        timeout: Optional[float] = None,
    ):
        settings = get_settings()
        self.model = model
        self.base_url = base_url or settings.ollama_base_url
        self.timeout = timeout if timeout is not None else settings.ollama_timeout
        self.name = f"ollama:{model}"

    def _chat(self, image: bytes, prompt: str) -> str:
        b64 = base64.b64encode(image).decode("ascii")
        payload = {
            "model": self.model,
            "stream": False,
            "options": {"temperature": 0.1},
            "messages": [
                {"role": "user", "content": prompt, "images": [b64]},
            ],
        }
        resp = _post_json(self.base_url, "/api/chat", payload, self.timeout)
        data = resp.json()
        msg = data.get("message") or {}
        content = msg.get("content")
        if not isinstance(content, str):
            raise ProviderError(f"ollama: malformed VLM response: {data!r}")
        return content.strip()

    def caption(self, image: bytes, **kwargs: Any) -> str:
        prompt = kwargs.get("prompt") or self.DEFAULT_CAPTION_PROMPT
        return self._chat(image, prompt)

    def vqa(self, image: bytes, question: str, **kwargs: Any) -> str:
        return self._chat(image, question)


# All three classes structurally satisfy their respective Protocols in
# ``mem_worker.providers.base``. We rely on ``@runtime_checkable`` +
# ``isinstance`` checks in tests rather than module-level assertions, to
# avoid constructing real instances at import time.
