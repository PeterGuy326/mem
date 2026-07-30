"""Security contract tests for authenticated memd -> Worker gRPC calls."""

from __future__ import annotations

import base64
import hashlib
import importlib
import json
import threading
from dataclasses import dataclass

import grpc
import pytest
from redis.exceptions import RedisError

from mem_worker.auth import (
    AUTH_SIGNATURE_HEADER,
    HEALTH_METHOD,
    PROCESS_METHOD,
    PROCESS_SCOPE,
    READINESS_SCOPE_PREFIX,
    RESPONSE_CONTRACT,
    RESPONSE_CONTRACT_HEADER,
    RESPONSE_KEY_ID_HEADER,
    RESPONSE_NONCE_HEADER,
    RESPONSE_SIGNATURE_HEADER,
    AuthenticationUnavailableError,
    RedisReplayStore,
    RequestAuthenticationError,
    RequestAuthenticator,
    build_request_metadata,
    deterministic_message_sha256,
    response_signature,
)
from mem_worker.server import ProcessorServicer

_KEY = bytes(range(32))
_KEY_ID = "memd-primary"
_NOW = 1_785_363_200
_NONCE = base64.urlsafe_b64encode(b"\x42" * 24).decode("ascii").rstrip("=")
_SOURCE = b"authenticated worker source"


class Aborted(RuntimeError):
    def __init__(self, code: grpc.StatusCode, detail: str):
        super().__init__(detail)
        self.code = code
        self.detail = detail


class Context:
    def __init__(self, metadata=()):
        self._metadata = tuple(metadata)
        self.trailing_metadata: tuple[tuple[str, str], ...] | None = None

    def invocation_metadata(self):
        return self._metadata

    def set_trailing_metadata(self, metadata):
        self.trailing_metadata = tuple(metadata)

    def abort(self, code, detail):
        raise Aborted(code, detail)


class SharedReplayStore:
    """Thread-safe fake with the same atomic one-time decision as Redis NX."""

    def __init__(self):
        self._lock = threading.Lock()
        self.claims: set[tuple[str, str]] = set()
        self.available = True
        self.pings = 0

    def claim(self, key_id: str, nonce: str, ttl_seconds: int) -> bool:
        if not self.available:
            raise AuthenticationUnavailableError("unavailable")
        assert ttl_seconds == 600
        with self._lock:
            value = (key_id, nonce)
            if value in self.claims:
                return False
            self.claims.add(value)
            return True

    def ping(self) -> None:
        if not self.available:
            raise AuthenticationUnavailableError("unavailable")
        self.pings += 1


def _protobuf_modules():
    pb = importlib.import_module("mem_worker.proto.processor_pb2")
    pbg = importlib.import_module("mem_worker.proto.processor_pb2_grpc")
    return pb, pbg


def _request(pb, *, source: bytes = _SOURCE, golden: bool = False):
    if golden:
        return pb.ProcessRequest(
            file_id="file-123",
            storage_uri="s3://mem/workspaces/w/files/f",
            mime="text/plain",
            sha256="0123456789abcdef" * 4,
            user_id="user-456",
            name="notes.txt",
            options_json=b'{"ai_profile":{"id":"local-fast-v2"}}',
        )
    return pb.ProcessRequest(
        file_id="00000000-0000-0000-0000-000000000054",
        storage_uri="data:text/plain;base64," + base64.b64encode(source).decode("ascii"),
        mime="text/plain",
        sha256=hashlib.sha256(source).hexdigest(),
        user_id="00000000-0000-0000-0000-000000000001",
        name="auth.txt",
        options_json=b'{"tag_hint":"auth"}',
    )


def _authenticator(
    store: SharedReplayStore | None = None,
    *,
    idealab: bool = True,
    openai: bool = True,
):
    return RequestAuthenticator(
        mode="required",
        key_id=_KEY_ID,
        key=_KEY,
        replay_store=store or SharedReplayStore(),
        clock=lambda: _NOW,
        idealab_binding_ready=lambda: idealab,
        openai_binding_ready=lambda: openai,
    )


def _metadata(
    request,
    *,
    nonce: str = _NONCE,
    timestamp: int = _NOW,
    method: str = PROCESS_METHOD,
    scope: str = PROCESS_SCOPE,
):
    return build_request_metadata(
        request,
        key=_KEY,
        key_id=_KEY_ID,
        timestamp=timestamp,
        nonce=nonce,
        method=method,
        scope=scope,
    )


def _metadata_dict(metadata):
    return dict(metadata or ())


