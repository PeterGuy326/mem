"""Hermetic coverage for the server-resolved Worker AI profile contract."""

from __future__ import annotations

import importlib
import json

import pytest

from mem_worker.processors.base import FileRef
from mem_worker.providers.registry import get_embedding_provider
from mem_worker.server import AIProfileError, ProcessorServicer, parse_ai_profile


def _stage(enabled: bool, provider: str, dimensions: int | None = None) -> dict:
    if not enabled:
        return {"enabled": False}
    result = {"enabled": True, "provider": provider}
    if dimensions is not None:
        result["dimensions"] = dimensions
    return result


def _profile(
    *,
    embedding: bool = True,
    visual_embedding: bool = True,
    llm: bool = True,
    vlm: bool = True,
    asr: bool = True,
    rerank: bool = False,
) -> dict:
    return {
        "contract": "mem.ai-profile/v1",
        "id": "idealab-quality-v1",
        "revision": "2026-07-29",
        "pipeline_revision": "file-enrichment-v2",
        "embedding": _stage(embedding, "openai:text-embedding-3-large", 768),
        "visual_embedding": _stage(visual_embedding, "clip:ViT-B-32", 512),
        "llm": _stage(llm, "openai:qwen3.7-max-2026-06-08"),
        "vlm": _stage(vlm, "openai:qwen-vl-max"),
        "asr": _stage(asr, "faster-whisper:tiny"),
        "rerank": _stage(rerank, "openai:qwen3-rerank"),
    }


def _protobuf_modules():
    pb = importlib.import_module("mem_worker.proto.processor_pb2")
    pbg = importlib.import_module("mem_worker.proto.processor_pb2_grpc")
    return pb, pbg


def _request(pb, options: dict):
    return pb.ProcessRequest(
        file_id="00000000-0000-0000-0000-000000000054",
        storage_uri="s3://mem/profile-test",
        mime="text/plain",
        sha256="",
        user_id="u",
        name="profile.txt",
        options_json=json.dumps(options).encode("utf-8"),
    )


class _Embedding:
    name = "openai:text-embedding-3-large"

    def embed_text(self, texts):
        return [[0.0] * 768 for _ in texts]

    def embed_image(self, images):
        raise NotImplementedError


class _LLM:
    name = "openai:qwen3.7-max-2026-06-08"

    def __init__(self):
        self.calls = 0

    def complete(self, messages, **kwargs):
        self.calls += 1
        return json.dumps(
            {
                "description": {"value": "A profile-routed document.", "confidence": 0.9},
                "tags": [{"value": "profile", "confidence": 0.8}],
            }
        )

    def stream(self, messages, **kwargs):
        yield ""


def test_profile_contract_requires_all_explicit_stages_and_schema_dimensions():
    profile = _profile()
    parsed = parse_ai_profile({"ai_profile": profile})

    assert parsed is not None
    assert parsed.embedding.provider == "openai:text-embedding-3-large"
    assert parsed.embedding.dimensions == 768
    assert parsed.visual_embedding.provider == "clip:ViT-B-32"
    assert parsed.visual_embedding.dimensions == 512
    assert parsed.rerank.enabled is False

    invalid_profiles = []
    missing_stage = _profile()
    del missing_stage["asr"]
    invalid_profiles.append(missing_stage)
    wrong_text_dimension = _profile()
    wrong_text_dimension["embedding"]["dimensions"] = 1024
    invalid_profiles.append(wrong_text_dimension)
    wrong_visual_dimension = _profile()
    wrong_visual_dimension["visual_embedding"]["dimensions"] = 768
    invalid_profiles.append(wrong_visual_dimension)
    disabled_with_provider = _profile(llm=False)
    disabled_with_provider["llm"]["provider"] = "openai:must-not-be-used"
    invalid_profiles.append(disabled_with_provider)
    unexpected_field = _profile()
    unexpected_field["base_url"] = "https://forbidden.invalid"
    invalid_profiles.append(unexpected_field)

    for invalid in invalid_profiles:
        with pytest.raises(AIProfileError, match="invalid ai_profile"):
            parse_ai_profile({"ai_profile": invalid})


def test_invalid_profile_fails_before_storage_or_default_routing(monkeypatch):
    pb, pbg = _protobuf_modules()
    invalid = _profile()
    invalid["embedding"]["dimensions"] = 1024
    server_module = importlib.import_module("mem_worker.server")
    monkeypatch.setattr(
        server_module,
        "fetch_bytes",
        lambda _uri: pytest.fail("invalid profile must not fetch source bytes"),
    )

    response = ProcessorServicer(pb, pbg).Process(
        _request(pb, {"ai_profile": invalid}),
        context=None,
    )

    assert response.status == pb.STATUS_FAILED
    assert response.error == "invalid ai_profile"


