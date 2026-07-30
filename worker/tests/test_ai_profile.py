"""Hermetic coverage for the server-resolved Worker AI profile contract."""

from __future__ import annotations

import base64
import hashlib
import importlib
import itertools
import json

import pytest

from mem_worker.auth import (
    PROCESS_METHOD,
    PROCESS_SCOPE,
    RequestAuthenticator,
    build_request_metadata,
)
from mem_worker.processors.base import FileRef, ProcessResult
from mem_worker.providers import ProviderError
from mem_worker.providers.registry import get_embedding_provider
from mem_worker.server import AIProfileError, ProcessorServicer, parse_ai_profile
from mem_worker.storage import StorageError

_AUTH_KEY = bytes(range(32))
_AUTH_KEY_ID = "memd-primary"
_AUTH_NOW = 1_785_363_200
_NONCE_SEQUENCE = itertools.count(1)
_DEFAULT_SOURCE = b"profile source " * 100


class _Context:
    def __init__(self, metadata):
        self._metadata = tuple(metadata)
        self.trailing_metadata = None

    def invocation_metadata(self):
        return self._metadata

    def set_trailing_metadata(self, metadata):
        self.trailing_metadata = tuple(metadata)

    def abort(self, code, detail):
        raise AssertionError(f"unexpected gRPC abort {code}: {detail}")


class _ReplayStore:
    def claim(self, key_id: str, nonce: str, ttl_seconds: int) -> bool:
        assert key_id == _AUTH_KEY_ID
        assert nonce
        assert ttl_seconds == 600
        return True

    def ping(self) -> None:
        return None


def _stage(enabled: bool, provider: str, dimensions: int | None = None) -> dict:
    if not enabled:
        return {"enabled": False}
    result = {"enabled": True, "provider": provider}
    if dimensions is not None:
        result["dimensions"] = dimensions
    return result


def _profile(
    *,
    data_egress: str = "managed_idealab",
    embedding: bool = True,
    visual_embedding: bool = True,
    llm: bool = True,
    vlm: bool = True,
    asr: bool = True,
    rerank: bool = False,
) -> dict:
    return {
        "contract": "mem.ai-profile/v1",
        "id": "idealab-quality-v2",
        "revision": "2026-07-30.1",
        "pipeline_revision": "file-enrichment-v2",
        "data_egress": data_egress,
        "embedding": _stage(embedding, "idealab:text-embedding-3-large", 768),
        "visual_embedding": _stage(visual_embedding, "clip:ViT-B-32", 512),
        "llm": _stage(llm, "idealab:qwen3.7-max-2026-06-08"),
        "vlm": _stage(vlm, "idealab:qwen-vl-max"),
        "asr": _stage(asr, "faster-whisper:tiny"),
        "rerank": _stage(rerank, "idealab:qwen3-rerank"),
    }


def _protobuf_modules():
    pb = importlib.import_module("mem_worker.proto.processor_pb2")
    pbg = importlib.import_module("mem_worker.proto.processor_pb2_grpc")
    return pb, pbg


def _request(pb, options: dict, *, source: bytes = _DEFAULT_SOURCE):
    return pb.ProcessRequest(
        file_id="00000000-0000-0000-0000-000000000054",
        storage_uri="s3://mem/profile-test",
        mime="text/plain",
        sha256=hashlib.sha256(source).hexdigest(),
        user_id="u",
        name="profile.txt",
        options_json=json.dumps(options).encode("utf-8"),
    )


def _managed_servicer(
    pb,
    pbg,
    *,
    idealab: bool = True,
    openai: bool = True,
):
    return ProcessorServicer(
        pb,
        pbg,
        authenticator=RequestAuthenticator(
            mode="required",
            key_id=_AUTH_KEY_ID,
            key=_AUTH_KEY,
            replay_store=_ReplayStore(),
            clock=lambda: _AUTH_NOW,
            idealab_binding_ready=lambda: idealab,
            openai_binding_ready=lambda: openai,
        ),
    )


def _signed_context(request):
    nonce_bytes = next(_NONCE_SEQUENCE).to_bytes(24, "big")
    nonce = base64.urlsafe_b64encode(nonce_bytes).decode("ascii").rstrip("=")
    return _Context(
        build_request_metadata(
            request,
            key=_AUTH_KEY,
            key_id=_AUTH_KEY_ID,
            timestamp=_AUTH_NOW,
            nonce=nonce,
            method=PROCESS_METHOD,
            scope=PROCESS_SCOPE,
        )
    )


