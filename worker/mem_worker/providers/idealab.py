"""Dedicated Idealab provider binding.

Idealab exposes an OpenAI-compatible HTTP surface, but its managed data-egress
boundary is not interchangeable with a generic ``openai:*`` provider. These
adapters therefore require their own credential and absolute HTTPS endpoint;
they never inherit ``OPENAI_*`` settings and never default to api.openai.com.
"""

from __future__ import annotations

from urllib.parse import urlsplit

from ..config import get_settings
from .base import ProviderError
from .openai import OpenAIEmbeddingProvider, OpenAILLMProvider, OpenAIVLMProvider


def _binding() -> tuple[str, str]:
    settings = get_settings()
    api_key = (settings.idealab_key() or "").strip()
    base_url = (settings.idealab_base_url or "").strip()
    if not api_key:
        raise ProviderError("IDEALAB_API_KEY not set")
    if not base_url:
        raise ProviderError("IDEALAB_BASE_URL not set")

    parsed = urlsplit(base_url)
    if (
        parsed.scheme.lower() != "https"
        or not parsed.netloc
        or not parsed.hostname
        or parsed.username is not None
        or parsed.password is not None
        or parsed.query
        or parsed.fragment
    ):
        raise ProviderError("IDEALAB_BASE_URL must be an absolute HTTPS URL")
    return api_key, base_url.rstrip("/")


class IdealabEmbeddingProvider(OpenAIEmbeddingProvider):
    """Idealab-bound OpenAI-compatible text embeddings."""

    def __init__(self, model: str, *, dimensions: int | None = None):
        api_key, base_url = _binding()
        super().__init__(
            model=model,
            api_key=api_key,
            base_url=base_url,
            dimensions=dimensions,
        )
        self._allow_redirects = False
        self.name = f"idealab:{model}"


class IdealabLLMProvider(OpenAILLMProvider):
    """Idealab-bound OpenAI-compatible text completion."""

    def __init__(self, model: str):
        api_key, base_url = _binding()
        super().__init__(model=model, api_key=api_key, base_url=base_url)
        self._allow_redirects = False
        self.name = f"idealab:{model}"


class IdealabVLMProvider(OpenAIVLMProvider):
    """Idealab-bound OpenAI-compatible multimodal completion."""

    def __init__(self, model: str):
        api_key, base_url = _binding()
        super().__init__(model=model, api_key=api_key, base_url=base_url)
        self._allow_redirects = False
        self.name = f"idealab:{model}"


__all__ = [
    "IdealabEmbeddingProvider",
    "IdealabLLMProvider",
    "IdealabVLMProvider",
]
