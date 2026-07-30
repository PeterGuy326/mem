"""Authenticated memd -> Worker request boundary.

The managed Idealab credential lives only in the Worker.  A reachable gRPC
port must therefore not be sufficient authority to spend it.  This module
implements the versioned cross-language HMAC contract shared with memd,
including a Redis-backed one-time nonce claim that remains effective across
Worker replicas.

HMAC authenticates requests and successful responses; it does not encrypt the
gRPC channel.  Production deployments must still keep the Worker on a private
network and should use TLS/mTLS where available.
"""

from __future__ import annotations

import base64
import hashlib
import hmac
import re
import time
from collections.abc import Callable, Iterable, Sequence
from dataclasses import dataclass
from typing import Any, Protocol

import redis
from redis.exceptions import RedisError

from .config import Settings

REQUEST_CONTRACT = "mem.worker.hmac/v1"
RESPONSE_CONTRACT = "mem.worker.response-hmac/v1"
REQUEST_SIGNING_DOMAIN = "mem.worker.request-auth/v1"
RESPONSE_SIGNING_DOMAIN = "mem.worker.response-auth/v1"

PROCESS_METHOD = "/mem.worker.v1.ProcessorService/Process"
HEALTH_METHOD = "/mem.worker.v1.ProcessorService/HealthCheck"
PROCESS_SCOPE = "process"
READINESS_SCOPE_PREFIX = "readiness:"

_READINESS_PROVIDER_VENDORS = {
    "idealab:text-embedding-3-large": "idealab",
    "idealab:qwen3.7-max-2026-06-08": "idealab",
    "openai:text-embedding-3-large": "openai",
    "openai:qwen3.7-max-2026-06-08": "openai",
}

AUTH_CONTRACT_HEADER = "x-mem-auth-contract"
AUTH_KEY_ID_HEADER = "x-mem-auth-key-id"
AUTH_TIMESTAMP_HEADER = "x-mem-auth-timestamp"
AUTH_NONCE_HEADER = "x-mem-auth-nonce"
AUTH_SCOPE_HEADER = "x-mem-auth-scope"
AUTH_SIGNATURE_HEADER = "x-mem-auth-signature"

RESPONSE_CONTRACT_HEADER = "x-mem-auth-response-contract"
RESPONSE_KEY_ID_HEADER = "x-mem-auth-response-key-id"
RESPONSE_NONCE_HEADER = "x-mem-auth-response-nonce"
RESPONSE_SIGNATURE_HEADER = "x-mem-auth-response-signature"

_REQUEST_HEADERS = frozenset(
    {
        AUTH_CONTRACT_HEADER,
        AUTH_KEY_ID_HEADER,
        AUTH_TIMESTAMP_HEADER,
        AUTH_NONCE_HEADER,
        AUTH_SCOPE_HEADER,
        AUTH_SIGNATURE_HEADER,
    }
)
_AUTH_HEADER_PREFIX = "x-mem-auth-"
_KEY_ID_PATTERN = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,63}")
_TIMESTAMP_PATTERN = re.compile(r"[0-9]{1,20}")
_NONCE_BYTES = 24
_SIGNATURE_BYTES = hashlib.sha256().digest_size
_MAX_PAST_SECONDS = 300
_MAX_FUTURE_SECONDS = 30
_REPLAY_TTL_SECONDS = 600
_REPLAY_KEY_PREFIX = "mem:worker-auth:v1:"


class RequestAuthenticationError(RuntimeError):
    """The caller did not present one valid, fresh request signature."""


class AuthenticationUnavailableError(RuntimeError):
    """The shared replay/auth dependency could not make a safe decision."""


class ReplayStore(Protocol):
    """Atomic one-time nonce storage used by every Worker replica."""

    def claim(self, key_id: str, nonce: str, ttl_seconds: int) -> bool:
        """Return true only when this nonce was atomically claimed."""

    def ping(self) -> None:
        """Raise when the shared store is unavailable."""


class RedisReplayStore:
    """Redis ``SET NX EX`` replay store.

    The Redis key hashes the public key-id/nonce pair.  This keeps a bounded,
    fixed alphabet in Redis keys and avoids copying request metadata into
    operational diagnostics.
    """

    def __init__(self, url: str, *, client: Any | None = None):
        self._client = client or redis.Redis.from_url(
            url,
            decode_responses=False,
        )

    def claim(self, key_id: str, nonce: str, ttl_seconds: int) -> bool:
        digest = hashlib.sha256(f"{key_id}\0{nonce}".encode("ascii")).hexdigest()
        key = _REPLAY_KEY_PREFIX + digest
        try:
            claimed = self._client.set(
                key,
                b"1",
                nx=True,
                ex=ttl_seconds,
            )
        except RedisError as exc:
            raise AuthenticationUnavailableError("request authentication is unavailable") from exc
        return bool(claimed)

    def ping(self) -> None:
        try:
            if not self._client.ping():
                raise AuthenticationUnavailableError("request authentication is unavailable")
        except RedisError as exc:
            raise AuthenticationUnavailableError("request authentication is unavailable") from exc