def _process_managed(request, pb, pbg, *, servicer=None):
    target = servicer or _managed_servicer(pb, pbg)
    return target.Process(request, _signed_context(request))


def _required_auth_env(env, **extra):
    values = {
        "MEM_DEPLOYMENT_MODE": "private",
        "MEM_WORKER_AUTH_MODE": "required",
        "MEM_WORKER_AUTH_KEY_ID": _AUTH_KEY_ID,
        "MEM_WORKER_AUTH_KEY_B64": base64.b64encode(_AUTH_KEY).decode("ascii"),
        "MEM_WORKER_AUTH_REPLAY_REDIS_URL": "redis://localhost:6379/7",
        "OPENAI_API_KEY": "",
        "OPENAI_BASE_URL": "",
        **extra,
    }
    env(**values)


class _Embedding:
    name = "idealab:text-embedding-3-large"

    def embed_text(self, texts):
        return [[0.0] * 768 for _ in texts]

    def embed_image(self, images):
        raise NotImplementedError


class _LLM:
    name = "idealab:qwen3.7-max-2026-06-08"

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
    assert parsed.data_egress == "managed_idealab"
    assert parsed.embedding.provider == "idealab:text-embedding-3-large"
    assert parsed.embedding.dimensions == 768
    assert parsed.visual_embedding.provider == "clip:ViT-B-32"
    assert parsed.visual_embedding.dimensions == 512
    assert parsed.rerank.enabled is False

    invalid_profiles = []
    missing_stage = _profile()
    del missing_stage["asr"]
    invalid_profiles.append(missing_stage)
    missing_data_egress = _profile()
    del missing_data_egress["data_egress"]
    invalid_profiles.append(missing_data_egress)
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


def test_managed_profile_rejects_generic_openai_namespace():
    profile = _profile(vlm=False, asr=False)
    profile["embedding"]["provider"] = "openai:text-embedding-3-large"

    with pytest.raises(AIProfileError, match="invalid ai_profile"):
        parse_ai_profile({"ai_profile": profile})


def test_exact_published_v1_query_projection_runs_through_authenticated_openai_binding(
    monkeypatch,
    env,
):
    _required_auth_env(
        env,
        MEM_DEPLOYMENT_MODE="saas",
        MEM_OPENAI_MANAGED_BINDING="true",
        OPENAI_API_KEY="legacy-managed-key",
        OPENAI_BASE_URL="https://legacy-idealab.invalid/compatible",
    )
    from mem_worker.config import get_settings

    settings = get_settings()
    assert settings.openai_managed_binding_ready()
    pb, pbg = _protobuf_modules()
    source = b"published V1 query projection"
    profile = {
        "contract": "mem.ai-profile/v1",
        "id": "idealab-quality-v1",
        "revision": "2026-07-29",
        "pipeline_revision": "file-enrichment-v1",
        "data_egress": "managed_idealab",
        "embedding": _stage(True, "openai:text-embedding-3-large", 768),
        "visual_embedding": _stage(True, "clip:ViT-B-32", 512),
        "llm": _stage(False, ""),
        "vlm": _stage(False, ""),
        "asr": _stage(False, ""),
        "rerank": _stage(False, ""),
    }
    parsed = parse_ai_profile({"ai_profile": profile})
    assert parsed is not None
    assert parsed.embedding.provider == "openai:text-embedding-3-large"
    assert parsed.llm.enabled is False

    class LegacyEmbedding(_Embedding):
        name = "openai:text-embedding-3-large"

    providers = importlib.import_module("mem_worker.providers")
    server_module = importlib.import_module("mem_worker.server")
    monkeypatch.setattr(
        providers,
        "get_embedding_provider",
        lambda _spec, *, dimensions=None: LegacyEmbedding(),
    )
    monkeypatch.setattr(server_module, "fetch_bytes", lambda _uri: source)
    request = _request(pb, {"ai_profile": profile}, source=source)
    servicer = ProcessorServicer(
        pb,
        pbg,
        authenticator=RequestAuthenticator(
            mode="required",
            key_id=_AUTH_KEY_ID,
            key=_AUTH_KEY,
            replay_store=_ReplayStore(),
            clock=lambda: _AUTH_NOW,
            idealab_binding_ready=settings.idealab_binding_ready,
            openai_binding_ready=settings.openai_managed_binding_ready,
        ),
    )

    response = _process_managed(request, pb, pbg, servicer=servicer)

    assert response.status == pb.STATUS_OK
    metadata = json.loads(response.metadata_json)
    assert metadata["ai_profile"] == {
        "contract": "mem.ai-profile/v1",
        "id": "idealab-quality-v1",
        "revision": "2026-07-29",
        "pipeline_revision": "file-enrichment-v1",
    }
    assert metadata["managed_usage"]["stages"] == {"embedding": "succeeded"}


