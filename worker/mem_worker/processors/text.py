"""TextProcessor — handles ``text/*``, JSON, and common code MIMEs.

W1 scope:
    1. Decode bytes as UTF-8 (with utf-8-sig + latin-1 fallbacks).
    2. Chunk into ~1000-char windows with 100-char overlap (configurable).
    3. Embed each chunk via the default text embedding provider.
    4. Ask the configured indexing LLM for bounded description/tag candidates.

Provider construction stays lazy and failures are non-fatal: unavailable models
produce a partial result while embeddings and extracted content remain usable.
This is an indexing enrichment path, not Agent answer generation.
"""

from __future__ import annotations

import json
from collections.abc import Iterable

from ..config import get_settings
from ..logging import get_logger
from ..managed_usage import (
    INDETERMINATE,
    NOT_INVOKED,
    PROCESSOR_STAGES_KEY,
    SUCCEEDED,
    is_managed_provider,
)
from ..providers import (
    EmbeddingProvider,
    LLMProvider,
    Message,
    ProviderError,
    get_embedding_provider,
    get_llm_provider,
)
from .annotations import (
    TEXT_ANNOTATION_SYSTEM_PROMPT,
    description_value,
    plain_description,
    structured_annotations,
    tag_values,
)
from .base import (
    ANNOTATION_ANALYSIS_VERSION,
    PROVIDER_ERROR_MARKER,
    EmbeddingRow,
    EmbeddingSet,
    FileRef,
    ProcessResult,
    get_explicit_embedding_provider,
)

log = get_logger(__name__)


# Common "text-ish" MIMEs that aren't strictly ``text/*``.
_EXTRA_TEXT_MIMES = [
    "application/json",
    "application/xml",
    "application/yaml",
    "application/x-yaml",
    "application/javascript",
    "application/typescript",
    "application/x-sh",
    "application/x-python",
    "application/x-toml",
]