def test_cross_language_request_and_response_golden_contract():
    pb, _ = _protobuf_modules()
    request = _request(pb, golden=True)
    metadata = _metadata(request)

    # These literals and the complete request fixture above are duplicated in
    # server/internal/workerclient/auth_test.go. Either implementation drifting
    # changes the deterministic protobuf digest or the URL-safe HMAC.
    assert deterministic_message_sha256(request) == (
        "6b125310b228c3e22c0736a09a90c574a55722d48878fb618272424e297d080e"
    )
    assert dict(metadata)[AUTH_SIGNATURE_HEADER] == "kaLOhFn8FjcXZM8iIsxwLlfjrBj033Vgjf7NvnUC5mA"

    store = SharedReplayStore()
    authenticator = _authenticator(store)
    context = Context(metadata)
    authenticated = authenticator.authenticate(
        request,
        context,
        method=PROCESS_METHOD,
        scope=PROCESS_SCOPE,
    )
    response = pb.ProcessResponse(status=pb.STATUS_OK, processor="text")
    authenticator.set_response_trailers(authenticated, response, context)

    trailers = _metadata_dict(context.trailing_metadata)
    assert trailers == {
        RESPONSE_CONTRACT_HEADER: RESPONSE_CONTRACT,
        RESPONSE_KEY_ID_HEADER: _KEY_ID,
        RESPONSE_NONCE_HEADER: _NONCE,
        RESPONSE_SIGNATURE_HEADER: "SiM53fRN6wY1OD7ja6YgyYHdeEVjMRDMet7Pc96RFEY",
    }


@pytest.mark.parametrize(
    ("field", "replacement"),
    [
        ("file_id", "tampered"),
        ("storage_uri", "data:text/plain;base64,dGFtcGVyZWQ="),
        ("mime", "application/pdf"),
        ("sha256", "0" * 64),
        ("user_id", "tampered"),
        ("name", "tampered.txt"),
        ("options_json", b'{"tag_hint":"tampered"}'),
    ],
)
def test_request_body_tamper_fails_before_replay_claim(field, replacement):
    pb, _ = _protobuf_modules()
    original = _request(pb)
    metadata = _metadata(original)
    tampered = pb.ProcessRequest()
    tampered.CopyFrom(original)
    setattr(tampered, field, replacement)
    store = SharedReplayStore()

    with pytest.raises(RequestAuthenticationError):
        _authenticator(store).authenticate(
            tampered,
            Context(metadata),
            method=PROCESS_METHOD,
            scope=PROCESS_SCOPE,
        )

    assert store.claims == set()


@pytest.mark.parametrize("timestamp", [_NOW - 301, _NOW + 31])
def test_stale_and_future_requests_are_rejected_without_claim(timestamp):
    pb, _ = _protobuf_modules()
    request = _request(pb)
    store = SharedReplayStore()

    with pytest.raises(RequestAuthenticationError):
        _authenticator(store).authenticate(
            request,
            Context(_metadata(request, timestamp=timestamp)),
            method=PROCESS_METHOD,
            scope=PROCESS_SCOPE,
        )

    assert store.claims == set()


def test_duplicate_unknown_and_partial_auth_metadata_are_rejected():
    pb, _ = _protobuf_modules()
    request = _request(pb)
    valid = list(_metadata(request))

    cases = [
        valid + [valid[2]],
        valid + [("x-mem-auth-unknown", "value")],
        valid[:-1],
    ]
    for metadata in cases:
        with pytest.raises(RequestAuthenticationError):
            _authenticator().authenticate(
                request,
                Context(metadata),
                method=PROCESS_METHOD,
                scope=PROCESS_SCOPE,
            )


def test_disabled_mode_rejects_any_auth_metadata_and_required_rejects_unsigned():
    pb, _ = _protobuf_modules()
    request = _request(pb)

    with pytest.raises(RequestAuthenticationError):
        RequestAuthenticator(mode="disabled").authenticate(
            request,
            Context((("x-mem-auth-unknown", "value"),)),
            method=PROCESS_METHOD,
            scope=PROCESS_SCOPE,
        )
    with pytest.raises(RequestAuthenticationError):
        _authenticator().authenticate(
            request,
            Context(),
            method=PROCESS_METHOD,
            scope=PROCESS_SCOPE,
        )


def test_bad_signature_does_not_burn_nonce_then_valid_request_succeeds():
    pb, _ = _protobuf_modules()
    request = _request(pb)
    metadata = list(_metadata(request))
    metadata[-1] = (AUTH_SIGNATURE_HEADER, "A" * 43)
    store = SharedReplayStore()
    authenticator = _authenticator(store)

    with pytest.raises(RequestAuthenticationError):
        authenticator.authenticate(
            request,
            Context(metadata),
            method=PROCESS_METHOD,
            scope=PROCESS_SCOPE,
        )
    assert store.claims == set()

    assert (
        authenticator.authenticate(
            request,
            Context(_metadata(request)),
            method=PROCESS_METHOD,
            scope=PROCESS_SCOPE,
        )
        is not None
    )


def test_shared_replay_store_rejects_same_nonce_across_worker_instances():
    pb, _ = _protobuf_modules()
    request = _request(pb)
    metadata = _metadata(request)
    store = SharedReplayStore()
    worker_a = _authenticator(store)
    worker_b = _authenticator(store)

    assert worker_a.authenticate(
        request,
        Context(metadata),
        method=PROCESS_METHOD,
        scope=PROCESS_SCOPE,
    )
    with pytest.raises(RequestAuthenticationError):
        worker_b.authenticate(
            request,
            Context(metadata),
            method=PROCESS_METHOD,
            scope=PROCESS_SCOPE,
        )