def test_published_v1_query_projection_rejects_unpublished_openai_model():
    profile = {
        "contract": "mem.ai-profile/v1",
        "id": "idealab-quality-v1",
        "revision": "2026-07-29",
        "pipeline_revision": "file-enrichment-v1",
        "data_egress": "managed_idealab",
        "embedding": _stage(True, "openai:unpublished-model", 768),
        "visual_embedding": _stage(True, "clip:ViT-B-32", 512),
        "llm": _stage(False, ""),
        "vlm": _stage(False, ""),
        "asr": _stage(False, ""),
        "rerank": _stage(False, ""),
    }

    with pytest.raises(AIProfileError, match="invalid ai_profile"):
        parse_ai_profile({"ai_profile": profile})


@pytest.mark.parametrize(
    "base_url",
    [
        "http://localhost:11434",
        "http://127.0.0.1:11434",
        "http://127.42.0.9:11434",
        "https://[::1]:11434",
    ],
)
@pytest.mark.parametrize("profile_id", ["local-fast-v1", "local-fast-v2"])
def test_local_profile_accepts_only_loopback_ollama_binding(
    env,
    base_url,
    profile_id,
):
    env(OLLAMA_BASE_URL=base_url)
    profile = _profile(
        data_egress="local_only",
        visual_embedding=False,
        llm=False,
        vlm=False,
        asr=False,
    )
    profile["id"] = profile_id
    profile["embedding"]["provider"] = "ollama:qwen3-embedding:0.6b"

    parsed = parse_ai_profile({"ai_profile": profile})

    assert parsed is not None
    assert parsed.data_egress == "local_only"


@pytest.mark.parametrize("profile_id", ["local-fast-v1", "local-fast-v2"])
def test_local_profile_rejects_remote_ollama_before_storage_or_network(
    monkeypatch,
    env,
    profile_id,
):
    env(OLLAMA_BASE_URL="https://remote-ollama.invalid")
    profile = _profile(
        data_egress="local_only",
        visual_embedding=False,
        llm=False,
        vlm=False,
        asr=False,
    )
    profile["id"] = profile_id
    profile["embedding"]["provider"] = "ollama:qwen3-embedding:0.6b"
    pb, pbg = _protobuf_modules()
    server_module = importlib.import_module("mem_worker.server")
    ollama_module = importlib.import_module("mem_worker.providers.ollama")
    monkeypatch.setattr(
        server_module,
        "fetch_bytes",
        lambda _uri: pytest.fail("invalid local binding must not fetch source bytes"),
    )
    monkeypatch.setattr(
        ollama_module.requests,
        "post",
        lambda *args, **kwargs: pytest.fail("invalid local binding must not call Ollama"),
    )

    response = ProcessorServicer(pb, pbg).Process(
        _request(pb, {"ai_profile": profile}),
        context=None,
    )

    assert response.status == pb.STATUS_FAILED
    assert response.error == "invalid ai_profile"
    assert "remote-ollama" not in response.error


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

    request = _request(
        pb,
        {
            "embedding_provider": "ollama:ignored-legacy-override",
            "llm_provider": "ollama:ignored-legacy-override",
            "ai_profile": profile,
        },
    )
    response = _process_managed(request, pb, pbg)

    assert response.status == pb.STATUS_OK
    assert embedding_calls == [("idealab:text-embedding-3-large", 768)]
    assert llm_specs == ["idealab:qwen3.7-max-2026-06-08"]
    assert llm.calls == 1
    metadata = json.loads(response.metadata_json)
    assert metadata["ai_profile"] == {
        "contract": "mem.ai-profile/v1",
        "id": "idealab-quality-v2",
        "revision": "2026-07-30.1",
        "pipeline_revision": "file-enrichment-v2",
    }
    assert metadata["managed_usage"] == {
        "contract": "mem.managed-stage-receipt/v1",
        "stages": {"embedding": "succeeded", "llm": "succeeded"},
    }
    assert all(
        annotation["analysis_version"] == "file-enrichment-v2"
        for annotation in metadata["annotations"]
    )


def test_profile_dimension_overrides_global_openai_dimension(env, monkeypatch):
    _required_auth_env(
        env,
        OPENAI_EMBEDDING_DIMENSIONS="1024",
        IDEALAB_API_KEY="test-key",
        IDEALAB_BASE_URL="https://idealab.invalid/compatible",
    )
    provider = get_embedding_provider("idealab:text-embedding-3-large", dimensions=768)
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


