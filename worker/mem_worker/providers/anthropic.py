"""Anthropic provider — Claude chat + vision.

Uses plain ``requests``. Anthropic does not expose an embedding endpoint, so
no AnthropicEmbeddingProvider class.

Spec strings (SPEC §F8.5):
    anthropic:claude-opus-4-7
    anthropic:claude-sonnet-4-6
    anthropic:claude-haiku-4-5-20251001

Auth: ``ANTHROPIC_API_KEY`` env.
"""

from __future__ import annotations

import base64
from typing import Any, Iterator, Optional

import requests

from ..config import get_settings
from ..logging import get_logger
from .base import Message, ProviderError

log = get_logger(__name__)

_API_BASE = "https://api.anthropic.com"
_API_VERSION = "2023-06-01"


class _BaseAnthropic:
    def __init__(self, api_key: Optional[str]):
        settings = get_settings()
        self._api_key = api_key or settings.anthropic_api_key
        if not self._api_key:
            raise ProviderError(
                "ANTHROPIC_API_KEY not set; export it before using anthropic:* providers"
            )

    def _post(self, path: str, payload: dict, *, timeout: float = 120.0) -> dict:
        url = f"{_API_BASE}{path}"
        try:
            resp = requests.post(
                url,
                headers={
                    "x-api-key": self._api_key,
                    "anthropic-version": _API_VERSION,
                    "content-type": "application/json",
                },
                json=payload,
                timeout=timeout,
            )
        except requests.RequestException as exc:
            raise ProviderError(f"anthropic network error: {exc}") from exc
        if not resp.ok:
            body = resp.text
            if len(body) > 500:
                body = body[:500] + "..."
            raise ProviderError(
                f"anthropic {path} HTTP {resp.status_code}: {body}"
            )
        return resp.json()


def _split_system(messages: list[Message]) -> tuple[str, list[dict]]:
    """Claude wants system as a top-level field, not in messages."""
    sys_text = ""
    out: list[dict] = []
    for m in messages:
        if m.role == "system":
            sys_text = (sys_text + "\n" + m.content).strip() if sys_text else m.content
        else:
            out.append({"role": m.role, "content": m.content})
    return sys_text, out


class AnthropicLLMProvider(_BaseAnthropic):
    """Claude chat completions."""

    def __init__(self, model: str = "claude-opus-4-7", api_key: Optional[str] = None):
        super().__init__(api_key)
        self.model = model
        self.name = f"anthropic:{model}"

    def complete(self, messages: list[Message], **kwargs: Any) -> str:
        system, msgs = _split_system(messages)
        payload: dict[str, Any] = {
            "model": self.model,
            "messages": msgs,
            "max_tokens": kwargs.get("max_tokens", 1024),
        }
        if system:
            payload["system"] = system
        for k in ("temperature", "top_p", "stop_sequences"):
            if k in kwargs:
                payload[k] = kwargs[k]
        data = self._post("/v1/messages", payload)
        # data.content is a list of {type, text} blocks; concat the text ones.
        parts = []
        for blk in data.get("content") or []:
            if blk.get("type") == "text":
                parts.append(blk.get("text", ""))
        return "".join(parts)

    def stream(self, messages: list[Message], **kwargs: Any) -> Iterator[str]:
        raise NotImplementedError("Anthropic streaming not wired yet")


class AnthropicVLMProvider(_BaseAnthropic):
    """Claude vision (multimodal messages with image blocks)."""

    def __init__(self, model: str = "claude-haiku-4-5-20251001",
                 api_key: Optional[str] = None):
        super().__init__(api_key)
        self.model = model
        self.name = f"anthropic:{model}"

    def caption(self, image: bytes, **kwargs: Any) -> str:
        return self._vision(image, "Describe this image in one short paragraph.")

    def vqa(self, image: bytes, question: str, **kwargs: Any) -> str:
        return self._vision(image, question)

    def _vision(self, image: bytes, prompt: str, media_type: str = "image/jpeg") -> str:
        b64 = base64.b64encode(image).decode("ascii")
        payload = {
            "model": self.model,
            "max_tokens": 512,
            "messages": [{
                "role": "user",
                "content": [
                    {"type": "image",
                     "source": {"type": "base64", "media_type": media_type, "data": b64}},
                    {"type": "text", "text": prompt},
                ],
            }],
        }
        data = self._post("/v1/messages", payload)
        parts = []
        for blk in data.get("content") or []:
            if blk.get("type") == "text":
                parts.append(blk.get("text", ""))
        return "".join(parts)