def test_redis_replay_store_uses_atomic_set_nx_ex_and_fails_closed():
    @dataclass
    class FakeRedis:
        fail: bool = False
        calls: list[tuple[str, bytes, bool, int]] | None = None

        def __post_init__(self):
            self.calls = []

        def set(self, key, value, *, nx, ex):
            if self.fail:
                raise RedisError("secret backend detail")
            self.calls.append((key, value, nx, ex))
            return True

        def ping(self):
            if self.fail:
                raise RedisError("secret backend detail")
            return True

    client = FakeRedis()
    store = RedisReplayStore("redis://unused.invalid", client=client)

    assert store.claim(_KEY_ID, _NONCE, 600) is True
    assert client.calls is not None
    key, value, nx, ex = client.calls[0]
    assert key.startswith("mem:worker-auth:v1:")
    assert _NONCE not in key
    assert (value, nx, ex) == (b"1", True, 600)
    store.ping()

    client.fail = True
    with pytest.raises(
        AuthenticationUnavailableError,
        match="request authentication is unavailable",
    ):
        store.claim(_KEY_ID, _NONCE, 600)
    with pytest.raises(AuthenticationUnavailableError):
        store.ping()


def test_response_proof_is_bound_to_method_scope_nonce_and_response_body():
    pb, _ = _protobuf_modules()
    response = pb.ProcessResponse(status=pb.STATUS_OK, processor="text")
    digest = deterministic_message_sha256(response)
    signature = response_signature(
        key=_KEY,
        method=PROCESS_METHOD,
        scope=PROCESS_SCOPE,
        key_id=_KEY_ID,
        nonce=_NONCE,
        response_body_sha256=digest,
    )

    for changed in (
        response_signature(
            key=_KEY,
            method=HEALTH_METHOD,
            scope=PROCESS_SCOPE,
            key_id=_KEY_ID,
            nonce=_NONCE,
            response_body_sha256=digest,
        ),
        response_signature(
            key=_KEY,
            method=PROCESS_METHOD,
            scope=PROCESS_SCOPE,
            key_id=_KEY_ID,
            nonce=base64.urlsafe_b64encode(b"x" * 24).decode().rstrip("="),
            response_body_sha256=digest,
        ),
        response_signature(
            key=_KEY,
            method=PROCESS_METHOD,
            scope=PROCESS_SCOPE,
            key_id=_KEY_ID,
            nonce=_NONCE,
            response_body_sha256="0" * 64,
        ),
    ):
        assert changed != signature


def test_process_authentication_precedes_logging_json_fetch_and_provider(monkeypatch):
    pb, pbg = _protobuf_modules()
    server_module = importlib.import_module("mem_worker.server")
    servicer = ProcessorServicer(
        pb,
        pbg,
        authenticator=_authenticator(),
    )
    monkeypatch.setattr(
        server_module.log,
        "info",
        lambda *args, **kwargs: pytest.fail("unauthenticated request was logged"),
    )
    monkeypatch.setattr(
        server_module,
        "fetch_bytes",
        lambda _uri: pytest.fail("unauthenticated request fetched storage"),
    )

    with pytest.raises(Aborted) as aborted:
        servicer.Process(_request(pb), Context())

    assert aborted.value.code == grpc.StatusCode.UNAUTHENTICATED


@pytest.mark.parametrize(
    ("provider_spec", "idealab", "openai"),
    [
        ("idealab:text-embedding-3-large", True, False),
        ("openai:text-embedding-3-large", False, True),
    ],
)
def test_signed_readiness_checks_exact_binding_and_returns_response_proof(
    provider_spec,
    idealab,
    openai,
):
    pb, pbg = _protobuf_modules()
    store = SharedReplayStore()
    authenticator = _authenticator(store, idealab=idealab, openai=openai)
    servicer = ProcessorServicer(pb, pbg, authenticator=authenticator)
    request = pb.HealthCheckRequest()
    scope = READINESS_SCOPE_PREFIX + provider_spec
    context = Context(
        _metadata(
            request,
            method=HEALTH_METHOD,
            scope=scope,
        )
    )

    response = servicer.HealthCheck(request, context)

    assert response.status == pb.HealthCheckResponse.SERVING
    assert store.pings == 1
    trailers = _metadata_dict(context.trailing_metadata)
    assert trailers[RESPONSE_CONTRACT_HEADER] == RESPONSE_CONTRACT
    assert trailers[RESPONSE_KEY_ID_HEADER] == _KEY_ID
    assert trailers[RESPONSE_NONCE_HEADER] == _NONCE
    assert trailers[RESPONSE_SIGNATURE_HEADER]


def test_required_auth_startup_fails_when_shared_replay_store_is_unavailable():
    store = SharedReplayStore()
    store.available = False

    with pytest.raises(
        AuthenticationUnavailableError,
        match="unavailable",
    ):
        _authenticator(store).startup_check()


def test_unsigned_healthcheck_remains_plain_liveness_in_required_mode():
    pb, pbg = _protobuf_modules()
    servicer = ProcessorServicer(
        pb,
        pbg,
        authenticator=_authenticator(),
    )
    context = Context()

    response = servicer.HealthCheck(pb.HealthCheckRequest(), context)

    assert response.status == pb.HealthCheckResponse.SERVING
    assert context.trailing_metadata is None


