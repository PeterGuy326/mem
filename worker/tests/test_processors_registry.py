"""Tests for the Processor registry + MIME routing."""

from __future__ import annotations

import pytest

from mem_worker.processors import (
    FileRef,
    ProcessResult,
    ProcessorRegistry,
    default_registry,
)
from mem_worker.processors.registry import reset_default_registry


class FakeProc:
    """Minimal Processor implementation for tests."""

    def __init__(self, name: str, accepts: list[str], tag: str = "fake"):
        self.name = name
        self.accepts = accepts
        self._tag = tag

    def process(self, file: FileRef) -> ProcessResult:
        return ProcessResult(processor=self.name, tags=[self._tag])


def test_register_and_find_exact_mime():
    reg = ProcessorRegistry()
    reg.register(FakeProc("pdf", ["application/pdf"]))
    assert reg.find("application/pdf").name == "pdf"
    assert reg.find("application/PDF").name == "pdf"   # case-insensitive
    assert reg.find("text/plain") is None


def test_register_glob_pattern_matches():
    reg = ProcessorRegistry()
    reg.register(FakeProc("image", ["image/*"]))
    assert reg.find("image/jpeg").name == "image"
    assert reg.find("image/png").name == "image"
    assert reg.find("image/heic").name == "image"
    assert reg.find("video/mp4") is None


def test_first_match_wins():
    reg = ProcessorRegistry()
    specific = FakeProc("pdf", ["application/pdf"])
    catchall = FakeProc("text", ["application/*"])
    reg.register(specific)
    reg.register(catchall)
    # Registered first -> wins.
    assert reg.find("application/pdf").name == "pdf"
    assert reg.find("application/json").name == "text"


def test_register_empty_accepts_rejected():
    reg = ProcessorRegistry()
    with pytest.raises(ValueError):
        reg.register(FakeProc("bad", []))


def test_default_registry_contains_all_phase1_processors():
    reset_default_registry()
    reg = default_registry()
    names = {p.name for p in reg.all()}
    assert names == {"image", "text", "pdf", "audio"}

    # Routing smoke-test
    assert reg.find("image/jpeg").name == "image"
    assert reg.find("application/pdf").name == "pdf"
    assert reg.find("audio/mpeg").name == "audio"
    assert reg.find("text/plain").name == "text"
    assert reg.find("text/markdown").name == "text"
    assert reg.find("application/json").name == "text"
    # Unsupported type
    assert reg.find("video/mp4") is None
    # default_registry() is cached
    assert default_registry() is reg


def test_processor_protocol_compliance():
    """Phase-1 concrete processors must structurally satisfy the Processor Protocol."""
    from mem_worker.processors.audio import AudioProcessor
    from mem_worker.processors.image import ImageProcessor
    from mem_worker.processors.pdf import PDFProcessor
    from mem_worker.processors.text import TextProcessor

    for cls in (ImageProcessor, TextProcessor, PDFProcessor, AudioProcessor):
        inst = cls()
        assert isinstance(inst.name, str) and inst.name
        assert isinstance(inst.accepts, list) and inst.accepts
        assert callable(inst.process)


def test_stub_processors_return_marker():
    # AudioProcessor is still a W2 stub. PDFProcessor is now real (see
    # test_pdf_processor_* in test_processor_logic.py) — on a non-PDF payload
    # it must degrade gracefully (parse_error / text_empty), never stub.
    from mem_worker.processors.audio import AudioProcessor
    from mem_worker.processors.pdf import PDFProcessor

    r = PDFProcessor().process(FileRef(
        file_id="x", storage_uri="file:///dev/null", mime="application/pdf",
        sha256="", user_id="u", data=b"%PDF-",
    ))
    assert r.metadata.get("stub") is not True
    assert "parse_error" in r.metadata or r.metadata.get("text_empty")

    r = AudioProcessor().process(FileRef(
        file_id="x", storage_uri="file:///dev/null", mime="audio/mpeg",
        sha256="", user_id="u", data=b"\xff\xfb",
    ))
    assert r.metadata.get("stub") is True
