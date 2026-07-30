"""Environment-driven configuration for the mem AI Worker.

All settings are loaded from environment variables (with the ``MEM_`` prefix
where applicable). Keep this file lean — the goal is "12-factor": no file
parsing, no profile switching, no implicit defaults that depend on PWD.

Provider specs use the form ``"<vendor>:<model>"``, e.g. ``"ollama:nomic-embed-text"``,
``"openai:gpt-4o-mini"``, ``"anthropic:claude-opus-4-7"`` (see SPEC §F8.5).
"""

from __future__ import annotations

import base64
import binascii
from functools import lru_cache
from ipaddress import ip_address
from typing import Literal
from urllib.parse import urlsplit

from pydantic import Field, SecretStr, field_validator, model_validator
from pydantic_settings import BaseSettings, SettingsConfigDict

_WORKER_AUTH_KEY_BYTES = 32
_WORKER_AUTH_KEY_ID_MAX_BYTES = 64
_AUTH_KEY_ID_CHARS = frozenset("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-")
_LEGACY_MANAGED_VENDOR = "idealab"
_LOCAL_PROVIDER_VENDORS = frozenset({"ollama", "clip", "faster-whisper", "whisper"})


class Settings(BaseSettings):
    """Worker-process settings.

    Notes:
    - ``MEM_DB_URL`` is read but NOT used in W1 (backend owns the schema).
    - ``MEM_S3_*`` are needed to fetch file bytes from object storage.
    - ``OLLAMA_BASE_URL`` defaults to localhost; if Ollama is unreachable we
      still want the gRPC server to come up (lazy init).
    """

    model_config = SettingsConfigDict(
        env_file=".env",
        env_file_encoding="utf-8",
        case_sensitive=False,
        extra="ignore",
    )

    # ---- gRPC server ----
    grpc_host: str = Field(default="127.0.0.1", alias="MEM_WORKER_GRPC_HOST")
    grpc_port: int = Field(default=50051, alias="MEM_WORKER_GRPC_PORT")
    grpc_max_workers: int = Field(default=8, alias="MEM_WORKER_GRPC_MAX_WORKERS")
    deployment_mode: Literal["private", "saas"] = Field(
        default="private",
        alias="MEM_DEPLOYMENT_MODE",
    )
    worker_auth_mode: Literal["disabled", "required"] = Field(
        default="disabled",
        alias="MEM_WORKER_AUTH_MODE",
    )
    worker_auth_key_id: str | None = Field(
        default=None,
        alias="MEM_WORKER_AUTH_KEY_ID",
    )
    worker_auth_key_b64: SecretStr | None = Field(
        default=None,
        alias="MEM_WORKER_AUTH_KEY_B64",
    )
    worker_auth_replay_redis_url: SecretStr | None = Field(
        default=None,
        alias="MEM_WORKER_AUTH_REPLAY_REDIS_URL",
    )

    # ---- Data layer (W2 — not used in W1) ----
    db_url: str | None = Field(default=None, alias="MEM_DB_URL")

    # ---- Object storage (S3-compatible: MinIO / R2 / OSS / AWS) ----
    s3_endpoint: str | None = Field(default=None, alias="MEM_S3_ENDPOINT")
    s3_region: str = Field(default="us-east-1", alias="MEM_S3_REGION")
    s3_bucket: str | None = Field(default=None, alias="MEM_S3_BUCKET")
    s3_access_key: str | None = Field(default=None, alias="MEM_S3_ACCESS_KEY")
    s3_secret_key: str | None = Field(default=None, alias="MEM_S3_SECRET_KEY")
    s3_use_ssl: bool = Field(default=False, alias="MEM_S3_USE_SSL")

    # ---- Provider: Ollama (local default) ----
    ollama_base_url: str = Field(default="http://localhost:11434", alias="OLLAMA_BASE_URL")
    ollama_timeout: float = Field(default=120.0, alias="OLLAMA_TIMEOUT")

    # ---- Provider: OpenAI (stub in W1) ----
    openai_api_key: str | None = Field(default=None, alias="OPENAI_API_KEY")
    openai_base_url: str | None = Field(default=None, alias="OPENAI_BASE_URL")
    # Distinguish a user's private/BYOM OpenAI-compatible configuration from
    # the platform credential retained solely for the immutable managed V1
    # profile. Only the explicit managed binding may satisfy SaaS readiness.
    openai_managed_binding: bool = Field(
        default=False,
        alias="MEM_OPENAI_MANAGED_BINDING",
    )
    # Optional because not every OpenAI-compatible embedding endpoint supports
    # the OpenAI ``dimensions`` request field. When set, the adapter requests
    # and verifies exactly this many values per vector.
    openai_embedding_dimensions: int | None = Field(
        default=None,
        alias="OPENAI_EMBEDDING_DIMENSIONS",
    )

    # ---- Provider: Idealab (dedicated managed binding) ----
    # Keep these credentials and endpoint separate from generic OpenAI-compatible
    # configuration. A managed Idealab profile must never inherit OPENAI_* or
    # fall through to api.openai.com when its deployment binding is incomplete.
    idealab_api_key: SecretStr | None = Field(default=None, alias="IDEALAB_API_KEY")
    idealab_base_url: str | None = Field(default=None, alias="IDEALAB_BASE_URL")

    # ---- Provider: Anthropic (stub in W1) ----
    anthropic_api_key: str | None = Field(default=None, alias="ANTHROPIC_API_KEY")

    # ---- Default provider specs (SPEC §9.4) ----
    default_embedding: str = Field(default="ollama:nomic-embed-text", alias="MEM_DEFAULT_EMBEDDING")
    default_visual_embedding: str = Field(
        default="clip:ViT-B-32",  # 512-d shared image+text space (Phase 2)
        alias="MEM_DEFAULT_VISUAL_EMBEDDING",
    )
    default_llm: str = Field(default="ollama:qwen2.5:7b", alias="MEM_DEFAULT_LLM")
    default_vlm: str = Field(default="ollama:minicpm-v", alias="MEM_DEFAULT_VLM")
    default_asr: str = Field(default="faster-whisper:tiny", alias="MEM_DEFAULT_ASR")

    # ---- Pipeline knobs ----
    text_chunk_size: int = Field(default=1000, alias="MEM_TEXT_CHUNK_SIZE")
    text_chunk_overlap: int = Field(default=100, alias="MEM_TEXT_CHUNK_OVERLAP")

    # ---- Logging ----
    log_level: str = Field(default="INFO", alias="MEM_LOG_LEVEL")
    log_json: bool = Field(default=False, alias="MEM_LOG_JSON")

    @field_validator("openai_embedding_dimensions", mode="before")
    @classmethod
    def validate_openai_embedding_dimensions(cls, value: object) -> int | None:
        """Accept an optional, positive, decimal output dimension only.

        This setting is forwarded to a managed endpoint, so reject coercions
        such as booleans, floats, and signed strings rather than silently
        changing the requested vector space.
        """
        if value is None:
            return None
        if isinstance(value, bool):
            raise ValueError("OPENAI_EMBEDDING_DIMENSIONS must be a positive integer")
        if isinstance(value, int):
            dimensions = value
        elif isinstance(value, str):
            normalized = value.strip()
            if not normalized or not normalized.isascii() or not normalized.isdecimal():
                raise ValueError("OPENAI_EMBEDDING_DIMENSIONS must be a positive integer")
            dimensions = int(normalized)
        else:
            raise ValueError("OPENAI_EMBEDDING_DIMENSIONS must be a positive integer")
        if dimensions <= 0:
            raise ValueError("OPENAI_EMBEDDING_DIMENSIONS must be a positive integer")
        return dimensions

    @model_validator(mode="after")
    def validate_worker_auth_and_managed_binding(self) -> Settings:
        """Fail closed on incomplete auth or managed-provider configuration.

        ``idealab:*`` is the platform-managed namespace. Mounting its
        credential on an unauthenticated Worker would let any network caller
        spend that credential, so the binding and request-auth boundary are
        validated together at process startup.
        """
        auth_fields_present = any(
            (
                self.worker_auth_key_id,
                self.worker_auth_key_b64,
                self.worker_auth_replay_redis_url,
            )
        )
        if self.deployment_mode == "saas" and self.worker_auth_mode != "required":
            raise ValueError("MEM_DEPLOYMENT_MODE=saas requires MEM_WORKER_AUTH_MODE=required")
        if self.worker_auth_mode == "disabled":
            if auth_fields_present:
                raise ValueError("worker auth settings require MEM_WORKER_AUTH_MODE=required")
        else:
            if not _valid_auth_key_id(self.worker_auth_key_id):
                raise ValueError("MEM_WORKER_AUTH_KEY_ID is required and invalid")
            if self.worker_auth_key_b64 is None:
                raise ValueError("MEM_WORKER_AUTH_KEY_B64 is required")
            _decode_worker_auth_key(self.worker_auth_key_b64)
            if self.worker_auth_replay_redis_url is None:
                raise ValueError("MEM_WORKER_AUTH_REPLAY_REDIS_URL is required")
            _validate_redis_url(self.worker_auth_replay_redis_url.get_secret_value())

        api_key_present = bool(
            self.idealab_api_key and self.idealab_api_key.get_secret_value().strip()
        )
        base_url_present = bool((self.idealab_base_url or "").strip())
        if api_key_present != base_url_present:
            raise ValueError("IDEALAB_API_KEY and IDEALAB_BASE_URL must be configured together")
        if api_key_present:
            if self.worker_auth_mode != "required":
                raise ValueError("Idealab binding requires MEM_WORKER_AUTH_MODE=required")
            _validate_idealab_base_url(self.idealab_base_url or "")

        if self.worker_auth_mode == "required":
            openai_key_present = bool((self.openai_api_key or "").strip())
            openai_url_present = bool((self.openai_base_url or "").strip())
            if openai_key_present != openai_url_present:
                raise ValueError(
                    "OPENAI_API_KEY and OPENAI_BASE_URL must be configured together "
                    "for an authenticated Worker"
                )
            if openai_key_present:
                _validate_managed_openai_base_url(self.openai_base_url or "")
            if (
                self.deployment_mode == "saas"
                and openai_key_present
                and not self.openai_managed_binding
            ):
                raise ValueError("SaaS OPENAI_API_KEY requires MEM_OPENAI_MANAGED_BINDING=true")
        if self.openai_managed_binding:
            if self.worker_auth_mode != "required":
                raise ValueError(
                    "MEM_OPENAI_MANAGED_BINDING requires MEM_WORKER_AUTH_MODE=required"
                )
            if not ((self.openai_api_key or "").strip() and (self.openai_base_url or "").strip()):
                raise ValueError(
                    "MEM_OPENAI_MANAGED_BINDING requires OPENAI_API_KEY and OPENAI_BASE_URL"
                )

        if self.deployment_mode == "saas" and not is_loopback_ollama_url(self.ollama_base_url):
            raise ValueError("SaaS OLLAMA_BASE_URL must be an absolute loopback HTTP(S) URL")

        for spec in (
            self.default_embedding,
            self.default_visual_embedding,
            self.default_llm,
            self.default_vlm,
            self.default_asr,
        ):
            if spec is None or (isinstance(spec, str) and not spec.strip()):
                continue
            vendor = _provider_vendor(spec)
            if vendor == _LEGACY_MANAGED_VENDOR:
                raise ValueError("idealab providers require an authenticated AI profile")
            if self.openai_managed_binding and vendor == "openai":
                raise ValueError(
                    "openai providers require an authenticated AI profile when "
                    "MEM_OPENAI_MANAGED_BINDING=true"
                )
            if self.deployment_mode == "saas":
                if not _valid_provider_spec(spec):
                    raise ValueError(
                        "MEM_DEPLOYMENT_MODE=saas requires every non-empty "
                        "MEM_DEFAULT_* to use '<vendor>:<model>'"
                    )
                if vendor not in _LOCAL_PROVIDER_VENDORS:
                    raise ValueError(
                        "MEM_DEPLOYMENT_MODE=saas requires every MEM_DEFAULT_* provider "
                        "to use a local runtime"
                    )
        return self

    def decoded_worker_auth_key(self) -> bytes | None:
        """Return the validated raw HMAC key without exposing it in repr/logs."""
        if self.worker_auth_key_b64 is None:
            return None
        return _decode_worker_auth_key(self.worker_auth_key_b64)

    def worker_auth_replay_url(self) -> str | None:
        """Return the replay-store URL only to the auth adapter."""
        if self.worker_auth_replay_redis_url is None:
            return None
        return self.worker_auth_replay_redis_url.get_secret_value()

    def idealab_key(self) -> str | None:
        """Return the managed provider key only to the dedicated adapter."""
        if self.idealab_api_key is None:
            return None
        return self.idealab_api_key.get_secret_value()

    def idealab_binding_ready(self) -> bool:
        """Report static binding readiness without making a paid API call."""
        return bool(
            self.idealab_key()
            and self.idealab_key().strip()
            and self.idealab_base_url
            and self.idealab_base_url.strip()
        )

    def openai_managed_binding_ready(self) -> bool:
        """Report the exact published V1 profile's safe generic binding."""
        return bool(
            self.worker_auth_mode == "required"
            and self.openai_managed_binding
            and self.openai_api_key
            and self.openai_api_key.strip()
            and self.openai_base_url
            and self.openai_base_url.strip()
        )

    def managed_binding_ready(self) -> bool:
        """Return true when at least one published managed binding is usable."""
        return self.idealab_binding_ready() or self.openai_managed_binding_ready()


