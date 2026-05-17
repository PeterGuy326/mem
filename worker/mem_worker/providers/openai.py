"""OpenAI provider — text embeddings + chat completions + vision.

Uses plain ``requests`` rather than the openai SDK so the only new optional
dep is API-key configuration.

Spec strings (SPEC §F8.5):
    openai:text-embedding-3-small  / -large  / ada-002
    openai:gpt-4o-mini  / gpt-4o  / gpt-4.1  / ...

Auth: ``OPENAI_API_KEY`` env. Base URL: ``OPENAI_BASE_URL`` (override for
proxies / Azure / vLLM-compat endpoints).
"""

from __future__ import annotations

import base64
from typing import Any, Iterator, Optional

import requests

from ..config import get_settings
from ..logging import get_logger
from .base import Message, ProviderError, Vector

log = get_logger(__name__)


class _BaseOpenAI:
    """Shared HTTP plumbing."""

    def __init__(self, api_key: Optional[str], base_url: Optional[str]):
        settings = get_settings()
        self._api_key = api_key or settings.openai_api_key
        self._base_url = (base_url or settings.openai_base_url or "https://api.openai.com").rstrip("/")
        if not self._api_key:
            raise ProviderError(
                "OPENAI_API_KEY not set; export it before using openai:* providers"
            )

    def _post(self, path: str, payload: dict, *, timeout: float = 120.0) -> dict:
        url = f"{self._base_url}{path}"
        try:
            resp = requests.post(
                url,
                headers={
                    "Authorization": f"Bearer {self._api_key}",
                    "Content-Type": "application/json",
                },
                json=payload,
                timeout=timeout,
            )
        except requests.RequestException as exc:
            raise ProviderError(f"openai network error: {exc}") from exc
        if not resp.ok:
            # Trim long error bodies for log readability.
            body = resp.text
            if len(body) > 500:
                body = body[:500] + "..."
            raise ProviderError(
                f"openai {path} HTTP {resp.status_code}: {body}"
            )
        return resp.json()


class OpenAIEmbeddingProvider(_BaseOpenAI):
    """OpenAI text embeddings."""

    _DIM_HINTS = {
        "text-embedding-3-small": 1536,
        "text-embedding-3-large": 3072,
        "text-embedding-ada-002": 1536,
    }

    def __init__(self, model: str = "text-embedding-3-small",
                 api_key: Optional[str] = None, base_url: Optional[str] = None):
        super().__init__(api_key, base_url)
        self.model = model
        self.name = f"openai:{model}"
        # dim is a hint only — final value is len(vectors[0]) at call time.
        self.dim = self._DIM_HINTS.get(model, 0)

    def embed_text(self, texts: list[str]) -> list[Vector]:
        if not texts:
            return []
        data = self._post("/v1/embeddings", {
            "model": self.model,
            "input": texts,
        })
        rows = data.get("data") or []
        return [row["embedding"] for row in rows]

    def embed_image(self, images: list[bytes]) -> list[Vector]:
        raise NotImplementedError(
            "OpenAI has no dedicated image-embedding endpoint; use clip:ViT-B-32"
        )


class OpenAILLMProvider(_BaseOpenAI):
    """OpenAI chat completions (``gpt-4o-mini`` / ``gpt-4o`` / …)."""

    def __init__(self, model: str = "gpt-4o-mini",
                 api_key: Optional[str] = None, base_url: Optional[str] = None):
        super().__init__(api_key, base_url)
        self.model = model
        self.name = f"openai:{model}"

    def complete(self, messages: list[Message], **kwargs: Any) -> str:
        payload = {
            "model": self.model,
            "messages": [{"role": m.role, "content": m.content} for m in messages],
        }
        # Pass through common knobs without overcommitting to OpenAI-specific
        # surface area; callers can extend via kwargs (max_tokens, temperature).
        for k in ("max_tokens", "temperature", "top_p", "stop"):
            if k in kwargs:
                payload[k] = kwargs[k]
        data = self._post("/v1/chat/completions", payload)
        choices = data.get("choices") or []
        if not choices:
            return ""
        return (choices[0].get("message") or {}).get("content") or ""

    def stream(self, messages: list[Message], **kwargs: Any) -> Iterator[str]:
        raise NotImplementedError("OpenAI streaming not wired yet")


class OpenAIVLMProvider(_BaseOpenAI):
    """Vision-capable chat models (e.g. gpt-4o-mini)."""

    def __init__(self, model: str = "gpt-4o-mini",
                 api_key: Optional[str] = None, base_url: Optional[str] = None):
        super().__init__(api_key, base_url)
        self.model = model
        self.name = f"openai:{model}"

    def caption(self, image: bytes, **kwargs: Any) -> str:
        return self._vision(image, "Describe this image in one short paragraph.")

    def vqa(self, image: bytes, question: str, **kwargs: Any) -> str:
        return self._vision(image, question)

    def _vision(self, image: bytes, prompt: str) -> str:
        b64 = base64.b64encode(image).decode("ascii")
        payload = {
            "model": self.model,
            "messages": [{
                "role": "user",
                "content": [
                    {"type": "text", "text": prompt},
                    {"type": "image_url",
                     "image_url": {"url": f"data:image/jpeg;base64,{b64}"}},
                ],
            }],
        }
        data = self._post("/v1/chat/completions", payload)
        choices = data.get("choices") or []
        if not choices:
            return ""
        return (choices[0].get("message") or {}).get("content") or ""