def test_readiness_rejects_arbitrary_provider_and_wrong_binding():
    pb, pbg = _protobuf_modules()
    request = pb.HealthCheckRequest()
    servicer = ProcessorServicer(
        pb,
        pbg,
        authenticator=_authenticator(idealab=False, openai=True),
    )

    arbitrary = READINESS_SCOPE_PREFIX + "idealab:arbitrary-model"
    with pytest.raises(Aborted) as invalid:
        servicer.HealthCheck(
            request,
            Context(
                _metadata(
                    request,
                    method=HEALTH_METHOD,
                    scope=arbitrary,
                )
            ),
        )
    assert invalid.value.code == grpc.StatusCode.UNAUTHENTICATED

    exact = READINESS_SCOPE_PREFIX + "idealab:text-embedding-3-large"
    with pytest.raises(Aborted) as unavailable:
        servicer.HealthCheck(
            request,
            Context(
                _metadata(
                    request,
                    nonce=base64.urlsafe_b64encode(b"y" * 24).decode().rstrip("="),
                    method=HEALTH_METHOD,
                    scope=exact,
                )
            ),
        )
    assert unavailable.value.code == grpc.StatusCode.UNAVAILABLE


def _managed_profile_options() -> bytes:
    profile = {
        "contract": "mem.ai-profile/v1",
        "id": "idealab-quality-v2",
        "revision": "2026-07-30.1",
        "pipeline_revision": "file-enrichment-v2",
        "data_egress": "managed_idealab",
        "embedding": {
            "enabled": True,
            "provider": "idealab:text-embedding-3-large",
            "dimensions": 768,
        },
        "visual_embedding": {"enabled": False},
        "llm": {
            "enabled": True,
            "provider": "idealab:qwen3.7-max-2026-06-08",
        },
        "vlm": {"enabled": False},
        "asr": {"enabled": False},
        "rerank": {"enabled": False},
    }
    return json.dumps({"ai_profile": profile}).encode()


def test_managed_content_hash_mismatch_never_reaches_provider(monkeypatch):
    pb, pbg = _protobuf_modules()
    request = _request(pb, source=b"signed source")
    request.sha256 = "0" * 64
    request.options_json = _managed_profile_options()
    server_module = importlib.import_module("mem_worker.server")
    processor_calls = 0

    class Processor:
        name = "text"

        def process(self, _file):
            nonlocal processor_calls
            processor_calls += 1
            raise AssertionError("provider pipeline must not run")

    monkeypatch.setattr(server_module, "fetch_bytes", lambda _uri: b"signed source")
    servicer = ProcessorServicer(
        pb,
        pbg,
        authenticator=_authenticator(),
    )
    monkeypatch.setattr(
        servicer,
        "_pick_processor",
        lambda _mime, _options, *, ai_profile=None: Processor(),
    )
    context = Context(_metadata(request))

    response = servicer.Process(request, context)

    assert response.status == pb.STATUS_FAILED
    assert response.error == "content integrity check failed"
    assert processor_calls == 0
    assert json.loads(response.metadata_json)["managed_usage"]["stages"] == {
        "embedding": "not_invoked",
        "llm": "not_invoked",
    }
    assert _metadata_dict(context.trailing_metadata)[RESPONSE_SIGNATURE_HEADER]


@pytest.mark.parametrize("invalid_sha256", ["", "0" * 63, "g" * 64])
def test_managed_missing_or_malformed_content_hash_fails_before_fetch(
    monkeypatch,
    invalid_sha256,
):
    pb, pbg = _protobuf_modules()
    request = _request(pb)
    request.sha256 = invalid_sha256
    request.options_json = _managed_profile_options()
    server_module = importlib.import_module("mem_worker.server")
    monkeypatch.setattr(
        server_module,
        "fetch_bytes",
        lambda _uri: pytest.fail("invalid content hash must fail before storage fetch"),
    )
    servicer = ProcessorServicer(
        pb,
        pbg,
        authenticator=_authenticator(),
    )

    response = servicer.Process(request, Context(_metadata(request)))

    assert response.status == pb.STATUS_FAILED
    assert response.error == "content integrity check failed"
    assert json.loads(response.metadata_json)["managed_usage"]["stages"] == {
        "embedding": "not_invoked",
        "llm": "not_invoked",
    }


@pytest.mark.parametrize(
    "field",
    [
        "embedding_provider",
        "visual_embedding_provider",
        "llm_provider",
        "vlm_provider",
        "asr_provider",
        "future_provider",
    ],
)
def test_legacy_idealab_is_denied_even_when_request_signature_is_valid(
    monkeypatch,
    field,
):
    pb, pbg = _protobuf_modules()
    request = _request(pb)
    request.options_json = json.dumps({field: "idealab:text-embedding-3-large"}).encode()
    server_module = importlib.import_module("mem_worker.server")
    monkeypatch.setattr(
        server_module,
        "fetch_bytes",
        lambda _uri: pytest.fail("legacy Idealab must be denied before fetch"),
    )
    servicer = ProcessorServicer(
        pb,
        pbg,
        authenticator=_authenticator(),
    )

    response = servicer.Process(request, Context(_metadata(request)))

    assert response.status == pb.STATUS_FAILED
    assert response.error == "legacy network provider is not permitted"