def test_idealab_binding_never_inherits_generic_openai_settings(env, monkeypatch):
    _required_auth_env(
        env,
        OPENAI_API_KEY="generic-key",
        OPENAI_BASE_URL="https://api.openai.com",
    )
    monkeypatch.setenv("OPENAI_API_KEY", "generic-key")
    monkeypatch.setenv("OPENAI_BASE_URL", "https://api.openai.com")
    monkeypatch.delenv("IDEALAB_API_KEY", raising=False)
    monkeypatch.delenv("IDEALAB_BASE_URL", raising=False)
    from mem_worker.config import get_settings

    get_settings.cache_clear()
    with pytest.raises(ProviderError, match="IDEALAB_API_KEY not set"):
        get_embedding_provider("idealab:text-embedding-3-large", dimensions=768)


@pytest.mark.parametrize(
    "base_url",
    [
        "http://idealab.invalid",
        "https://user:secret@idealab.invalid",
        "https://idealab.invalid/path?token=secret",
    ],
)
def test_idealab_binding_rejects_unsafe_endpoint(env, base_url):
    _required_auth_env(
        env,
        IDEALAB_API_KEY="test-key",
        IDEALAB_BASE_URL=base_url,
    )

    with pytest.raises(ValueError, match="absolute HTTPS URL"):
        get_embedding_provider("idealab:text-embedding-3-large", dimensions=768)


def test_idealab_binding_does_not_follow_redirects(env, monkeypatch):
    _required_auth_env(
        env,
        IDEALAB_API_KEY="test-key",
        IDEALAB_BASE_URL="https://idealab.invalid/compatible",
    )
    provider = get_embedding_provider("idealab:text-embedding-3-large", dimensions=768)
    captured = {}

    class Response:
        ok = True
        status_code = 200

        @staticmethod
        def json():
            return {"data": [{"embedding": [0.0] * 768}]}

    def post(url, **kwargs):
        captured.update({"url": url, **kwargs})
        return Response()

    openai_module = importlib.import_module("mem_worker.providers.openai")
    monkeypatch.setattr(openai_module.requests, "post", post)

    assert provider.embed_text(["redirect boundary"]) == [[0.0] * 768]
    assert captured["url"] == "https://idealab.invalid/compatible/v1/embeddings"
    assert captured["allow_redirects"] is False


def test_short_and_empty_text_release_uninvoked_managed_stages(monkeypatch):
    pb, pbg = _protobuf_modules()
    profile = _profile(visual_embedding=False, vlm=False, asr=False)
    providers = importlib.import_module("mem_worker.providers")
    text_module = importlib.import_module("mem_worker.processors.text")
    server_module = importlib.import_module("mem_worker.server")
    monkeypatch.setattr(
        providers,
        "get_embedding_provider",
        lambda _spec, *, dimensions=None: _Embedding(),
    )
    monkeypatch.setattr(
        text_module,
        "get_llm_provider",
        lambda _spec: pytest.fail("short or empty text must not invoke the LLM"),
    )

    for source, expected in [
        (
            b"short source",
            {"embedding": "succeeded", "llm": "not_invoked"},
        ),
        (
            b" \n\t",
            {"embedding": "not_invoked", "llm": "not_invoked"},
        ),
    ]:
        monkeypatch.setattr(server_module, "fetch_bytes", lambda _uri, data=source: data)
        request = _request(
            pb,
            {"ai_profile": profile},
            source=source,
        )
        response = _process_managed(request, pb, pbg)
        metadata = json.loads(response.metadata_json)
        assert metadata["managed_usage"] == {
            "contract": "mem.managed-stage-receipt/v1",
            "stages": expected,
        }


def test_managed_provider_failure_is_indeterminate_per_stage(monkeypatch):
    pb, pbg = _protobuf_modules()
    profile = _profile(visual_embedding=False, vlm=False, asr=False)
    providers = importlib.import_module("mem_worker.providers")
    text_module = importlib.import_module("mem_worker.processors.text")
    server_module = importlib.import_module("mem_worker.server")

    class FailingEmbedding(_Embedding):
        def embed_text(self, texts):
            raise ProviderError("upstream detail must stay private")

    monkeypatch.setattr(
        providers,
        "get_embedding_provider",
        lambda _spec, *, dimensions=None: FailingEmbedding(),
    )
    monkeypatch.setattr(
        text_module,
        "get_llm_provider",
        lambda _spec: pytest.fail("short text must not invoke the LLM"),
    )
    monkeypatch.setattr(server_module, "fetch_bytes", lambda _uri: b"short source")

    request = _request(
        pb,
        {"ai_profile": profile},
        source=b"short source",
    )
    response = _process_managed(request, pb, pbg)

    assert response.status == pb.STATUS_PARTIAL
    metadata = json.loads(response.metadata_json)
    assert metadata["managed_usage"]["stages"] == {
        "embedding": "indeterminate",
        "llm": "not_invoked",
    }
    assert "upstream detail" not in response.error
    assert "upstream detail" not in response.metadata_json.decode()


