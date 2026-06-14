"""Provider factory: spec string -> concrete adapter.

A *spec* is ``"<vendor>:<model>"`` (with ``<model>`` allowed to contain colons,
e.g. ``"ollama:qwen2.5:7b"``). The registry never *constructs* a network
client until you actually call ``get_*_provider``, so importing this module
has no side effects.

Adding a new vendor:
    1. Implement adapter classes in ``mem_worker/providers/<vendor>.py``.
    2. Add a case in :func:`get_embedding_provider` / :func:`get_llm_provider`
       / :func:`get_vlm_provider`.
"""

from __future__ import annotations

from typing import Optional

from . import anthropic as anthropic_mod
from . import ollama as ollama_mod
from . import openai as openai_mod
from .base import ASRProvider, EmbeddingProvider, LLMProvider, ProviderError, VLMProvider


def parse_spec(spec: str) -> tuple[str, str]:
    """Split ``"vendor:model"`` into ``("vendor", "model")``.

    Model names may contain colons (e.g. ``qwen2.5:7b``), so we only split
    on the *first* one.

    Raises:
        ProviderError: if the spec is missing the vendor prefix.
    """
    if ":" not in spec:
        raise ProviderError(
            f"invalid provider spec {spec!r}; expected '<vendor>:<model>'"
        )
    vendor, _, model = spec.partition(":")
    vendor = vendor.strip().lower()
    model = model.strip()
    if not vendor or not model:
        raise ProviderError(f"invalid provider spec {spec!r}")
    return vendor, model


def get_embedding_provider(spec: str) -> EmbeddingProvider:
    """Construct an :class:`EmbeddingProvider` for ``spec``."""
    vendor, model = parse_spec(spec)
    if vendor == "ollama":
        return ollama_mod.OllamaEmbeddingProvider(model=model)
    if vendor == "openai":
        return openai_mod.OpenAIEmbeddingProvider(model=model)
    if vendor == "clip":
        # CLIP spec is "clip:<model>[:<pretrained>]". Pretrained defaults to
        # OpenAI's checkpoint, which matches SPEC §9.4.
        from . import clip as clip_mod  # lazy import: heavy torch deps

        if ":" in model:
            arch, pretrained = model.split(":", 1)
        else:
            arch, pretrained = model, "openai"
        return clip_mod.CLIPEmbeddingProvider(model=arch, pretrained=pretrained)
    raise ProviderError(f"no EmbeddingProvider for vendor {vendor!r}")


def get_llm_provider(spec: str) -> LLMProvider:
    """Construct an :class:`LLMProvider` for ``spec``."""
    vendor, model = parse_spec(spec)
    if vendor == "ollama":
        return ollama_mod.OllamaLLMProvider(model=model)
    if vendor == "openai":
        return openai_mod.OpenAILLMProvider(model=model)
    if vendor == "anthropic":
        return anthropic_mod.AnthropicLLMProvider(model=model)
    raise ProviderError(f"no LLMProvider for vendor {vendor!r}")


def get_vlm_provider(spec: str) -> VLMProvider:
    """Construct a :class:`VLMProvider` for ``spec``."""
    vendor, model = parse_spec(spec)
    if vendor == "ollama":
        return ollama_mod.OllamaVLMProvider(model=model)
    if vendor == "openai":
        return openai_mod.OpenAIVLMProvider(model=model)
    if vendor == "anthropic":
        return anthropic_mod.AnthropicVLMProvider(model=model)
    raise ProviderError(f"no VLMProvider for vendor {vendor!r}")


def get_asr_provider(spec: str) -> ASRProvider:
    """Construct an :class:`ASRProvider` for ``spec``."""
    vendor, model = parse_spec(spec)
    if vendor in ("faster-whisper", "whisper"):
        from . import whisper as whisper_mod  # lazy: pulls in faster-whisper

        return whisper_mod.FasterWhisperASRProvider(model=model)
    raise ProviderError(f"no ASRProvider for vendor {vendor!r}")


# Convenience: build all three from defaults in one call.
def build_default_providers(
    *,
    embedding_spec: Optional[str] = None,
    llm_spec: Optional[str] = None,
    vlm_spec: Optional[str] = None,
) -> tuple[EmbeddingProvider, LLMProvider, VLMProvider]:
    """Return ``(embed, llm, vlm)`` using settings defaults when not overridden."""
    from ..config import get_settings  # local import to avoid cycles

    settings = get_settings()
    return (
        get_embedding_provider(embedding_spec or settings.default_embedding),
        get_llm_provider(llm_spec or settings.default_llm),
        get_vlm_provider(vlm_spec or settings.default_vlm),
    )