@dataclass(frozen=True, slots=True)
class AuthenticatedRequest:
    """Bound request identity retained until response proof generation."""

    method: str
    scope: str
    key_id: str
    timestamp: str
    nonce: str
    request_body_sha256: str


class RequestAuthenticator:
    """Verify requests, atomically reject replay, and sign responses."""

    def __init__(
        self,
        *,
        mode: str,
        key_id: str | None = None,
        key: bytes | None = None,
        replay_store: ReplayStore | None = None,
        clock: Callable[[], float] = time.time,
        idealab_binding_ready: Callable[[], bool] = lambda: False,
        openai_binding_ready: Callable[[], bool] = lambda: False,
    ):
        if mode not in {"disabled", "required"}:
            raise ValueError("invalid worker auth mode")
        if mode == "required" and (
            not isinstance(key_id, str)
            or not _KEY_ID_PATTERN.fullmatch(key_id)
            or key is None
            or len(key) != _SIGNATURE_BYTES
            or replay_store is None
        ):
            raise ValueError("required worker auth is incomplete")
        self.mode = mode
        self._key_id = key_id
        self._key = key
        self._replay_store = replay_store
        self._clock = clock
        self._idealab_binding_ready = idealab_binding_ready
        self._openai_binding_ready = openai_binding_ready

    @classmethod
    def from_settings(cls, settings: Settings) -> RequestAuthenticator:
        if settings.worker_auth_mode == "disabled":
            return cls(mode="disabled")
        replay_url = settings.worker_auth_replay_url()
        key = settings.decoded_worker_auth_key()
        if replay_url is None or key is None:
            # Settings validation should make this unreachable.  Keep the
            # construction boundary independently fail-closed.
            raise ValueError("required worker auth is incomplete")
        return cls(
            mode="required",
            key_id=settings.worker_auth_key_id,
            key=key,
            replay_store=RedisReplayStore(replay_url),
            idealab_binding_ready=settings.idealab_binding_ready,
            openai_binding_ready=settings.openai_managed_binding_ready,
        )

    def startup_check(self) -> None:
        """Require the shared replay store before opening an authenticated port."""
        if self.mode == "required":
            self._require_replay_store().ping()

    def managed_readiness_check(self, provider_spec: str) -> None:
        """Verify the non-paid dependencies required by a managed request."""
        vendor = _READINESS_PROVIDER_VENDORS.get(provider_spec)
        if self.mode != "required" or vendor is None or not self.managed_binding_configured(vendor):
            raise AuthenticationUnavailableError("managed Worker readiness is unavailable")
        self._require_replay_store().ping()

    def managed_binding_configured(self, vendor: str) -> bool:
        """Return whether one exact managed provider namespace is safely bound."""
        if self.mode != "required":
            return False
        if vendor == "idealab":
            return self._idealab_binding_ready()
        if vendor == "openai":
            return self._openai_binding_ready()
        return False

    def has_auth_metadata(self, context: Any | None) -> bool:
        return bool(_auth_metadata_pairs(context))

    def authenticate(
        self,
        request: Any,
        context: Any | None,
        *,
        method: str,
        scope: str | None,
    ) -> AuthenticatedRequest | None:
        """Authenticate before callers inspect or log the request body."""
        pairs = _auth_metadata_pairs(context)
        if self.mode == "disabled":
            if pairs:
                raise RequestAuthenticationError("request authentication failed")
            return None
        if not pairs:
            raise RequestAuthenticationError("request authentication failed")

        values = _single_request_metadata(pairs)
        supplied_scope = values[AUTH_SCOPE_HEADER]
        if (
            values[AUTH_CONTRACT_HEADER] != REQUEST_CONTRACT
            or values[AUTH_KEY_ID_HEADER] != self._key_id
            or (scope is not None and supplied_scope != scope)
        ):
            raise RequestAuthenticationError("request authentication failed")
        if scope is None and (
            method != HEALTH_METHOD
            or not supplied_scope.startswith(READINESS_SCOPE_PREFIX)
            or supplied_scope[len(READINESS_SCOPE_PREFIX) :] not in _READINESS_PROVIDER_VENDORS
        ):
            raise RequestAuthenticationError("request authentication failed")

        timestamp = values[AUTH_TIMESTAMP_HEADER]
        nonce = values[AUTH_NONCE_HEADER]
        signature = values[AUTH_SIGNATURE_HEADER]
        timestamp_value = _parse_timestamp(timestamp)
        now = int(self._clock())
        if timestamp_value < now - _MAX_PAST_SECONDS or timestamp_value > now + _MAX_FUTURE_SECONDS:
            raise RequestAuthenticationError("request authentication failed")
        _decode_canonical_urlsafe(nonce, _NONCE_BYTES)
        supplied_signature = _decode_canonical_urlsafe(
            signature,
            _SIGNATURE_BYTES,
        )

        request_body_sha256 = deterministic_message_sha256(request)
        expected = request_signature(
            key=self._require_key(),
            method=method,
            scope=supplied_scope,
            key_id=values[AUTH_KEY_ID_HEADER],
            timestamp=timestamp,
            nonce=nonce,
            body_sha256=request_body_sha256,
        )
        if not hmac.compare_digest(supplied_signature, expected):
            raise RequestAuthenticationError("request authentication failed")

        # Claim only after the signature is valid.  Invalid callers cannot
        # consume replay-store memory or block a legitimate nonce.
        if not self._require_replay_store().claim(
            values[AUTH_KEY_ID_HEADER],
            nonce,
            _REPLAY_TTL_SECONDS,
        ):
            raise RequestAuthenticationError("request authentication failed")

        return AuthenticatedRequest(
            method=method,
            scope=supplied_scope,
            key_id=values[AUTH_KEY_ID_HEADER],
            timestamp=timestamp,
            nonce=nonce,
            request_body_sha256=request_body_sha256,
        )

    def set_response_trailers(
        self,
        authenticated: AuthenticatedRequest | None,
        response: Any,
        context: Any | None,
    ) -> None:
        """Attach a proof to every successful authenticated unary response."""
        if authenticated is None:
            return
        if context is None or not hasattr(context, "set_trailing_metadata"):
            raise AuthenticationUnavailableError("response authentication is unavailable")
        signature = response_signature(
            key=self._require_key(),
            method=authenticated.method,
            scope=authenticated.scope,
            key_id=authenticated.key_id,
            nonce=authenticated.nonce,
            response_body_sha256=deterministic_message_sha256(response),
        )
        context.set_trailing_metadata(
            (
                (RESPONSE_CONTRACT_HEADER, RESPONSE_CONTRACT),
                (RESPONSE_KEY_ID_HEADER, authenticated.key_id),
                (RESPONSE_NONCE_HEADER, authenticated.nonce),
                (
                    RESPONSE_SIGNATURE_HEADER,
                    _encode_urlsafe(signature),
                ),
            )
        )

    def _require_key(self) -> bytes:
        if self._key is None:
            raise AuthenticationUnavailableError("request authentication is unavailable")
        return self._key

    def _require_replay_store(self) -> ReplayStore:
        if self._replay_store is None:
            raise AuthenticationUnavailableError("request authentication is unavailable")
        return self._replay_store


