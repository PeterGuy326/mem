"""Face detection + embedding via insightface (buffalo_l).

SPEC §F6 / §9.4 — face is "built-in", not a vendor-pluggable provider, so
this lives as a sibling module loaded directly by ImageProcessor rather
than through the providers registry.

Output per detected face:
    bbox: [x1, y1, x2, y2]
    embedding: 512-d normalized vector
    confidence: detector score (0-1)
    keypoints: 5 facial landmarks (optional, not stored yet)

The model is lazy-loaded on first call — importing this module costs
nothing, important because the worker may boot without the `face` extra
installed.
"""

from __future__ import annotations

import io
import threading
from dataclasses import dataclass
from typing import Optional

from ..logging import get_logger

log = get_logger(__name__)


@dataclass(slots=True)
class FaceDetection:
    bbox: list[float]              # [x1, y1, x2, y2]
    embedding: list[float]         # 512-d L2-normalized
    confidence: float              # 0.0 – 1.0


class FaceAnalyzer:
    """Wraps an insightface FaceAnalysis pipeline (default model 'buffalo_l')."""

    def __init__(self, model_name: str = "buffalo_l", det_size: int = 640):
        self._model_name = model_name
        self._det_size = (det_size, det_size)
        self._app = None
        self._lock = threading.Lock()

    def _ensure_loaded(self) -> None:
        if self._app is not None:
            return
        with self._lock:
            if self._app is not None:
                return
            try:
                from insightface.app import FaceAnalysis  # noqa: WPS433 — lazy heavy dep
            except ImportError as exc:
                raise RuntimeError(
                    "insightface not installed; install with "
                    "`uv sync --extra face` (worker/)"
                ) from exc

            log.info("face.loading", model=self._model_name)
            app = FaceAnalysis(name=self._model_name, providers=["CPUExecutionProvider"])
            app.prepare(ctx_id=-1, det_size=self._det_size)  # ctx_id=-1 -> CPU
            self._app = app
            log.info("face.loaded", model=self._model_name)

    def detect(self, image_bytes: bytes) -> list[FaceDetection]:
        """Detect faces + extract embeddings. Returns empty list if none / decode fails."""
        try:
            from PIL import Image  # noqa: WPS433
            import numpy as np  # noqa: WPS433
        except ImportError as exc:
            raise RuntimeError(f"face deps missing: {exc}") from exc

        try:
            pil = Image.open(io.BytesIO(image_bytes)).convert("RGB")
        except Exception as exc:
            log.warning("face.decode_failed", error=str(exc))
            return []

        # insightface expects BGR uint8 ndarray (cv2 convention).
        arr = np.array(pil)[..., ::-1].copy()

        self._ensure_loaded()
        faces = self._app.get(arr)
        out: list[FaceDetection] = []
        for f in faces:
            bbox = [float(x) for x in f.bbox.tolist()]
            emb = f.normed_embedding  # already L2-normalized
            out.append(FaceDetection(
                bbox=bbox,
                embedding=[float(x) for x in emb.tolist()],
                confidence=float(getattr(f, "det_score", 0.0)),
            ))
        return out


# Process-singleton — building a FaceAnalyzer twice would double-load the
# model.
_default: Optional[FaceAnalyzer] = None
_default_lock = threading.Lock()


def default_analyzer() -> FaceAnalyzer:
    global _default
    if _default is None:
        with _default_lock:
            if _default is None:
                _default = FaceAnalyzer()
    return _default