def test_unusable_managed_embedding_is_not_reported_succeeded(monkeypatch):
    pb, pbg = _protobuf_modules()
    profile = _profile(visual_embedding=False, vlm=False, asr=False)
    providers = importlib.import_module("mem_worker.providers")
    server_module = importlib.import_module("mem_worker.server")

    class WrongDimensionEmbedding(_Embedding):
        def embed_text(self, texts):
            return [[0.0] * 767 for _ in texts]

    monkeypatch.setattr(
        providers,
        "get_embedding_provider",
        lambda _spec, *, dimensions=None: WrongDimensionEmbedding(),
    )
    monkeypatch.setattr(server_module, "fetch_bytes", lambda _uri: b"short source")

    request = _request(
        pb,
        {"ai_profile": profile},
        source=b"short source",
    )
    response = _process_managed(request, pb, pbg)

    assert response.status == pb.STATUS_PARTIAL
    metadata = json.loads(response.metadata_json)
    assert metadata["managed_usage"]["stages"] == {
        "embedding": "indeterminate",
        "llm": "not_invoked",
    }
    assert response.embeddings == {}


def test_profiled_pdf_parse_failure_releases_uninvoked_stages(monkeypatch):
    pb, pbg = _protobuf_modules()
    profile = _profile(visual_embedding=False, vlm=False, asr=False)
    server_module = importlib.import_module("mem_worker.server")
    monkeypatch.setattr(server_module, "fetch_bytes", lambda _uri: b"not a PDF")
    request = _request(
        pb,
        {"ai_profile": profile},
        source=b"not a PDF",
    )
    request.mime = "application/pdf; version=1.7"

    response = _process_managed(request, pb, pbg)

    assert response.status == pb.STATUS_PARTIAL
    assert json.loads(response.metadata_json)["managed_usage"]["stages"] == {
        "embedding": "not_invoked",
        "llm": "not_invoked",
    }


def test_missing_processor_stage_receipt_fails_closed(monkeypatch):
    pb, pbg = _protobuf_modules()
    profile = _profile(visual_embedding=False, vlm=False, asr=False)
    server_module = importlib.import_module("mem_worker.server")
    servicer = _managed_servicer(pb, pbg)

    class ProcessorWithoutReceipt:
        name = "text"

        @staticmethod
        def process(_file):
            return ProcessResult(processor="text")

    monkeypatch.setattr(server_module, "fetch_bytes", lambda _uri: b"source")
    monkeypatch.setattr(
        servicer,
        "_pick_processor",
        lambda _mime, _options, *, ai_profile=None: ProcessorWithoutReceipt(),
    )

    request = _request(
        pb,
        {"ai_profile": profile},
        source=b"source",
    )
    response = _process_managed(request, pb, pbg, servicer=servicer)

    assert json.loads(response.metadata_json)["managed_usage"]["stages"] == {
        "embedding": "indeterminate",
        "llm": "indeterminate",
    }


def test_query_storage_uri_and_failure_detail_never_enter_logs_or_response(
    monkeypatch,
    capsys,
):
    pb, pbg = _protobuf_modules()
    profile = _profile(visual_embedding=False, vlm=False, asr=False)
    secret = "private query about acquisition plans"
    encoded = base64.b64encode(secret.encode()).decode()
    request = _request(pb, {"ai_profile": profile})
    request.storage_uri = "data:text/plain;base64," + encoded
    server_module = importlib.import_module("mem_worker.server")
    monkeypatch.setattr(
        server_module,
        "fetch_bytes",
        lambda _uri: (_ for _ in ()).throw(StorageError(secret)),
    )

    response = _process_managed(request, pb, pbg)
    captured = capsys.readouterr()
    rendered = captured.out + captured.err + response.error + response.metadata_json.decode()

    assert response.status == pb.STATUS_FAILED
    assert response.error == "storage unavailable"
    assert secret not in rendered
    assert encoded not in rendered
    assert json.loads(response.metadata_json)["managed_usage"]["stages"] == {
        "embedding": "not_invoked",
        "llm": "not_invoked",
    }
