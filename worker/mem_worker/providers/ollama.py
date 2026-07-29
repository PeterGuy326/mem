"""Ollama HTTP adapter.

Ollama is the *default local* provider for embeddings, LLM, and VLM. We talk
to it via its REST API (https://github.com/ollama/ollama/blob/main/docs/api.md).

This adapter is intentionally thin — it has no retry policy, no token
accounting, no streaming buffering. Those concerns live one level up.

W1 scope:
  * embed_text  via batched POST /api/embed (default: nomic-embed-text)
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
import math
from collections.abc import Iterator
from typing import Any

import requests

from ..config import get_settings
from ..logging import get_logger
from .base import Message, ProviderError, Vector

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
        raise ProviderError(f"ollama: {url} returned {resp.status_code}: {resp.text[:300]}")
    return resp


# ---------------------------------------------------------------------------
# Embedding
# ---------------------------------------------------------------------------


class OllamaEmbeddingProvider:
    """Text embedding via Ollama's modern batched ``/api/embed`` endpoint.

    mem's current PostgreSQL corpus is fixed at 768 dimensions. The adapter
    requests that dimension explicitly and fails closed when a model cannot
    honor it; it never truncates text or vectors locally to manufacture
    compatibility.
    """

    def __init__(
        self,
        model: str = "nomic-embed-text",
        base_url: str | None = None,
        timeout: float | None = None,
        dimensions: int = 768,
    ):
        settings = get_settings()
        if isinstance(dimensions, bool) or not isinstance(dimensions, int) or dimensions <= 0:
            raise ProviderError("ollama: embedding dimensions must be positive")
        self.model = model
        self.base_url = base_url or settings.ollama_base_url
        self.timeout = timeout if timeout is not None else settings.ollama_timeout
        self.name = f"ollama:{model}"
        self.dim = dimensions

    def embed_text(self, texts: list[str]) -> list[Vector]:
        """Encode a batch of strings while preserving input order."""
        if not texts:
            return []
        resp = _post_json(
            self.base_url,
            "/api/embed",
            {
                "model": self.model,
                "input": texts,
                "dimensions": self.dim,
                "truncate": False,
            },
            self.timeout,
        )
        try:
            data = resp.json()
        except (TypeError, ValueError) as exc:
            raise ProviderError("ollama: malformed embed response") from exc
        embeddings = data.get("embeddings") if isinstance(data, dict) else None
        if not isinstance(embeddings, list) or len(embeddings) != len(texts):
            count = len(embeddings) if isinstance(embeddings, list) else "invalid"
            raise ProviderError(
                f"ollama: malformed embed response: got {count} vectors for {len(texts)} inputs"
            )

        out: list[Vector] = []
        for index, vector in enumerate(embeddings):
            if not isinstance(vector, list):
                raise ProviderError(
                    f"ollama: malformed embed response: vector {index} is not an array"
                )
            if len(vector) != self.dim:
                raise ProviderError(
                    f"ollama: embedding dimension mismatch at vector {index}: "
                    f"got {len(vector)}, want {self.dim}"
                )
            if any(
                isinstance(value, bool) or not isinstance(value, (int, float)) for value in vector
            ):
                raise ProviderError(
                    f"ollama: malformed embed response: vector {index} contains a non-numeric value"
                )
            converted = [float(value) for value in vector]
            if any(not math.isfinite(value) for value in converted):
                raise ProviderError(
                    f"ollama: malformed embed response: vector {index} contains a non-finite value"
                )
            out.append(converted)
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
        base_url: str | None = None,
        timeout: float | None = None,
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
        "Describe this image in one concise sentence. "
        "Focus on subject, action, setting, and notable objects."
    )

    def __init__(
        self,
        model: str = "minicpm-v",
        base_url: str | None = None,
        timeout: float | None = None,
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
