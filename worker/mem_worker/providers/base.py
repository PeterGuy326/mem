"""Provider interfaces — Protocols + value objects.

Three families (SPEC §9.3):

  * :class:`EmbeddingProvider` — text + image vector encoders
  * :class:`LLMProvider`       — chat / streaming completion
  * :class:`VLMProvider`       — image caption + visual QA

Adapters live in sibling modules (``ollama.py``, ``openai.py``, …) and are
constructed by :func:`mem_worker.providers.registry.get_*_provider`.

These are :class:`typing.Protocol`s on purpose: adapters don't have to inherit,
and tests can pass plain dataclasses or fakes. We also expose concrete
:class:`ProviderError` so callers have one type to catch.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any, Iterator, Protocol, runtime_checkable

# A single embedding vector.
Vector = list[float]


@dataclass(slots=True)
class Message:
    """One turn in an LLM conversation."""

    role: str  # "system" | "user" | "assistant"
    content: str


class ProviderError(RuntimeError):
    """Raised on any provider failure (network, model error, bad spec, …)."""


@runtime_checkable
class EmbeddingProvider(Protocol):
    """Text + (optional) image embedding."""

    name: str  # e.g. "ollama:nomic-embed-text"
    dim: int   # vector dimensionality, e.g. 768

    def embed_text(self, texts: list[str]) -> list[Vector]:
        """Encode a batch of strings. Order preserved."""
        ...

    def embed_image(self, images: list[bytes]) -> list[Vector]:
        """Encode a batch of image bytes. Optional — raise NotImplementedError
        if the provider does not support visual embeddings."""
        ...


@runtime_checkable
class LLMProvider(Protocol):
    """Chat-style completion."""

    name: str

    def complete(self, messages: list[Message], **kwargs: Any) -> str:
        ...

    def stream(self, messages: list[Message], **kwargs: Any) -> Iterator[str]:
        ...


@runtime_checkable
class VLMProvider(Protocol):
    """Vision-language model (captioning + VQA)."""

    name: str

    def caption(self, image: bytes, **kwargs: Any) -> str:
        ...

    def vqa(self, image: bytes, question: str, **kwargs: Any) -> str:
        ...