def _decode_worker_auth_key(value: SecretStr) -> bytes:
    encoded = value.get_secret_value()
    try:
        decoded = base64.b64decode(encoded, validate=True)
    except (ValueError, binascii.Error) as exc:
        raise ValueError("MEM_WORKER_AUTH_KEY_B64 must be valid standard base64") from exc
    if len(decoded) != _WORKER_AUTH_KEY_BYTES:
        raise ValueError(f"MEM_WORKER_AUTH_KEY_B64 must decode to {_WORKER_AUTH_KEY_BYTES} bytes")
    return decoded


def _valid_auth_key_id(value: str | None) -> bool:
    return bool(
        value
        and len(value.encode("utf-8")) <= _WORKER_AUTH_KEY_ID_MAX_BYTES
        and value[0] in "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
        and all(char in _AUTH_KEY_ID_CHARS for char in value)
    )


def _validate_redis_url(value: str) -> None:
    parsed = urlsplit(value)
    if (
        parsed.scheme.lower() not in {"redis", "rediss"}
        or not parsed.netloc
        or not parsed.hostname
        or parsed.fragment
    ):
        raise ValueError("MEM_WORKER_AUTH_REPLAY_REDIS_URL must be an absolute redis URL")


def _validate_idealab_base_url(value: str) -> None:
    parsed = urlsplit(value.strip())
    if (
        parsed.scheme.lower() != "https"
        or not parsed.netloc
        or not parsed.hostname
        or parsed.username is not None
        or parsed.password is not None
        or parsed.query
        or parsed.fragment
    ):
        raise ValueError("IDEALAB_BASE_URL must be an absolute HTTPS URL")