def test_malformed_options_fail_before_storage_or_default_routing(monkeypatch):
    pb, pbg = _protobuf_modules()
    server_module = importlib.import_module("mem_worker.server")
    monkeypatch.setattr(
        server_module,
        "fetch_bytes",
        lambda _uri: pytest.fail("malformed options must not fetch source bytes"),
    )
    request = pb.ProcessRequest(
        file_id="00000000-0000-0000-0000-000000000054",
        storage_uri="s3://mem/profile-test",
        mime="text/plain",
        options_json=b'{"ai_profile":',
    )

    response = ProcessorServicer(pb, pbg).Process(request, context=None)

    assert response.status == pb.STATUS_FAILED
    assert response.error == "invalid options_json"


def test_profiled_processors_disable_all_default_provider_resolution():
    pb, pbg = _protobuf_modules()
    profile = _profile(
        embedding=False,
        visual_embedding=False,
        llm=False,
        vlm=False,
        asr=False,
    )
    servicer = ProcessorServicer(pb, pbg)

    text = servicer._pick_processor("text/plain", {"ai_profile": profile})
    image = servicer._pick_processor("image/png", {"ai_profile": profile})
    audio = servicer._pick_processor("audio/mpeg", {"ai_profile": profile})

    assert text._resolve_embedder() is None
    assert text._resolve_llm() is None
    assert image._resolve_vlm() is None
    assert image._resolve_embedder() is None
    assert audio._resolve_asr() is None

    result = text.process(
        FileRef(
            file_id="disabled",
            storage_uri="file:///disabled.txt",
            mime="text/plain",
            sha256="",
            user_id="u",
            data=("source text " * 100).encode(),
        )
    )
    assert result.embeddings == {}
    assert result.annotations == []


def test_profile_routes_explicit_models_and_propagates_bounded_provenance(monkeypatch, env):
    env(
        MEM_DEFAULT_EMBEDDING="invalid:must-not-be-used",
        MEM_DEFAULT_LLM="invalid:must-not-be-used",
        MEM_DEFAULT_VLM="invalid:must-not-be-used",
        MEM_DEFAULT_ASR="invalid:must-not-be-used",
        MEM_DEFAULT_VISUAL_EMBEDDING="invalid:must-not-be-used",
    )
    pb, pbg = _protobuf_modules()
    profile = _profile(visual_embedding=False, vlm=False, asr=False)
    embedding = _Embedding()
    llm = _LLM()
    embedding_calls: list[tuple[str, int | None]] = []
    llm_specs: list[str] = []
    providers = importlib.import_module("mem_worker.providers")
    text_module = importlib.import_module("mem_worker.processors.text")
    server_module = importlib.import_module("mem_worker.server")

    def get_embedder(spec: str, *, dimensions: int | None = None):
        embedding_calls.append((spec, dimensions))
        return embedding

    def get_llm(spec: str):
        llm_specs.append(spec)
        return llm

    monkeypatch.setattr(providers, "get_embedding_provider", get_embedder)
    monkeypatch.setattr(text_module, "get_llm_provider", get_llm)
    monkeypatch.setattr(server_module, "fetch_bytes", lambda _uri: b"profile source " * 100)

    response = ProcessorServicer(pb, pbg).Process(
        _request(
            pb,
            {
                "embedding_provider": "ollama:ignored-legacy-override",
                "llm_provider": "ollama:ignored-legacy-override",
                "ai_profile": profile,
            },
        ),
        context=None,
    )

    assert response.status == pb.STATUS_OK
    assert embedding_calls == [("openai:text-embedding-3-large", 768)]
    assert llm_specs == ["openai:qwen3.7-max-2026-06-08"]
    assert llm.calls == 1
    metadata = json.loads(response.metadata_json)
    assert metadata["ai_profile"] == {
        "contract": "mem.ai-profile/v1",
        "id": "idealab-quality-v1",
        "revision": "2026-07-29",
        "pipeline_revision": "file-enrichment-v2",
    }
    assert all(
        annotation["analysis_version"] == "file-enrichment-v2"
        for annotation in metadata["annotations"]
    )


def test_profile_dimension_overrides_global_openai_dimension(env, monkeypatch):
    env(OPENAI_API_KEY="test-key", OPENAI_EMBEDDING_DIMENSIONS="1024")
    provider = get_embedding_provider("openai:text-embedding-3-large", dimensions=768)
    calls = []

    def post(path, payload):
        calls.append((path, payload))
        return {"data": [{"embedding": [0.0] * 768}]}

    monkeypatch.setattr(provider, "_post", post)

    assert provider.embed_text(["profile dimension"]) == [[0.0] * 768]
    assert provider.dim == 768
    assert calls == [
        (
            "/v1/embeddings",
            {
                "model": "text-embedding-3-large",
                "input": ["profile dimension"],
                "dimensions": 768,
            },
        )
    ]
