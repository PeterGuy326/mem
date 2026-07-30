"""PDFProcessor — extract a text layer via ``pypdf``, then reuse the text pipeline.

Phase: W2 (real implementation).

    1. Parse the PDF with ``pypdf`` and extract text per page.
    2. Concatenate page text and hand it to :class:`TextProcessor` so PDFs get
       the same chunk + embed + summarize treatment as plain text — no duplicated
       logic, embeddings land under the well-known ``"text"`` kind.
    3. Surface ``page_count`` / extracted length in metadata.

Failures are non-fatal and match the house style: a malformed PDF, or a scanned
PDF with no embedded text layer, returns a ``ProcessResult`` with a clear marker
in ``metadata`` rather than raising. OCR fallback for scanned PDFs (Tesseract /
rapidocr) remains future work.
"""

from __future__ import annotations

from io import BytesIO
from typing import Optional

from ..logging import get_logger
from ..providers import EmbeddingProvider, LLMProvider
from .base import ANNOTATION_ANALYSIS_VERSION, FileRef, ProcessResult
from .text import TextProcessor

log = get_logger(__name__)


class PDFProcessor:
    name = "pdf"
    accepts = ["application/pdf"]

    def __init__(
        self,
        embedder: Optional[EmbeddingProvider] = None,
        llm: Optional[LLMProvider] = None,
        *,
        llm_spec: str | None = None,
        embedding_spec: str | None = None,
        embedding_dimensions: int | None = None,
        embedding_enabled: bool | None = None,
        llm_enabled: bool | None = None,
        analysis_version: str = ANNOTATION_ANALYSIS_VERSION,
    ):
        # Delegate the heavy lifting (chunk/embed/summarize) to TextProcessor,
        # forwarding any injected providers so tests can stub them.
        self._text = TextProcessor(
            embedder=embedder,
            llm=llm,
            llm_spec=llm_spec,
            embedding_spec=embedding_spec,
            embedding_dimensions=embedding_dimensions,
            embedding_enabled=embedding_enabled,
            llm_enabled=llm_enabled,
            analysis_version=analysis_version,
        )

    def process(self, file: FileRef) -> ProcessResult:
        try:
            from pypdf import PdfReader
        except ImportError as exc:  # dependency missing → degrade, don't crash
            return ProcessResult(
                processor=self.name,
                metadata={"error": f"pypdf unavailable: {exc}", "byte_length": len(file.data)},
            )

        try:
            reader = PdfReader(BytesIO(file.data))
        except Exception as exc:  # encrypted / malformed / truncated
            log.warning("pdf.parse_failed", file_id=file.file_id, error=str(exc))
            return ProcessResult(
                processor=self.name,
                metadata={"parse_error": str(exc), "byte_length": len(file.data)},
            )

        page_count = len(reader.pages)
        pages: list[str] = []
        for i, page in enumerate(reader.pages):
            try:
                pages.append(page.extract_text() or "")
            except Exception as exc:  # one bad page shouldn't sink the doc
                log.warning("pdf.page_extract_failed", file_id=file.file_id, page=i, error=str(exc))
                pages.append("")
        text = "\n\n".join(p for p in pages if p.strip())

        if not text.strip():
            # No extractable text layer (likely a scanned/image-only PDF).
            return ProcessResult(
                processor=self.name,
                metadata={
                    "page_count": page_count,
                    "text_empty": True,
                    "note": "no extractable text layer (OCR fallback not yet implemented)",
                    "byte_length": len(file.data),
                },
            )

        # Reuse the text pipeline by presenting the extracted text as a synthetic
        # text file. Embeddings + summary come back under the same shapes.
        synthetic = FileRef(
            file_id=file.file_id,
            storage_uri=file.storage_uri,
            mime="text/plain",
            sha256=file.sha256,
            user_id=file.user_id,
            name=file.name,
            data=text.encode("utf-8"),
            options=file.options,
        )
        result = self._text.process(synthetic)
        result.processor = self.name
        result.metadata = {
            **result.metadata,
            "page_count": page_count,
            "extracted_char_length": len(text),
            "source_mime": "application/pdf",
        }
        return result