@pytest.mark.parametrize(
    ("field", "provider"),
    [
        ("embedding_provider", "openai:text-embedding-3-large"),
        ("llm_provider", "openai:qwen3.7-max-2026-06-08"),
        ("vlm_provider", "openai:arbitrary-private-model"),
    ],
)
def test_signed_legacy_openai_is_denied_before_fetch_or_provider_when_binding_is_managed(
    monkeypatch,
    env,
    field,
    provider,
):
    _required_auth_env(
        env,
        MEM_OPENAI_MANAGED_BINDING="true",
        OPENAI_API_KEY="legacy-platform-key",
        OPENAI_BASE_URL="https://legacy-idealab.invalid/compatible",
    )
    pb, pbg = _protobuf_modules()
    request = _request(pb)
    request.options_json = json.dumps({field: provider}).encode()
    server_module = importlib.import_module("mem_worker.server")
    monkeypatch.setattr(
        server_module,
        "fetch_bytes",
        lambda _uri: pytest.fail("managed legacy OpenAI must be denied before fetch"),
    )
    servicer = ProcessorServicer(
        pb,
        pbg,
        authenticator=_authenticator(),
    )
    monkeypatch.setattr(
        servicer,
        "_pick_processor",
        lambda *_args, **_kwargs: pytest.fail(
            "managed legacy OpenAI must be denied before provider construction"
        ),
    )
    context = Context(_metadata(request))

    response = servicer.Process(request, context)

    assert response.status == pb.STATUS_FAILED
    assert response.error == "legacy network provider is not permitted"
    assert response.metadata_json == b""
    assert _metadata_dict(context.trailing_metadata)[RESPONSE_SIGNATURE_HEADER]


@pytest.mark.parametrize(
    "provider",
    ["openai:private-model", "anthropic:private-model"],
)
def test_private_network_byom_legacy_override_remains_compatible(
    monkeypatch,
    env,
    provider,
):
    env(
        MEM_DEPLOYMENT_MODE="private",
        MEM_WORKER_AUTH_MODE="disabled",
        MEM_OPENAI_MANAGED_BINDING="false",
        OPENAI_API_KEY="private-user-key",
        OPENAI_BASE_URL="",
    )
    pb, pbg = _protobuf_modules()
    request = _request(pb)
    request.options_json = json.dumps({"embedding_provider": provider}).encode()
    server_module = importlib.import_module("mem_worker.server")
    monkeypatch.setattr(server_module, "fetch_bytes", lambda _uri: _SOURCE)

    class Processor:
        name = "text"

        def process(self, _file):
            return server_module.ProcessResult(processor=self.name)

    servicer = ProcessorServicer(
        pb,
        pbg,
        authenticator=RequestAuthenticator(mode="disabled"),
    )
    monkeypatch.setattr(
        servicer,
        "_pick_processor",
        lambda *_args, **_kwargs: Processor(),
    )

    response = servicer.Process(request, Context())

    assert response.status == pb.STATUS_OK


@pytest.mark.parametrize(
    ("field", "provider"),
    [
        ("embedding_provider", "openai:private-model"),
        ("llm_provider", "anthropic:claude-private"),
        ("future_provider", "custom-network:private-model"),
    ],
)
def test_signed_saas_legacy_network_provider_is_denied_before_fetch_or_construction(
    monkeypatch,
    env,
    field,
    provider,
):
    _required_auth_env(env, MEM_DEPLOYMENT_MODE="saas")
    pb, pbg = _protobuf_modules()
    request = _request(pb)
    request.options_json = json.dumps({field: provider}).encode()
    server_module = importlib.import_module("mem_worker.server")
    monkeypatch.setattr(
        server_module,
        "fetch_bytes",
        lambda _uri: pytest.fail("SaaS legacy network provider reached storage"),
    )
    servicer = ProcessorServicer(
        pb,
        pbg,
        authenticator=_authenticator(),
    )
    monkeypatch.setattr(
        servicer,
        "_pick_processor",
        lambda *_args, **_kwargs: pytest.fail(
            "SaaS legacy network provider reached provider construction"
        ),
    )
    context = Context(_metadata(request))

    response = servicer.Process(request, context)

    assert response.status == pb.STATUS_FAILED
    assert response.error == "legacy network provider is not permitted"
    assert response.metadata_json == b""
    assert _metadata_dict(context.trailing_metadata)[RESPONSE_SIGNATURE_HEADER]


def test_signed_saas_local_legacy_override_remains_compatible(monkeypatch, env):
    _required_auth_env(env, MEM_DEPLOYMENT_MODE="saas")
    pb, pbg = _protobuf_modules()
    request = _request(pb)
    request.options_json = json.dumps({"embedding_provider": "ollama:local-model"}).encode()
    server_module = importlib.import_module("mem_worker.server")
    monkeypatch.setattr(server_module, "fetch_bytes", lambda _uri: _SOURCE)

    class Processor:
        name = "text"

        def process(self, _file):
            return server_module.ProcessResult(processor=self.name)

    servicer = ProcessorServicer(
        pb,
        pbg,
        authenticator=_authenticator(),
    )
    monkeypatch.setattr(
        servicer,
        "_pick_processor",
        lambda *_args, **_kwargs: Processor(),
    )

    response = servicer.Process(request, Context(_metadata(request)))

    assert response.status == pb.STATUS_OK


