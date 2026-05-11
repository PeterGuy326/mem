"""Processor Protocol + shared value objects.

Mirrors SPEC §9.1:

    class Processor(Protocol):
        name: str
        accepts: list[str]   # mime patterns, e.g. ["image/*"]

        def process(self, file: FileRef) -> ProcessResult: ...

We use plain dataclasses for the result so they are trivially convertible
to/from the protobuf ProcessResponse in :mod:`mem_worker.server`.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Optional, Protocol, runtime_checkable


# ---------------------------------------------------------------------------
# Inputs
# ---------------------------------------------------------------------------

@dataclass(slots=True)
class FileRef:
    """A single file submitted for processing.

    ``data`` is populated lazily by the server (after fetching from storage),
    so processors never have to touch boto3 directly.
    """

    file_id: str
    storage_uri: str
    mime: str
    sha256: str
    user_id: str
    name: str = ""
    data: bytes = b""                  # populated by server before dispatch
    options: dict[str, Any] = field(default_factory=dict)


# ---------------------------------------------------------------------------
# Outputs
# ---------------------------------------------------------------------------

@dataclass(slots=True)
class Entity:
    """Extracted entity (person / place / org / event / …)."""

    type: str
    name: str = ""
    metadata: dict[str, Any] = field(default_factory=dict)
    confidence: float = 1.0


@dataclass(slots=True)
class EmbeddingRow:
    """One row of a (possibly multi-vector) embedding."""

    values: list[float]
    index: int = -1                    # chunk index for text; -1 if N/A
    chunk_text: str = ""                # for text only
    metadata: dict[str, Any] = field(default_factory=dict)


@dataclass(slots=True)
class EmbeddingSet:
    """A named bundle of vectors (e.g. all text chunks of one file)."""

    provider: str                      # e.g. "ollama:nomic-embed-text"
    dim: int
    rows: list[EmbeddingRow] = field(default_factory=list)


@dataclass(slots=True)
class ProcessResult:
    """The full processor output (SPEC §9.1)."""

    summary: Optional[str] = None
    caption: Optional[str] = None
    tags: list[str] = field(default_factory=list)
    entities: list[Entity] = field(default_factory=list)
    # Keys are well-known kinds: "text", "visual", "face".
    embeddings: dict[str, EmbeddingSet] = field(default_factory=dict)
    metadata: dict[str, Any] = field(default_factory=dict)
    processor: str = ""                # filled in by the dispatching code


# ---------------------------------------------------------------------------
# Protocol + error
# ---------------------------------------------------------------------------

class ProcessorError(RuntimeError):
    """Raised by Processors on unrecoverable failure for a single file."""


@runtime_checkable
class Processor(Protocol):
    """Per-mime processing interface."""

    name: str
    accepts: list[str]                 # mime patterns, e.g. ["image/*"]

    def process(self, file: FileRef) -> ProcessResult: ...