def deterministic_message_sha256(message: Any) -> str:
    """Hash the deterministic protobuf representation as lowercase hex."""
    serialized = message.SerializeToString(deterministic=True)
    return hashlib.sha256(serialized).hexdigest()


def request_signature(
    *,
    key: bytes,
    method: str,
    scope: str,
    key_id: str,
    timestamp: str,
    nonce: str,
    body_sha256: str,
) -> bytes:
    canonical = (
        f"{REQUEST_SIGNING_DOMAIN}\n"
        f"{method}\n"
        f"{scope}\n"
        f"{key_id}\n"
        f"{timestamp}\n"
        f"{nonce}\n"
        f"{body_sha256}"
    ).encode()
    return hmac.new(key, canonical, hashlib.sha256).digest()


def response_signature(
    *,
    key: bytes,
    method: str,
    scope: str,
    key_id: str,
    nonce: str,
    response_body_sha256: str,
) -> bytes:
    canonical = (
        f"{RESPONSE_SIGNING_DOMAIN}\n"
        f"{method}\n"
        f"{scope}\n"
        f"{key_id}\n"
        f"{nonce}\n"
        "0\n"
        f"{response_body_sha256}"
    ).encode()
    return hmac.new(key, canonical, hashlib.sha256).digest()


def build_request_metadata(
    request: Any,
    *,
    key: bytes,
    key_id: str,
    timestamp: int,
    nonce: str,
    method: str,
    scope: str,
) -> tuple[tuple[str, str], ...]:
    """Build exact metadata for hermetic tests and cross-language fixtures."""
    timestamp_text = str(timestamp)
    body_sha256 = deterministic_message_sha256(request)
    signature = request_signature(
        key=key,
        method=method,
        scope=scope,
        key_id=key_id,
        timestamp=timestamp_text,
        nonce=nonce,
        body_sha256=body_sha256,
    )
    return (
        (AUTH_CONTRACT_HEADER, REQUEST_CONTRACT),
        (AUTH_KEY_ID_HEADER, key_id),
        (AUTH_TIMESTAMP_HEADER, timestamp_text),
        (AUTH_NONCE_HEADER, nonce),
        (AUTH_SCOPE_HEADER, scope),
        (AUTH_SIGNATURE_HEADER, _encode_urlsafe(signature)),
    )


