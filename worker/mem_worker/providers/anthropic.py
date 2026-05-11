"""Anthropic provider — STUB.

Phase: scheduled for W2/W3. Anthropic does not (yet) expose a text-embedding
endpoint, so only LLM + VLM (Claude models accept images natively) classes
are defined here. The Embedding class is intentionally absent.

Default model: ``claude-opus-4-7`` per SPEC §9.4.
"""

from __future__ import annotations

from typing import Any, Iterator, Optional

from ..config import get_settings
from .base import Message


class AnthropicLLMProvider:
    """Anthropic Claude chat completions."""

    def __init__(self, model: str = "claude-opus-4-7", api_key: Optional[str] = None):
        settings = get_settings()
        self.model = model
        self.api_key = api_key or settings.anthropic_api_key
        self.name = f"anthropic:{model}"

    def complete(self, messages: list[Message], **kwargs: Any) -> str:
        raise NotImplementedError("AnthropicLLMProvider.complete — W2")

    def stream(self, messages: list[Message], **kwargs: Any) -> Iterator[str]:
        raise NotImplementedError("AnthropicLLMProvider.stream — W2")


class AnthropicVLMProvider:
    """Claude vision (multimodal messages with image blocks)."""

    def __init__(
        self,
        model: str = "claude-haiku-4-5-20251001",
        api_key: Optional[str] = None,
    ):
        settings = get_settings()
        self.model = model
        self.api_key = api_key or settings.anthropic_api_key
        self.name = f"anthropic:{model}"

    def caption(self, image: bytes, **kwargs: Any) -> str:
        raise NotImplementedError("AnthropicVLMProvider.caption — W2")

    def vqa(self, image: bytes, question: str, **kwargs: Any) -> str:
        raise NotImplementedError("AnthropicVLMProvider.vqa — W2")