class TextProcessor:
    """Generic text/document processor."""

    name = "text"
    # Glob patterns: text/* covers text/plain, text/html, text/markdown,
    # text/x-python, etc. The exact mimes listed cover JSON-ish payloads.
    accepts = ["text/*", *_EXTRA_TEXT_MIMES]

    def __init__(
        self,
        embedder: EmbeddingProvider | None = None,
        llm: LLMProvider | None = None,
        *,
        llm_spec: str | None = None,
        embedding_spec: str | None = None,
        embedding_dimensions: int | None = None,
        embedding_enabled: bool | None = None,
        llm_enabled: bool | None = None,
        analysis_version: str = ANNOTATION_ANALYSIS_VERSION,
    ):
        self._embedder = embedder
        self._llm = llm
        self._llm_spec = llm_spec
        self._embedding_spec = embedding_spec
        self._embedding_dimensions = embedding_dimensions
        # ``None`` retains legacy defaults; booleans are the explicit profile
        # contract and must never fall through to MEM_DEFAULT_*.
        self._embedding_enabled = embedding_enabled
        self._llm_enabled = llm_enabled
        self._analysis_version = analysis_version

    # ---- helpers ----

    def _resolve_embedder(self) -> EmbeddingProvider | None:
        if self._embedding_enabled is False:
            return None
        if self._embedder is None:
            spec = self._embedding_spec
            if spec is None:
                if self._embedding_enabled is True:
                    raise ProviderError("profile embedding provider is missing")
                # Preserve the legacy default construction path exactly when
                # no profile capability state was supplied.
                self._embedder = get_embedding_provider(get_settings().default_embedding)
            else:
                self._embedder = get_explicit_embedding_provider(
                    spec,
                    self._embedding_dimensions,
                )
        return self._embedder

    def _resolve_llm(self) -> LLMProvider | None:
        if self._llm_enabled is False:
            return None
        if self._llm is not None:
            return self._llm
        spec = self._llm_spec
        if spec is None:
            if self._llm_enabled is True:
                raise ProviderError("profile llm provider is missing")
            spec = get_settings().default_llm
        spec = spec.strip()
        if not spec:
            return None
        self._llm = get_llm_provider(spec)
        return self._llm

    # ---- main entrypoint ----

    def initial_managed_usage_stages(self) -> dict[str, str]:
        """Return the closed pre-invocation receipt for delegated processors."""
        managed_stages: dict[str, str] = {}
        if self._embedding_enabled is True and is_managed_provider(self._embedding_spec):
            managed_stages["embedding"] = NOT_INVOKED
        if self._llm_enabled is True and is_managed_provider(self._llm_spec):
            managed_stages["llm"] = NOT_INVOKED
        return managed_stages

    def process(self, file: FileRef) -> ProcessResult:
        result = ProcessResult(processor=self.name)
        managed_stages = self.initial_managed_usage_stages()
        if managed_stages:
            result.metadata[PROCESSOR_STAGES_KEY] = managed_stages

        text = _decode_text(file.data)
        if not text.strip():
            result.metadata.update({"decode_empty": True, "byte_length": len(file.data)})
            return result

        settings = get_settings()
        chunks = list(
            _chunk_text(
                text,
                size=settings.text_chunk_size,
                overlap=settings.text_chunk_overlap,
            )
        )
        result.metadata.update(
            {
                "char_length": len(text),
                "chunk_count": len(chunks),
                "chunk_size": settings.text_chunk_size,
                "chunk_overlap": settings.text_chunk_overlap,
            }
        )

        # 1. Embeddings.
        if self._embedding_enabled is not False:
            try:
                embedder = self._resolve_embedder()
                if embedder is not None:
                    if "embedding" in managed_stages:
                        managed_stages["embedding"] = INDETERMINATE
                    vectors = embedder.embed_text(chunks)
                    if vectors:
                        if self._embedding_dimensions is not None and any(
                            len(vector) != self._embedding_dimensions for vector in vectors
                        ):
                            raise ProviderError(
                                "profile embedding response did not match configured dimensions"
                            )
                        dim = len(vectors[0])
                        result.embeddings["text"] = EmbeddingSet(
                            provider=embedder.name,
                            dim=dim,
                            rows=[
                                EmbeddingRow(values=v, index=i, chunk_text=chunks[i])
                                for i, v in enumerate(vectors)
                            ],
                        )
                        if "embedding" in managed_stages:
                            managed_stages["embedding"] = SUCCEEDED
            except (ProviderError, NotImplementedError):
                log.warning(
                    "text.embed_failed",
                    file_id=file.file_id,
                    error=PROVIDER_ERROR_MARKER,
                )
                result.metadata["embed_error"] = PROVIDER_ERROR_MARKER
            except Exception:  # noqa: BLE001 — provider bugs must stay partial and redacted
                log.error(
                    "text.embed_unexpected",
                    file_id=file.file_id,
                    error=PROVIDER_ERROR_MARKER,
                )
                result.metadata["embed_error"] = PROVIDER_ERROR_MARKER

        # 2. Optional model annotation. Provider resolution happens here so
        # importing the registry and serving HealthCheck never require a model.
        if len(text) >= 200 and self._llm_enabled is not False:
            try:
                llm = self._resolve_llm()
                if llm is None:
                    return result
                if "llm" in managed_stages:
                    managed_stages["llm"] = INDETERMINATE
                model_output = llm.complete(
                    [
                        Message(
                            role="system",
                            content=TEXT_ANNOTATION_SYSTEM_PROMPT,
                        ),
                        Message(
                            role="user",
                            content=(
                                "UNTRUSTED_DOCUMENT_JSON_STRING:\n"
                                + json.dumps(text[:8000], ensure_ascii=False)
                            ),
                        ),
                    ]
                )
                if "llm" in managed_stages:
                    managed_stages["llm"] = SUCCEEDED
                suggestions = structured_annotations(
                    model_output,
                    provider=getattr(llm, "name", ""),
                    analysis_version=self._analysis_version,
                )
                if suggestions is not None:
                    result.annotations = suggestions
                    result.annotations_complete = True
                    result.summary = description_value(suggestions)
                    result.tags = tag_values(suggestions)
                else:
                    fallback = plain_description(
                        model_output,
                        provider=getattr(llm, "name", ""),
                        analysis_version=self._analysis_version,
                    )
                    if fallback is not None:
                        # Compatibility for explicit hooks and their existing
                        # plain-text fake providers.
                        result.annotations = [fallback]
                        result.annotations_complete = True
                        result.summary = fallback.value
                    else:
                        result.metadata["annotation_parse_error"] = (
                            "invalid structured model output"
                        )
            except (ProviderError, NotImplementedError):
                log.warning(
                    "text.summary_failed",
                    file_id=file.file_id,
                    error=PROVIDER_ERROR_MARKER,
                )
                result.metadata["summary_error"] = PROVIDER_ERROR_MARKER
            except Exception:  # noqa: BLE001 — model failure must stay partial and redacted
                log.error(
                    "text.summary_unexpected",
                    file_id=file.file_id,
                    error=PROVIDER_ERROR_MARKER,
                )
                result.metadata["summary_error"] = PROVIDER_ERROR_MARKER

        return result


# ---------------------------------------------------------------------------
# Decoding + chunking helpers (pure functions, easy to test)
# ---------------------------------------------------------------------------


def _decode_text(data: bytes) -> str:
    """Best-effort decode of arbitrary bytes to ``str``.

    Tries UTF-8 (with BOM), then latin-1 (which never fails). Replaces
    invalid sequences with U+FFFD rather than raising.
    """
    for enc in ("utf-8-sig", "utf-8"):
        try:
            return data.decode(enc)
        except UnicodeDecodeError:
            continue
    return data.decode("latin-1", errors="replace")


def _chunk_text(text: str, *, size: int, overlap: int) -> Iterable[str]:
    """Yield overlapping fixed-size windows of ``text``.

    Last chunk may be shorter than ``size``. ``overlap`` must be < ``size``.
    """
    if size <= 0:
        raise ValueError("size must be positive")
    if overlap < 0 or overlap >= size:
        raise ValueError("overlap must satisfy 0 <= overlap < size")
    step = size - overlap
    n = len(text)
    if n == 0:
        return
    i = 0
    while i < n:
        yield text[i : i + size]
        if i + size >= n:
            return
        i += step
