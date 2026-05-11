"""OpenAI provider — STUB.

Phase: scheduled for W2/W3 once the Ollama path is fully exercised.
This module defines the public surface (class names, method signatures, dim
hints) so the registry can already route ``openai:*`` spec strings.

To implement, swap ``NotImplementedError`` for real ``openai`` SDK calls
(``client.embeddings.create``, ``client.chat.completions.create``).
"""

from __future__ import annotations

from typing import Any, Iterator, Optional

from ..config import get_settings
from .base import Message, Vector


class OpenAIEmbeddingProvider:
    """OpenAI text embeddings (``text-embedding-3-small`` / ``-large``)."""

    _DIM_HINTS = {
        "text-embedding-3-small": 1536,
        "text-embedding-3-large": 3072,
        "text-embedding-ada-002": 1536,
    }

    def __init__(self, model: str = "text-embedding-3-small", api_key: Optional[str] = None):
        settings = get_settings()
        self.model = model
        self.api_key = api_key or settings.openai_api_key
        self.base_url = settings.openai_base_url
        self.name = f"openai:{model}"
        self.dim = self._DIM_HINTS.get(model, 0)

    def embed_text(self, texts: list[str]) -> list[Vector]:
        raise NotImplementedError("OpenAIEmbeddingProvider.embed_text — W2")

    def embed_image(self, images: list[bytes]) -> list[Vector]:
        raise NotImplementedError(
            "OpenAI does not expose an image-embedding endpoint as of this writing"
        )


class OpenAILLMProvider:
    """OpenAI chat completions (``gpt-4o-mini`` / ``gpt-4o`` / …)."""

    def __init__(self, model: str = "gpt-4o-mini", api_key: Optional[str] = None):
        settings = get_settings()
        self.model = model
        self.api_key = api_key or settings.openai_api_key
        self.base_url = settings.openai_base_url
        self.name = f"openai:{model}"

    def complete(self, messages: list[Message], **kwargs: Any) -> str:
        raise NotImplementedError("OpenAILLMProvider.complete — W2")

    def stream(self, messages: list[Message], **kwargs: Any) -> Iterator[str]:
        raise NotImplementedError("OpenAILLMProvider.stream — W2")


class OpenAIVLMProvider:
    """OpenAI vision-capable models (``gpt-4o-mini`` accepts images)."""

    def __init__(self, model: str = "gpt-4o-mini", api_key: Optional[str] = None):
        settings = get_settings()
        self.model = model
        self.api_key = api_key or settings.openai_api_key
        self.base_url = settings.openai_base_url
        self.name = f"openai:{model}"

    def caption(self, image: bytes, **kwargs: Any) -> str:
        raise NotImplementedError("OpenAIVLMProvider.caption — W2")

    def vqa(self, image: bytes, question: str, **kwargs: Any) -> str:
        raise NotImplementedError("OpenAIVLMProvider.vqa — W2")
