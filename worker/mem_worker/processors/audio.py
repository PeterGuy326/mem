"""AudioProcessor — transcribe via faster-whisper, then run the text pipeline.

Phase: W2 (real implementation).

    1. Run ASR (faster-whisper) on the audio bytes to get a transcript.
    2. Hand the transcript to :class:`TextProcessor` so audio gets the same
       chunk + embed + summarize treatment — embeddings land under the
       well-known ``"text"`` kind, making the recording searchable by content.
    3. Surface language / duration / transcript length in metadata.

Failures are non-fatal and match the house style: if ASR is unavailable (model
download blocked, faster-whisper missing) or the clip yields no speech, we
return a ``ProcessResult`` with a clear marker in ``metadata`` rather than
raising. Speaker diarization is left as future work.
"""

from __future__ import annotations

from ..config import get_settings
from ..logging import get_logger
from ..providers import ASRProvider, LLMProvider, ProviderError, get_asr_provider
from ..providers.base import EmbeddingProvider
from .base import ANNOTATION_ANALYSIS_VERSION, PROVIDER_ERROR_MARKER, FileRef, ProcessResult
from .text import TextProcessor

log = get_logger(__name__)


class AudioProcessor:
    name = "audio"
    accepts = ["audio/*"]

    def __init__(
        self,
        asr: ASRProvider | None = None,
        embedder: EmbeddingProvider | None = None,
        llm: LLMProvider | None = None,
        *,
        llm_spec: str | None = None,
        asr_spec: str | None = None,
        asr_enabled: bool | None = None,
        embedding_spec: str | None = None,
        embedding_dimensions: int | None = None,
        embedding_enabled: bool | None = None,
        llm_enabled: bool | None = None,
        analysis_version: str = ANNOTATION_ANALYSIS_VERSION,
    ):
        self._asr = asr
        self._asr_spec = asr_spec
        self._asr_enabled = asr_enabled
        # Delegate chunk/embed/summarize to TextProcessor, forwarding any
        # injected providers so tests can stub them.
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

    def _resolve_asr(self) -> ASRProvider | None:
        if self._asr_enabled is False:
            return None
        if self._asr is None:
            spec = self._asr_spec
            if spec is None:
                if self._asr_enabled is True:
                    raise ProviderError("profile asr provider is missing")
                spec = get_settings().default_asr
            self._asr = get_asr_provider(spec)
        return self._asr

    def process(self, file: FileRef) -> ProcessResult:
        if self._asr_enabled is False:
            return ProcessResult(
                processor=self.name,
                metadata={
                    "asr_skipped": "disabled_by_profile",
                    "byte_length": len(file.data),
                },
            )
        try:
            asr = self._resolve_asr()
            if asr is None:
                raise ProviderError("profile asr provider is missing")
            transcription = asr.transcribe(file.data)
        except (ProviderError, NotImplementedError):
            log.warning(
                "audio.asr_failed",
                file_id=file.file_id,
                error=PROVIDER_ERROR_MARKER,
            )
            return ProcessResult(
                processor=self.name,
                metadata={
                    "asr_error": PROVIDER_ERROR_MARKER,
                    "byte_length": len(file.data),
                },
            )
        except Exception:  # noqa: BLE001 — ASR bugs must stay non-fatal and redacted
            log.error(
                "audio.asr_unexpected",
                file_id=file.file_id,
                error=PROVIDER_ERROR_MARKER,
            )
            return ProcessResult(
                processor=self.name,
                metadata={
                    "asr_error": PROVIDER_ERROR_MARKER,
                    "byte_length": len(file.data),
                },
            )

        text = (transcription.text or "").strip()
        base_meta = {
            "language": transcription.language,
            "duration_sec": round(transcription.duration, 2),
            "transcript_char_length": len(text),
            "asr_provider": asr.name,
        }
        if not text:
            return ProcessResult(
                processor=self.name,
                metadata={**base_meta, "transcript_empty": True, "byte_length": len(file.data)},
            )

        # Reuse the text pipeline by presenting the transcript as a text file.
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
        result.metadata = {**result.metadata, **base_meta, "source_mime": file.mime}
        return result
