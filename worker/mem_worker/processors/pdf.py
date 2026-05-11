"""PDFProcessor — STUB.

Phase: W2. The real implementation will:
    1. Extract text per page via ``pypdf`` (already a declared dependency).
    2. Fall back to OCR (Tesseract via ``pytesseract`` or ``rapidocr-onnx``)
       when a page yields < N chars (scanned PDF detection).
    3. Concatenate page text, then hand off to TextProcessor's chunk +
       embed + summarize pipeline.

For W1 we register a placeholder that returns an empty result with a clear
"not implemented" marker in metadata so backend tests can exercise routing.
"""

from __future__ import annotations

from .base import FileRef, ProcessResult


class PDFProcessor:
    name = "pdf"
    accepts = ["application/pdf"]

    def process(self, file: FileRef) -> ProcessResult:
        return ProcessResult(
            processor=self.name,
            metadata={
                "stub": True,
                "byte_length": len(file.data),
                "note": "PDFProcessor.process is scheduled for W2",
            },
        )
