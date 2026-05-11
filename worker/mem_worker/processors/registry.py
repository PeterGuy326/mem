"""MIME-based Processor routing.

Lookup is by glob-style mime pattern (the same syntax we put in
``Processor.accepts``):

    "image/*"               -> matches any image/...
    "application/pdf"       -> exact
    "text/*"                -> matches any text/...
    "*/*"                   -> catch-all (rare)

The default registry is constructed lazily so that importing this module
has no side effects (and tests can swap implementations cheaply).
"""

from __future__ import annotations

import fnmatch
from typing import Iterable, Optional

from ..logging import get_logger
from .base import Processor

log = get_logger(__name__)


class ProcessorRegistry:
    """Ordered list of registered processors with MIME-based lookup.

    First match wins, so register more specific patterns first.
    """

    def __init__(self) -> None:
        self._items: list[Processor] = []

    # ---- registration ----

    def register(self, processor: Processor) -> None:
        if not getattr(processor, "accepts", None):
            raise ValueError(
                f"processor {processor!r} has empty `accepts`; refusing to register"
            )
        self._items.append(processor)
        log.info(
            "processor.registered",
            name=processor.name,
            accepts=processor.accepts,
        )

    def register_all(self, processors: Iterable[Processor]) -> None:
        for p in processors:
            self.register(p)

    # ---- lookup ----

    def find(self, mime: str) -> Optional[Processor]:
        """Return the first processor whose ``accepts`` matches ``mime``.

        Matching is via ``fnmatch.fnmatchcase`` — MIME types are case-sensitive
        by RFC, but in practice are always lower-case; we normalize anyway.
        """
        mime_norm = (mime or "").strip().lower()
        for proc in self._items:
            for pattern in proc.accepts:
                if fnmatch.fnmatchcase(mime_norm, pattern.lower()):
                    return proc
        return None

    def all(self) -> list[Processor]:
        return list(self._items)

    def __len__(self) -> int:
        return len(self._items)


# ---------------------------------------------------------------------------
# Default registry
# ---------------------------------------------------------------------------

_default: Optional[ProcessorRegistry] = None


def default_registry() -> ProcessorRegistry:
    """Build (once) and return the process-wide default registry.

    Processors are constructed *without* their downstream Providers — those
    are injected lazily inside ``process()`` from the registry getter — so
    importing this module is safe even when Ollama / boto3 are unavailable.
    """
    global _default
    if _default is not None:
        return _default

    from .image import ImageProcessor
    from .text import TextProcessor
    from .pdf import PDFProcessor
    from .audio import AudioProcessor

    reg = ProcessorRegistry()
    # Order matters: register more specific mime patterns before generic ones.
    reg.register(ImageProcessor())
    reg.register(PDFProcessor())
    reg.register(AudioProcessor())
    reg.register(TextProcessor())     # "text/*" + json + common code mimes
    _default = reg
    return reg


def reset_default_registry() -> None:
    """Test helper: force the next ``default_registry()`` to rebuild."""
    global _default
    _default = None
