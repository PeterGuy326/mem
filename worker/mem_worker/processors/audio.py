"""AudioProcessor — STUB.

Phase: W2. The real implementation will:
    1. Run ``faster-whisper`` ASR on the bytes.
    2. Pipe the transcript through TextProcessor for chunked embeddings
       and a summary.
    3. Surface language, duration, and speaker count (if diarization)
       in metadata.

For W1 we register a placeholder so routing works end-to-end.
"""

from __future__ import annotations

from .base import FileRef, ProcessResult


class AudioProcessor:
    name = "audio"
    accepts = ["audio/*"]

    def process(self, file: FileRef) -> ProcessResult:
        return ProcessResult(
            processor=self.name,
            metadata={
                "stub": True,
                "byte_length": len(file.data),
                "note": "AudioProcessor.process is scheduled for W2",
            },
        )
