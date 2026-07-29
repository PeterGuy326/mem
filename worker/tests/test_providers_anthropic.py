"""Regression tests for Anthropic-compatible indexing providers.

The tests stub the provider's HTTP helper, so they never contact a model
service or require credentials.
"""

from __future__ import annotations

from typing import Any

from mem_worker.providers.anthropic import AnthropicVLMProvider


class _PostCapture:
    def __init__(self, response: dict[str, Any]):
        self.response = response
        self.calls: list[tuple[str, dict[str, Any]]] = []

    def __call__(self, path: str, payload: dict[str, Any]) -> dict[str, Any]:
        self.calls.append((path, payload))
        return self.response


def test_vlm_caption_default_prompt_is_unchanged(monkeypatch):
    provider = AnthropicVLMProvider(
        model="claude-haiku-test",
        api_key="test-key",
    )
    post = _PostCapture({"content": [{"type": "text", "text": "a document"}]})
    monkeypatch.setattr(provider, "_post", post)

    assert provider.caption(b"test-image") == "a document"
    path, payload = post.calls[0]
    assert path == "/v1/messages"
    text_prompt = payload["messages"][0]["content"][1]["text"]
    assert text_prompt == "Describe this image in one short paragraph."


def test_vlm_caption_passes_custom_prompt(monkeypatch):
    provider = AnthropicVLMProvider(
        model="claude-haiku-test",
        api_key="test-key",
    )
    post = _PostCapture({"content": [{"type": "text", "text": "structured labels"}]})
    monkeypatch.setattr(provider, "_post", post)

    result = provider.caption(b"test-image", prompt="Return concise semantic tags.")

    assert result == "structured labels"
    _, payload = post.calls[0]
    text_prompt = payload["messages"][0]["content"][1]["text"]
    assert text_prompt == "Return concise semantic tags."