def _validate_managed_openai_base_url(value: str) -> None:
    parsed = urlsplit(value.strip())
    if (
        parsed.scheme.lower() != "https"
        or not parsed.netloc
        or not parsed.hostname
        or parsed.username is not None
        or parsed.password is not None
        or parsed.query
        or parsed.fragment
    ):
        raise ValueError(
            "OPENAI_BASE_URL must be an absolute HTTPS URL for an authenticated Worker"
        )


def is_loopback_ollama_url(value: str) -> bool:
    """Return whether an Ollama base URL is a strict local HTTP(S) endpoint."""
    if not isinstance(value, str):
        return False
    parsed = urlsplit(value.strip())
    if (
        parsed.scheme.lower() not in {"http", "https"}
        or not parsed.netloc
        or not parsed.hostname
        or parsed.username is not None
        or parsed.password is not None
        or parsed.query
        or parsed.fragment
    ):
        return False
    try:
        # Accessing port also rejects malformed or out-of-range explicit ports.
        _ = parsed.port
    except ValueError:
        return False
    if parsed.hostname.lower() == "localhost":
        return True
    try:
        return ip_address(parsed.hostname).is_loopback
    except ValueError:
        return False


def _valid_provider_spec(spec: str) -> bool:
    vendor, separator, model = spec.strip().partition(":")
    return bool(separator and vendor.strip() and model.strip())


def _provider_vendor(spec: str | None) -> str:
    if not isinstance(spec, str):
        return ""
    return spec.partition(":")[0].strip().lower()


@lru_cache(maxsize=1)
def get_settings() -> Settings:
    """Return a process-singleton Settings instance.

    The lru_cache makes this safe to call from anywhere without re-parsing
    env on every call, while still being trivially overridable in tests via
    ``get_settings.cache_clear()``.
    """
    return Settings()
