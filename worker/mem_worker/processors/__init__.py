"""Per-mime Processors (SPEC §9.1 / §9.2).

A processor consumes a single file (resolved to bytes) and produces a
:class:`ProcessResult`. Routing is by MIME glob (e.g. ``image/*``); see
:mod:`mem_worker.processors.registry`.

Phase 1 inventory:
    * image.py — full implementation (W1)
    * text.py  — full implementation (W1)
    * pdf.py   — STUB (W2)
    * audio.py — STUB (W2)
"""

from __future__ import annotations

from .base import (
    AnnotationSuggestion,
    Entity,
    FileRef,
    Processor,
    ProcessorError,
    ProcessResult,
)
from .registry import ProcessorRegistry, default_registry

__all__ = [
    "AnnotationSuggestion",
    "Entity",
    "FileRef",
    "ProcessResult",
    "Processor",
    "ProcessorError",
    "ProcessorRegistry",
    "default_registry",
]