def _auth_metadata_pairs(context: Any | None) -> list[tuple[str, str]]:
    if context is None or not hasattr(context, "invocation_metadata"):
        return []
    try:
        incoming: Iterable[Any] = context.invocation_metadata() or ()
    except Exception as exc:  # noqa: BLE001 - an unreadable context cannot authorize
        raise RequestAuthenticationError("request authentication failed") from exc

    pairs: list[tuple[str, str]] = []
    for item in incoming:
        if hasattr(item, "key") and hasattr(item, "value"):
            key, value = item.key, item.value
        else:
            try:
                key, value = item
            except (TypeError, ValueError) as exc:
                raise RequestAuthenticationError("request authentication failed") from exc
        if not isinstance(key, str):
            continue
        key = key.lower()
        if not key.startswith(_AUTH_HEADER_PREFIX):
            continue
        if not isinstance(value, str):
            raise RequestAuthenticationError("request authentication failed")
        pairs.append((key, value))
    return pairs


def _single_request_metadata(pairs: Sequence[tuple[str, str]]) -> dict[str, str]:
    values: dict[str, str] = {}
    for key, value in pairs:
        if key not in _REQUEST_HEADERS or key in values:
            raise RequestAuthenticationError("request authentication failed")
        values[key] = value
    if set(values) != _REQUEST_HEADERS:
        raise RequestAuthenticationError("request authentication failed")
    return values


def _parse_timestamp(value: str) -> int:
    if not _TIMESTAMP_PATTERN.fullmatch(value):
        raise RequestAuthenticationError("request authentication failed")
    try:
        return int(value, 10)
    except ValueError as exc:
        raise RequestAuthenticationError("request authentication failed") from exc


def _decode_canonical_urlsafe(value: str, expected_bytes: int) -> bytes:
    if not value or "=" in value:
        raise RequestAuthenticationError("request authentication failed")
    try:
        decoded = base64.urlsafe_b64decode(value + ("=" * (-len(value) % 4)))
    except (ValueError, base64.binascii.Error) as exc:
        raise RequestAuthenticationError("request authentication failed") from exc
    if len(decoded) != expected_bytes or _encode_urlsafe(decoded) != value:
        raise RequestAuthenticationError("request authentication failed")
    return decoded


def _encode_urlsafe(value: bytes) -> str:
    return base64.urlsafe_b64encode(value).decode("ascii").rstrip("=")


__all__ = [
    "AUTH_CONTRACT_HEADER",
    "AUTH_KEY_ID_HEADER",
    "AUTH_NONCE_HEADER",
    "AUTH_SCOPE_HEADER",
    "AUTH_SIGNATURE_HEADER",
    "AUTH_TIMESTAMP_HEADER",
    "AuthenticatedRequest",
    "AuthenticationUnavailableError",
    "HEALTH_METHOD",
    "PROCESS_METHOD",
    "PROCESS_SCOPE",
    "READINESS_SCOPE_PREFIX",
    "REQUEST_CONTRACT",
    "RESPONSE_CONTRACT",
    "RESPONSE_CONTRACT_HEADER",
    "RESPONSE_KEY_ID_HEADER",
    "RESPONSE_NONCE_HEADER",
    "RESPONSE_SIGNATURE_HEADER",
    "RedisReplayStore",
    "ReplayStore",
    "RequestAuthenticationError",
    "RequestAuthenticator",
    "build_request_metadata",
    "deterministic_message_sha256",
    "request_signature",
    "response_signature",
]
