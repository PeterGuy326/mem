"""Hermetic coverage for production indexing-LLM resolution."""

from __future__ import annotations

import importlib
import json

import pytest

from mem_worker.config import get_settings
from mem_worker.processors.base import FileRef
from mem_worker.processors.registry import default_registry, reset_default_registry
from mem_worker.processors.text import TextProcessor
from mem_worker.providers.base import ProviderError
from mem_worker.server import _result_to_proto


class _Embedder:
    name = "fake:embedder"

    def __init__(self, *, error: Exception | None = None):
        self.error = error

    def embed_text(self, texts):
        if self.error is not None:
            raise self.error
        return [[float(len(text)), 0.0] for text in texts]

    def embed_image(self, images):
        raise NotImplementedError


class _LLM:
    name = "fake:configured"

    def __init__(self, *, error: Exception | None = None):
        self.error = error
        self.calls = 0

    def complete(self, messages, **kwargs):
        self.calls += 1
        if self.error is not None:
            raise self.error
        return json.dumps(
            {
                "description": {
                    "value": "A configured-model description.",
                    "confidence": 0.8,
                },
                "tags": [{"value": "document", "confidence": 0.7}],
            }
        )

    def stream(self, messages, **kwargs):
        yield ""


def _long_file() -> FileRef:
    return FileRef(
        file_id="configured-llm",
        storage_uri="file:///document.txt",
        mime="text/plain",
        sha256="",
        user_id="u",
        data=("ordinary document content " * 20).encode(),
    )


def test_default_llm_setting_honors_environment(env):
    env(MEM_DEFAULT_LLM="openai:configured-indexer")

    assert get_settings().default_llm == "openai:configured-indexer"


@pytest.mark.parametrize(
    ("mime", "text_getter"),
    [
        ("text/plain", lambda processor: processor),
        ("application/pdf", lambda processor: processor._text),
        ("audio/mpeg", lambda processor: processor._text),
    ],
)
def test_default_registry_resolves_text_derived_llm_only_on_demand(
    monkeypatch, env, mime, text_getter
):
    env(MEM_DEFAULT_LLM="test:configured")
    resolved_specs: list[str] = []
    llm = _LLM()

    def resolve(spec):
        resolved_specs.append(spec)
        return llm

    text_module = importlib.import_module("mem_worker.processors.text")
    monkeypatch.setattr(text_module, "get_llm_provider", resolve)
    reset_default_registry()
    try:
        processor = default_registry().find(mime)
        assert processor is not None
        assert resolved_specs == []

        assert text_getter(processor)._resolve_llm() is llm
        assert resolved_specs == ["test:configured"]
    finally:
        reset_default_registry()


def test_configured_default_llm_emits_annotations_lazily(monkeypatch, env):
    env(MEM_DEFAULT_LLM="test:configured")
    llm = _LLM()
    resolved_specs: list[str] = []

    def resolve(spec):
        resolved_specs.append(spec)
        return llm

    text_module = importlib.import_module("mem_worker.processors.text")
    monkeypatch.setattr(text_module, "get_llm_provider", resolve)
    processor = TextProcessor(embedder=_Embedder())
    assert resolved_specs == []

    result = processor.process(_long_file())

    assert resolved_specs == ["test:configured"]
    assert llm.calls == 1
    assert result.summary == "A configured-model description."
    assert result.tags == ["document"]
    assert result.annotations_complete is True
    assert [item.provider for item in result.annotations] == [
        "fake:configured",
        "fake:configured",
    ]


@pytest.mark.parametrize(
    "failure",
    [
        pytest.param(
            ProviderError("private upstream response and credentials"),
            id="provider-error",
        ),
        pytest.param(
            NotImplementedError("private unsupported-provider detail"),
            id="not-implemented",
        ),
        pytest.param(
            RuntimeError("private unexpected model response"),
            id="unexpected-error",
        ),
    ],
)
def test_unavailable_default_llm_is_partial_without_persisting_raw_error(
    monkeypatch,
    env,
    failure: Exception,
):
    env(MEM_DEFAULT_LLM="test:offline")
    llm = _LLM(error=failure)
    text_module = importlib.import_module("mem_worker.processors.text")
    monkeypatch.setattr(text_module, "get_llm_provider", lambda spec: llm)

    result = TextProcessor(embedder=_Embedder()).process(_long_file())

    assert "text" in result.embeddings
    assert result.annotations == []
    assert result.annotations_complete is False
    assert result.metadata["summary_error"] == "provider_unavailable"
    assert str(failure) not in json.dumps(result.metadata)

    pb = importlib.import_module("mem_worker.proto.processor_pb2")
    response = _result_to_proto(result, pb)
    assert json.loads(response.metadata_json)["annotations_complete"] is False
    assert response.status == pb.STATUS_PARTIAL
    assert str(failure) not in response.metadata_json.decode()


@pytest.mark.parametrize(
    "failure",
    [
        pytest.param(
            ProviderError("private embedding upstream response"),
            id="provider-error",
        ),
        pytest.param(
            NotImplementedError("private unsupported-embedding detail"),
            id="not-implemented",
        ),
        pytest.param(
            RuntimeError("private unexpected embedding response"),
            id="unexpected-error",
        ),
    ],
)
def test_embedding_failure_is_partial_without_persisting_raw_error(failure: Exception):
    result = TextProcessor(embedder=_Embedder(error=failure)).process(
        FileRef(
            file_id="embedding-failure",
            storage_uri="file:///short.txt",
            mime="text/plain",
            sha256="",
            user_id="u",
            data=b"short document",
        )
    )

    assert result.embeddings == {}
    assert result.metadata["embed_error"] == "provider_unavailable"
    assert str(failure) not in json.dumps(result.metadata)

    pb = importlib.import_module("mem_worker.proto.processor_pb2")
    response = _result_to_proto(result, pb)
    assert response.status == pb.STATUS_PARTIAL
    assert str(failure) not in response.metadata_json.decode()
