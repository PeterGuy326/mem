"""TextProcessor — handles ``text/*``, JSON, and common code MIMEs.

W1 scope:
    1. Decode bytes as UTF-8 (with utf-8-sig + latin-1 fallbacks).
    2. Chunk into ~1000-char windows with 100-char overlap (configurable).
    3. Embed each chunk via the default text embedding provider.
    4. Optionally summarize the whole document via the default LLM.

Failures are non-fatal: if the LLM is unreachable we still return embeddings;
if embedding fails we still return the summary. The pipeline-level status
in the gRPC response will reflect this.
"""

from __future__ import annotations

from typing import Iterable, Optional

from ..config import get_settings
from ..logging import get_logger
from ..providers import (
    EmbeddingProvider,
    LLMProvider,
    Message,
    ProviderError,
    get_embedding_provider,
    get_llm_provider,
)
from .base import EmbeddingRow, EmbeddingSet, FileRef, ProcessResult

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
        embedder: Optional[EmbeddingProvider] = None,
        llm: Optional[LLMProvider] = None,
    ):
        self._embedder = embedder
        self._llm = llm

    # ---- helpers ----

    def _resolve_embedder(self) -> EmbeddingProvider:
        if self._embedder is None:
            self._embedder = get_embedding_provider(get_settings().default_embedding)
        return self._embedder

    def _resolve_llm(self) -> LLMProvider:
        if self._llm is None:
            self._llm = get_llm_provider(get_settings().default_llm)
        return self._llm

    # ---- main entrypoint ----

    def process(self, file: FileRef) -> ProcessResult:
        result = ProcessResult(processor=self.name)
        text = _decode_text(file.data)
        if not text.strip():
            result.metadata = {"decode_empty": True, "byte_length": len(file.data)}
            return result

        settings = get_settings()
        chunks = list(
            _chunk_text(
                text,
                size=settings.text_chunk_size,
                overlap=settings.text_chunk_overlap,
            )
        )
        result.metadata = {
            "char_length": len(text),
            "chunk_count": len(chunks),
            "chunk_size": settings.text_chunk_size,
            "chunk_overlap": settings.text_chunk_overlap,
        }

        # 1. Embeddings.
        try:
            embedder = self._resolve_embedder()
            vectors = embedder.embed_text(chunks)
            if vectors:
                dim = len(vectors[0])
                result.embeddings["text"] = EmbeddingSet(
                    provider=embedder.name,
                    dim=dim,
                    rows=[
                        EmbeddingRow(values=v, index=i, chunk_text=chunks[i])
                        for i, v in enumerate(vectors)
                    ],
                )
        except (ProviderError, NotImplementedError) as exc:
            log.warning("text.embed_failed", file_id=file.file_id, error=str(exc))
            result.metadata["embed_error"] = str(exc)

        # 2. Summary (only for non-trivially-short documents).
        if len(text) >= 200:
            try:
                llm = self._resolve_llm()
                summary = llm.complete(
                    [
                        Message(
                            role="system",
                            content=(
                                "You are a careful summarizer. Return a single "
                                "paragraph (<=3 sentences) summarizing the user "
                                "message. Do not add commentary."
                            ),
                        ),
                        Message(role="user", content=text[:8000]),
                    ]
                )
                result.summary = summary.strip() or None
            except (ProviderError, NotImplementedError) as exc:
                log.warning("text.summary_failed", file_id=file.file_id, error=str(exc))
                result.metadata["summary_error"] = str(exc)

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
