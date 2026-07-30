"""Regression tests for OpenAI-compatible indexing providers.

The tests stub the provider's HTTP helper, so they never contact a model
service or require credentials.
"""

from __future__ import annotations

import base64
from typing import Any

import pytest

from mem_worker.config import get_settings
from mem_worker.providers import ProviderError
from mem_worker.providers.base import Message
from mem_worker.providers.openai import (
    OpenAIEmbeddingProvider,
    OpenAILLMProvider,
    OpenAIVLMProvider,
)


class _PostCapture:
    def __init__(self, response: dict[str, Any]):
        self.response = response
        self.calls: list[tuple[str, dict[str, Any]]] = []

    def __call__(self, path: str, payload: dict[str, Any]) -> dict[str, Any]:
        self.calls.append((path, payload))
        return self.response


def test_completion_ignores_vendor_private_reasoning(monkeypatch):
    provider = OpenAILLMProvider(
        model="compat-model",
        api_key="test-key",
        base_url="https://model.invalid",
    )
    post = _PostCapture(
        {
            "choices": [
                {
                    "message": {
                        "content": "index-safe summary",
                        "reasoning_content": "private reasoning must not escape",
                    }
                }
            ]
        }
    )
    monkeypatch.setattr(provider, "_post", post)

    result = provider.complete([Message(role="user", content="summarize source")])

    assert result == "index-safe summary"
    assert "private reasoning" not in result
    assert "<think>" not in result
    assert post.calls[0][0] == "/v1/chat/completions"


def test_embedding_contract_is_unchanged(monkeypatch):
    provider = OpenAIEmbeddingProvider(
        model="text-embedding-3-small",
        api_key="test-key",
        base_url="https://model.invalid",
    )
    post = _PostCapture({"data": [{"embedding": [0.1, 0.2, 0.3]}]})
    monkeypatch.setattr(provider, "_post", post)

    assert provider.embed_text(["evidence"]) == [[0.1, 0.2, 0.3]]
    assert post.calls == [
        (
            "/v1/embeddings",
            {"model": "text-embedding-3-small", "input": ["evidence"]},
        )
    ]


def test_embedding_uses_configured_output_dimensions(monkeypatch, env):
    env(OPENAI_EMBEDDING_DIMENSIONS="768")
    provider = OpenAIEmbeddingProvider(
        model="text-embedding-3-large",
        api_key="test-key",
        base_url="https://model.invalid",
    )
    vector = [0.0] * 768
    post = _PostCapture({"data": [{"embedding": vector}]})
    monkeypatch.setattr(provider, "_post", post)

    assert provider.embed_text(["quality-profile probe"]) == [vector]
    assert provider.dim == 768
    assert post.calls == [
        (
            "/v1/embeddings",
            {
                "model": "text-embedding-3-large",
                "input": ["quality-profile probe"],
                "dimensions": 768,
            },
        )
    ]


def test_embedding_rejects_configured_dimension_mismatch(monkeypatch):
    provider = OpenAIEmbeddingProvider(
        model="text-embedding-3-large",
        api_key="test-key",
        base_url="https://model.invalid",
        dimensions=768,
    )
    monkeypatch.setattr(provider, "_post", _PostCapture({"data": [{"embedding": [0.1, 0.2]}]}))

    with pytest.raises(ProviderError, match="got 2, want 768"):
        provider.embed_text(["quality-profile probe"])


@pytest.mark.parametrize("dimensions", [0, -1, True, 768.0, "768"])
def test_embedding_rejects_invalid_dimension_configuration(dimensions):
    with pytest.raises(ProviderError, match="dimensions must be a positive integer"):
        OpenAIEmbeddingProvider(
            api_key="test-key",
            base_url="https://model.invalid",
            dimensions=dimensions,
        )


@pytest.mark.parametrize("dimensions", ["", "0", "-1", "true", "768.0"])
def test_openai_embedding_dimension_setting_requires_positive_integer(env, dimensions):
    env(OPENAI_EMBEDDING_DIMENSIONS=dimensions)

    with pytest.raises(ValueError, match="OPENAI_EMBEDDING_DIMENSIONS must be a positive integer"):
        get_settings()


def test_vlm_contract_is_unchanged(monkeypatch):
    provider = OpenAIVLMProvider(
        model="gpt-4o-mini",
        api_key="test-key",
        base_url="https://model.invalid",
    )
    post = _PostCapture({"choices": [{"message": {"content": "a document on a desk"}}]})
    monkeypatch.setattr(provider, "_post", post)
    image = b"test-image"

    assert provider.caption(image) == "a document on a desk"
    path, payload = post.calls[0]
    assert path == "/v1/chat/completions"
    assert payload["model"] == "gpt-4o-mini"
    text_prompt = payload["messages"][0]["content"][0]["text"]
    assert text_prompt == "Describe this image in one short paragraph."
    image_url = payload["messages"][0]["content"][1]["image_url"]["url"]
    assert image_url == ("data:image/jpeg;base64," + base64.b64encode(image).decode("ascii"))


def test_vlm_caption_passes_custom_prompt(monkeypatch):
    provider = OpenAIVLMProvider(
        model="gpt-4o-mini",
        api_key="test-key",
        base_url="https://model.invalid",
    )
    post = _PostCapture({"choices": [{"message": {"content": "structured labels"}}]})
    monkeypatch.setattr(provider, "_post", post)

    result = provider.caption(b"test-image", prompt="Return concise semantic tags.")

    assert result == "structured labels"
    _, payload = post.calls[0]
    text_prompt = payload["messages"][0]["content"][0]["text"]
    assert text_prompt == "Return concise semantic tags."
