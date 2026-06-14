"""faster-whisper ASR provider.

Wraps :class:`faster_whisper.WhisperModel` behind the :class:`ASRProvider`
Protocol. CPU + int8 by default so it runs on a plain dev box; the model is
loaded lazily on first ``transcribe`` and cached on the instance (first call
downloads the weights into the HF cache).

Audio bytes are written to a temp file before handing off — faster-whisper
decodes via PyAV/ffmpeg, and a real file path is the most format-agnostic input
(mp3 / m4a / wav / flac all work).
"""

from __future__ import annotations

import os
import tempfile

from ..logging import get_logger
from .base import ProviderError, Transcription

log = get_logger(__name__)


class FasterWhisperASRProvider:
    def __init__(
        self,
        model: str = "tiny",
        *,
        device: str = "cpu",
        compute_type: str = "int8",
    ):
        self.name = f"faster-whisper:{model}"
        self._model_name = model
        self._device = device
        self._compute_type = compute_type
        self._model = None  # lazy

    def _resolve_model(self):
        if self._model is None:
            try:
                from faster_whisper import WhisperModel
            except ImportError as exc:  # dependency missing → degrade upstream
                raise ProviderError(f"faster-whisper not installed: {exc}") from exc
            try:
                self._model = WhisperModel(
                    self._model_name,
                    device=self._device,
                    compute_type=self._compute_type,
                )
            except Exception as exc:  # model download / load failure
                raise ProviderError(
                    f"failed to load whisper model {self._model_name!r}: {exc}"
                ) from exc
        return self._model

    def transcribe(self, audio: bytes, **kwargs) -> Transcription:
        if not audio:
            return Transcription(text="")
        model = self._resolve_model()

        # Persist to a temp file so PyAV/ffmpeg can probe the container format.
        suffix = kwargs.get("suffix", ".audio")
        fd, path = tempfile.mkstemp(suffix=suffix)
        try:
            with os.fdopen(fd, "wb") as fh:
                fh.write(audio)
            try:
                segments, info = model.transcribe(path, beam_size=1)
                text = "".join(seg.text for seg in segments).strip()
            except Exception as exc:
                raise ProviderError(f"whisper transcribe failed: {exc}") from exc
        finally:
            try:
                os.unlink(path)
            except OSError:
                pass

        return Transcription(
            text=text,
            language=getattr(info, "language", "") or "",
            duration=float(getattr(info, "duration", 0.0) or 0.0),
        )
