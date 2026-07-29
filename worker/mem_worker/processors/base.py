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

import math
import unicodedata
from dataclasses import dataclass, field
from typing import Any, Literal, Protocol, runtime_checkable

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
    data: bytes = b""  # populated by server before dispatch
    options: dict[str, Any] = field(default_factory=dict)


# ---------------------------------------------------------------------------
# Outputs
# ---------------------------------------------------------------------------

ANNOTATION_ANALYSIS_VERSION = "file-enrichment-v1"
MAX_ANNOTATION_DESCRIPTION_LENGTH = 2000
MAX_ANNOTATION_TAG_LENGTH = 64
MAX_ANNOTATION_TAGS = 20
MAX_ANNOTATION_PROVIDER_LENGTH = 255
MAX_ANNOTATION_PROCESSOR_LENGTH = 64
PROVIDER_ERROR_MARKER = "provider_unavailable"

# Unicode 15 Cf ∪ Default_Ignorable_Code_Point. Python 3.11 exposes Unicode
# 14 data, so explicit ranges keep the last-line dataclass guard aligned with
# the structured parser, Go, and PostgreSQL.
_NON_DISPLAY_RANGES = (
    (0x00AD, 0x00AD),
    (0x034F, 0x034F),
    (0x0600, 0x0605),
    (0x061C, 0x061C),
    (0x06DD, 0x06DD),
    (0x070F, 0x070F),
    (0x0890, 0x0891),
    (0x08E2, 0x08E2),
    (0x115F, 0x1160),
    (0x17B4, 0x17B5),
    (0x180B, 0x180F),
    (0x200B, 0x200F),
    (0x202A, 0x202E),
    (0x2060, 0x206F),
    (0x3164, 0x3164),
    (0xFE00, 0xFE0F),
    (0xFEFF, 0xFEFF),
    (0xFFA0, 0xFFA0),
    (0xFFF0, 0xFFFB),
    (0x110BD, 0x110BD),
    (0x110CD, 0x110CD),
    (0x13430, 0x1343F),
    (0x1BCA0, 0x1BCA3),
    (0x1D173, 0x1D17A),
    (0xE0000, 0xE0FFF),
)


@dataclass(frozen=True, slots=True)
class AnnotationSuggestion:
    """A bounded model suggestion awaiting a human decision.

    The Worker deliberately emits suggestions rather than confirmed facts.
    Persistence assigns review state and a stable key; this object carries
    only the model output and its provenance.
    """

    kind: Literal["description", "tag"]
    value: str
    confidence: float
    provider: str = ""
    analysis_version: str = ANNOTATION_ANALYSIS_VERSION
    source: Literal["model"] = "model"
    # The final processor is filled while serializing the ProcessResult. This
    # avoids labelling PDF/audio annotations as "text" merely because those
    # processors reuse TextProcessor internally.
    processor: str = ""

    def __post_init__(self) -> None:
        if self.kind not in ("description", "tag"):
            raise ValueError("annotation kind must be description or tag")
        if self.source != "model":
            raise ValueError("annotation source must be model")
        if isinstance(self.confidence, bool) or not isinstance(self.confidence, (int, float)):
            raise ValueError("annotation confidence must be numeric")
        if not math.isfinite(self.confidence) or not 0 <= self.confidence <= 1:
            raise ValueError("annotation confidence must be between 0 and 1")

        value_limit = (
            MAX_ANNOTATION_TAG_LENGTH if self.kind == "tag" else MAX_ANNOTATION_DESCRIPTION_LENGTH
        )
        _validate_annotation_text("value", self.value, value_limit, allow_empty=False)
        _validate_annotation_text(
            "provider",
            self.provider,
            MAX_ANNOTATION_PROVIDER_LENGTH,
            allow_empty=True,
        )
        _validate_annotation_text(
            "analysis_version",
            self.analysis_version,
            64,
            allow_empty=False,
        )
        _validate_annotation_text(
            "processor",
            self.processor,
            MAX_ANNOTATION_PROCESSOR_LENGTH,
            allow_empty=True,
        )


def _validate_annotation_text(
    field_name: str,
    value: str,
    limit: int,
    *,
    allow_empty: bool,
) -> None:
    if not isinstance(value, str):
        raise ValueError(f"annotation {field_name} must be a string")
    if not allow_empty and not value:
        raise ValueError(f"annotation {field_name} must not be empty")
    if len(value) > limit:
        raise ValueError(f"annotation {field_name} exceeds {limit} Unicode scalars")
    if contains_non_display_character(value):
        raise ValueError(f"annotation {field_name} contains non-display characters")


def contains_non_display_character(value: str) -> bool:
    return any(
        unicodedata.category(char) in {"Cc", "Cf", "Cs"}
        or any(start <= ord(char) <= end for start, end in _NON_DISPLAY_RANGES)
        for char in value
    )


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
    index: int = -1  # chunk index for text; -1 if N/A
    chunk_text: str = ""  # for text only
    metadata: dict[str, Any] = field(default_factory=dict)


@dataclass(slots=True)
class EmbeddingSet:
    """A named bundle of vectors (e.g. all text chunks of one file)."""

    provider: str  # e.g. "ollama:nomic-embed-text"
    dim: int
    rows: list[EmbeddingRow] = field(default_factory=list)


@dataclass(slots=True)
class ProcessResult:
    """The full processor output (SPEC §9.1)."""

    summary: str | None = None
    caption: str | None = None
    tags: list[str] = field(default_factory=list)
    annotations: list[AnnotationSuggestion] = field(default_factory=list)
    # True only when an annotation model returned a usable result. The
    # indexer uses this signal to distinguish a successful empty refresh from
    # an unconfigured, skipped, or failed model call.
    annotations_complete: bool = False
    entities: list[Entity] = field(default_factory=list)
    # Keys are well-known kinds: "text", "visual", "face".
    embeddings: dict[str, EmbeddingSet] = field(default_factory=dict)
    metadata: dict[str, Any] = field(default_factory=dict)
    processor: str = ""  # filled in by the dispatching code


# ---------------------------------------------------------------------------
# Protocol + error
# ---------------------------------------------------------------------------


class ProcessorError(RuntimeError):
    """Raised by Processors on unrecoverable failure for a single file."""


@runtime_checkable
class Processor(Protocol):
    """Per-mime processing interface."""

    name: str
    accepts: list[str]  # mime patterns, e.g. ["image/*"]

    def process(self, file: FileRef) -> ProcessResult: ...