@pytest.mark.parametrize(
    "options",
    [
        {"tag_hint": "default-routing"},
        {"embedding_provider": "ollama:local-model"},
    ],
)
def test_signed_saas_nonloopback_ollama_config_drift_is_denied_before_fetch_or_construction(
    monkeypatch,
    env,
    options,
):
    _required_auth_env(env, MEM_DEPLOYMENT_MODE="saas")
    from mem_worker.config import get_settings

    settings = get_settings()
    monkeypatch.setattr(
        settings,
        "ollama_base_url",
        "https://remote-ollama.invalid",
    )
    pb, pbg = _protobuf_modules()
    request = _request(pb)
    request.options_json = json.dumps(options).encode()
    server_module = importlib.import_module("mem_worker.server")
    monkeypatch.setattr(
        server_module,
        "fetch_bytes",
        lambda _uri: pytest.fail("non-loopback SaaS Ollama reached storage"),
    )
    servicer = ProcessorServicer(
        pb,
        pbg,
        authenticator=_authenticator(),
    )
    monkeypatch.setattr(
        servicer,
        "_pick_processor",
        lambda *_args, **_kwargs: pytest.fail(
            "non-loopback SaaS Ollama reached provider construction"
        ),
    )

    response = servicer.Process(request, Context(_metadata(request)))

    assert response.status == pb.STATUS_FAILED
    assert response.error == "legacy network provider is not permitted"


def test_private_remote_ollama_legacy_remains_compatible(monkeypatch, env):
    env(
        MEM_DEPLOYMENT_MODE="private",
        MEM_WORKER_AUTH_MODE="disabled",
        OLLAMA_BASE_URL="https://private-ollama.example",
    )
    from mem_worker.config import get_settings

    assert get_settings().ollama_base_url == "https://private-ollama.example"
    pb, pbg = _protobuf_modules()
    request = _request(pb)
    request.options_json = json.dumps({"embedding_provider": "ollama:private-model"}).encode()
    server_module = importlib.import_module("mem_worker.server")
    monkeypatch.setattr(server_module, "fetch_bytes", lambda _uri: _SOURCE)

    class Processor:
        name = "text"

        def process(self, _file):
            return server_module.ProcessResult(processor=self.name)

    servicer = ProcessorServicer(
        pb,
        pbg,
        authenticator=RequestAuthenticator(mode="disabled"),
    )
    monkeypatch.setattr(
        servicer,
        "_pick_processor",
        lambda *_args, **_kwargs: Processor(),
    )

    response = servicer.Process(request, Context())

    assert response.status == pb.STATUS_OK


def _required_auth_env(env, **extra):
    values = {
        "MEM_DEPLOYMENT_MODE": "private",
        "MEM_WORKER_AUTH_MODE": "required",
        "MEM_WORKER_AUTH_KEY_ID": _KEY_ID,
        "MEM_WORKER_AUTH_KEY_B64": base64.b64encode(_KEY).decode("ascii"),
        "MEM_WORKER_AUTH_REPLAY_REDIS_URL": "redis://localhost:6379/7",
        **extra,
    }
    env(**values)


def test_settings_preserve_unsigned_private_openai_byom(env):
    env(
        MEM_DEPLOYMENT_MODE="private",
        MEM_WORKER_AUTH_MODE="disabled",
        OPENAI_API_KEY="private-user-key",
        OPENAI_BASE_URL="",
    )
    from mem_worker.config import get_settings

    settings = get_settings()

    assert settings.worker_auth_mode == "disabled"
    assert settings.openai_api_key == "private-user-key"
    assert settings.openai_managed_binding is False
    assert settings.openai_managed_binding_ready() is False


def test_settings_reject_explicit_openai_managed_binding_without_auth(env):
    env(
        MEM_WORKER_AUTH_MODE="disabled",
        MEM_OPENAI_MANAGED_BINDING="true",
        OPENAI_API_KEY="legacy-platform-key",
        OPENAI_BASE_URL="https://legacy-idealab.invalid/compatible",
    )
    from mem_worker.config import get_settings

    with pytest.raises(
        ValueError,
        match="MEM_OPENAI_MANAGED_BINDING requires MEM_WORKER_AUTH_MODE=required",
    ) as failure:
        get_settings()

    assert "legacy-platform-key" not in str(failure.value)


@pytest.mark.parametrize(
    "openai",
    [
        {},
        {
            "OPENAI_API_KEY": "legacy-platform-key",
            "OPENAI_BASE_URL": "https://legacy-idealab.invalid/compatible",
        },
    ],
)
def test_settings_reject_saas_worker_without_required_auth(env, openai):
    env(
        MEM_DEPLOYMENT_MODE="saas",
        MEM_WORKER_AUTH_MODE="disabled",
        **openai,
    )
    from mem_worker.config import get_settings

    with pytest.raises(
        ValueError,
        match="MEM_DEPLOYMENT_MODE=saas requires MEM_WORKER_AUTH_MODE=required",
    ) as failure:
        get_settings()

    assert "legacy-platform-key" not in str(failure.value)


