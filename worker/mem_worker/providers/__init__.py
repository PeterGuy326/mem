"""Model-provider adapters (SPEC §9.3 / §F8).

A *provider* is any service that supplies AI capabilities:
  * EmbeddingProvider — text + image embeddings
  * LLMProvider       — chat / completion
  * VLMProvider       — vision-language (caption / VQA)
  * ASRProvider       — speech-to-text

Providers are addressed by spec strings of the form ``"<vendor>:<model>"``,
e.g. ``"ollama:nomic-embed-text"``. See :mod:`mem_worker.providers.registry`.
"""

from __future__ import annotations

from .base import (
    ASRProvider,
    EmbeddingProvider,
    LLMProvider,
    Message,
    ProviderError,
    Transcription,
    Vector,
    VLMProvider,
)
from .registry import (
    get_asr_provider,
    get_embedding_provider,
    get_llm_provider,
    get_vlm_provider,
    parse_spec,
)

__all__ = [
    "EmbeddingProvider",
    "LLMProvider",
    "VLMProvider",
    "ASRProvider",
    "Message",
    "Transcription",
    "Vector",
    "ProviderError",
    "get_asr_provider",
    "get_embedding_provider",
    "get_llm_provider",
    "get_vlm_provider",
    "parse_spec",
]
