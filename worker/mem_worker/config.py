"""Environment-driven configuration for the mem AI Worker.

All settings are loaded from environment variables (with the ``MEM_`` prefix
where applicable). Keep this file lean — the goal is "12-factor": no file
parsing, no profile switching, no implicit defaults that depend on PWD.

Provider specs use the form ``"<vendor>:<model>"``, e.g. ``"ollama:nomic-embed-text"``,
``"openai:gpt-4o-mini"``, ``"anthropic:claude-opus-4-7"`` (see SPEC §F8.5).
"""

from __future__ import annotations

from functools import lru_cache
from typing import Optional

from pydantic import Field
from pydantic_settings import BaseSettings, SettingsConfigDict


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

    # ---- Data layer (W2 — not used in W1) ----
    db_url: Optional[str] = Field(default=None, alias="MEM_DB_URL")

    # ---- Object storage (S3-compatible: MinIO / R2 / OSS / AWS) ----
    s3_endpoint: Optional[str] = Field(default=None, alias="MEM_S3_ENDPOINT")
    s3_region: str = Field(default="us-east-1", alias="MEM_S3_REGION")
    s3_bucket: Optional[str] = Field(default=None, alias="MEM_S3_BUCKET")
    s3_access_key: Optional[str] = Field(default=None, alias="MEM_S3_ACCESS_KEY")
    s3_secret_key: Optional[str] = Field(default=None, alias="MEM_S3_SECRET_KEY")
    s3_use_ssl: bool = Field(default=False, alias="MEM_S3_USE_SSL")

    # ---- Provider: Ollama (local default) ----
    ollama_base_url: str = Field(
        default="http://localhost:11434", alias="OLLAMA_BASE_URL"
    )
    ollama_timeout: float = Field(default=120.0, alias="OLLAMA_TIMEOUT")

    # ---- Provider: OpenAI (stub in W1) ----
    openai_api_key: Optional[str] = Field(default=None, alias="OPENAI_API_KEY")
    openai_base_url: Optional[str] = Field(default=None, alias="OPENAI_BASE_URL")

    # ---- Provider: Anthropic (stub in W1) ----
    anthropic_api_key: Optional[str] = Field(default=None, alias="ANTHROPIC_API_KEY")

    # ---- Default provider specs (SPEC §9.4) ----
    default_embedding: str = Field(
        default="ollama:nomic-embed-text", alias="MEM_DEFAULT_EMBEDDING"
    )
    default_visual_embedding: str = Field(
        default="clip:ViT-B-32",  # 512-d shared image+text space (Phase 2)
        alias="MEM_DEFAULT_VISUAL_EMBEDDING",
    )
    default_vlm: str = Field(default="ollama:minicpm-v", alias="MEM_DEFAULT_VLM")
    default_asr: str = Field(default="faster-whisper:tiny", alias="MEM_DEFAULT_ASR")

    # ---- Pipeline knobs ----
    text_chunk_size: int = Field(default=1000, alias="MEM_TEXT_CHUNK_SIZE")
    text_chunk_overlap: int = Field(default=100, alias="MEM_TEXT_CHUNK_OVERLAP")

    # ---- Logging ----
    log_level: str = Field(default="INFO", alias="MEM_LOG_LEVEL")
    log_json: bool = Field(default=False, alias="MEM_LOG_JSON")


@lru_cache(maxsize=1)
def get_settings() -> Settings:
    """Return a process-singleton Settings instance.

    The lru_cache makes this safe to call from anywhere without re-parsing
    env on every call, while still being trivially overridable in tests via
    ``get_settings.cache_clear()``.
    """
    return Settings()