def test_settings_reject_saas_openai_pair_without_managed_binding_flag(env):
    _required_auth_env(
        env,
        MEM_DEPLOYMENT_MODE="saas",
        MEM_OPENAI_MANAGED_BINDING="false",
        OPENAI_API_KEY="legacy-platform-key",
        OPENAI_BASE_URL="https://legacy-idealab.invalid/compatible",
    )
    from mem_worker.config import get_settings

    with pytest.raises(
        ValueError,
        match="SaaS OPENAI_API_KEY requires MEM_OPENAI_MANAGED_BINDING=true",
    ) as failure:
        get_settings()

    assert "legacy-platform-key" not in str(failure.value)


def test_settings_reject_managed_openai_flag_without_complete_pair(env):
    _required_auth_env(
        env,
        MEM_OPENAI_MANAGED_BINDING="true",
        OPENAI_API_KEY="",
        OPENAI_BASE_URL="",
    )
    from mem_worker.config import get_settings

    with pytest.raises(
        ValueError,
        match="MEM_OPENAI_MANAGED_BINDING requires OPENAI_API_KEY and OPENAI_BASE_URL",
    ):
        get_settings()


@pytest.mark.parametrize(
    "values",
    [
        {
            "MEM_WORKER_AUTH_MODE": "required",
            "MEM_WORKER_AUTH_KEY_ID": _KEY_ID,
            "MEM_WORKER_AUTH_REPLAY_REDIS_URL": "redis://localhost:6379/7",
        },
        {
            "MEM_WORKER_AUTH_MODE": "required",
            "MEM_WORKER_AUTH_KEY_ID": _KEY_ID,
            "MEM_WORKER_AUTH_KEY_B64": base64.b64encode(b"short").decode(),
            "MEM_WORKER_AUTH_REPLAY_REDIS_URL": "redis://localhost:6379/7",
        },
        {
            "MEM_WORKER_AUTH_MODE": "required",
            "MEM_WORKER_AUTH_KEY_ID": _KEY_ID,
            "MEM_WORKER_AUTH_KEY_B64": base64.b64encode(_KEY).decode(),
        },
    ],
)
def test_settings_reject_incomplete_required_auth(env, values):
    env(**values)
    from mem_worker.config import get_settings

    with pytest.raises(ValueError):
        get_settings()


@pytest.mark.parametrize("key_id", [".hidden", "_private", "-invalid", "含中文"])
def test_settings_and_authenticator_reject_noncanonical_key_ids(env, key_id):
    _required_auth_env(env, MEM_WORKER_AUTH_KEY_ID=key_id)
    from mem_worker.config import get_settings

    with pytest.raises(ValueError, match="KEY_ID"):
        get_settings()
    with pytest.raises(ValueError, match="incomplete"):
        RequestAuthenticator(
            mode="required",
            key_id=key_id,
            key=_KEY,
            replay_store=SharedReplayStore(),
        )


def test_settings_reject_managed_credentials_without_auth(env):
    env(
        MEM_WORKER_AUTH_MODE="disabled",
        IDEALAB_API_KEY="must-not-leak",
        IDEALAB_BASE_URL="https://idealab.invalid/v1",
    )
    from mem_worker.config import get_settings

    with pytest.raises(ValueError) as failure:
        get_settings()

    assert "must-not-leak" not in str(failure.value)


@pytest.mark.parametrize(
    "field",
    [
        "MEM_DEFAULT_EMBEDDING",
        "MEM_DEFAULT_VISUAL_EMBEDDING",
        "MEM_DEFAULT_LLM",
        "MEM_DEFAULT_VLM",
        "MEM_DEFAULT_ASR",
    ],
)
def test_settings_reject_legacy_idealab_defaults(env, field):
    env(**{field: "idealab:forbidden"})
    from mem_worker.config import get_settings

    with pytest.raises(
        ValueError,
        match="idealab providers require an authenticated AI profile",
    ):
        get_settings()


@pytest.mark.parametrize(
    "field",
    [
        "MEM_DEFAULT_EMBEDDING",
        "MEM_DEFAULT_VISUAL_EMBEDDING",
        "MEM_DEFAULT_LLM",
        "MEM_DEFAULT_VLM",
        "MEM_DEFAULT_ASR",
    ],
)
def test_settings_reject_openai_defaults_when_binding_is_managed(env, field):
    _required_auth_env(
        env,
        MEM_OPENAI_MANAGED_BINDING="true",
        OPENAI_API_KEY="legacy-platform-key",
        OPENAI_BASE_URL="https://legacy-idealab.invalid/compatible",
        **{field: "openai:forbidden"},
    )
    from mem_worker.config import get_settings

    with pytest.raises(
        ValueError,
        match="openai providers require an authenticated AI profile",
    ):
        get_settings()


@pytest.mark.parametrize(
    "field",
    [
        "MEM_DEFAULT_EMBEDDING",
        "MEM_DEFAULT_VISUAL_EMBEDDING",
        "MEM_DEFAULT_LLM",
        "MEM_DEFAULT_VLM",
        "MEM_DEFAULT_ASR",
    ],
)
def test_settings_reject_saas_network_provider_defaults(env, field):
    _required_auth_env(
        env,
        MEM_DEPLOYMENT_MODE="saas",
        **{field: "anthropic:forbidden"},
    )
    from mem_worker.config import get_settings

    with pytest.raises(
        ValueError,
        match="requires every MEM_DEFAULT_\\* provider to use a local runtime",
    ):
        get_settings()


@pytest.mark.parametrize(
    "base_url",
    [
        "http://localhost:11434",
        "http://127.0.0.1:11434",
        "http://127.42.0.9:11434",
        "https://[::1]:11434",
    ],
)
def test_settings_accept_saas_loopback_ollama_base_url(env, base_url):
    _required_auth_env(
        env,
        MEM_DEPLOYMENT_MODE="saas",
        OLLAMA_BASE_URL=base_url,
    )
    from mem_worker.config import get_settings

    assert get_settings().ollama_base_url == base_url


@pytest.mark.parametrize(
    "base_url",
    [
        "https://remote-ollama.invalid",
        "ftp://localhost:11434",
        "http://user:password@localhost:11434",
        "http://localhost:11434?target=remote",
        "http://localhost:11434#remote",
        "http://localhost:not-a-port",
        "http://localhost:99999",
    ],
)
def test_settings_reject_saas_nonloopback_or_dangerous_ollama_base_url(env, base_url):
    _required_auth_env(
        env,
        MEM_DEPLOYMENT_MODE="saas",
        OLLAMA_BASE_URL=base_url,
    )
    from mem_worker.config import get_settings

    with pytest.raises(
        ValueError,
        match="SaaS OLLAMA_BASE_URL must be an absolute loopback HTTP\\(S\\) URL",
    ):
        get_settings()


@pytest.mark.parametrize(
    ("field", "attribute"),
    [
        ("MEM_DEFAULT_EMBEDDING", "default_embedding"),
        ("MEM_DEFAULT_VISUAL_EMBEDDING", "default_visual_embedding"),
        ("MEM_DEFAULT_LLM", "default_llm"),
        ("MEM_DEFAULT_VLM", "default_vlm"),
        ("MEM_DEFAULT_ASR", "default_asr"),
    ],
)
def test_settings_accept_saas_blank_provider_defaults(env, field, attribute):
    _required_auth_env(
        env,
        MEM_DEPLOYMENT_MODE="saas",
        **{field: " \t "},
    )
    from mem_worker.config import get_settings

    assert not getattr(get_settings(), attribute).strip()


@pytest.mark.parametrize("spec", ["ollama", "ollama: ", " :model"])
def test_settings_reject_saas_nonempty_malformed_provider_defaults(env, spec):
    _required_auth_env(
        env,
        MEM_DEPLOYMENT_MODE="saas",
        MEM_DEFAULT_EMBEDDING=spec,
    )
    from mem_worker.config import get_settings

    with pytest.raises(
        ValueError,
        match="requires every non-empty MEM_DEFAULT_\\* to use",
    ):
        get_settings()


def test_settings_preserve_private_network_provider_defaults(env):
    env(
        MEM_DEPLOYMENT_MODE="private",
        MEM_WORKER_AUTH_MODE="disabled",
        MEM_OPENAI_MANAGED_BINDING="false",
        MEM_DEFAULT_EMBEDDING="openai:private-model",
        MEM_DEFAULT_LLM="anthropic:private-model",
    )
    from mem_worker.config import get_settings

    settings = get_settings()

    assert settings.default_embedding == "openai:private-model"
    assert settings.default_llm == "anthropic:private-model"


def test_settings_accept_complete_authenticated_v1_or_v2_binding(env):
    from mem_worker.config import get_settings

    _required_auth_env(
        env,
        MEM_OPENAI_MANAGED_BINDING="true",
        OPENAI_API_KEY="legacy-managed-key",
        OPENAI_BASE_URL="https://legacy-idealab.invalid/compatible",
    )
    v1 = get_settings()
    assert v1.openai_managed_binding_ready()
    assert v1.managed_binding_ready()

    get_settings.cache_clear()
    _required_auth_env(
        env,
        MEM_OPENAI_MANAGED_BINDING="false",
        OPENAI_API_KEY="",
        OPENAI_BASE_URL="",
        IDEALAB_API_KEY="hardened-managed-key",
        IDEALAB_BASE_URL="https://idealab.invalid/compatible",
    )
    v2 = get_settings()
    assert v2.idealab_binding_ready()
    assert v2.managed_binding_ready()


def test_settings_reject_incomplete_authenticated_binding(env):
    _required_auth_env(
        env,
        MEM_OPENAI_MANAGED_BINDING="true",
        OPENAI_API_KEY="managed-key",
        OPENAI_BASE_URL="",
    )
    from mem_worker.config import get_settings

    with pytest.raises(
        ValueError,
        match="OPENAI_API_KEY and OPENAI_BASE_URL",
    ):
        get_settings()
