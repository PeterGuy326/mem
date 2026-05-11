"""ImageProcessor — handles ``image/*``.

W1 scope (this file):
    1. Decode bytes via Pillow.
    2. Extract EXIF: capture datetime, GPS lat/lng (if any), camera make/model.
    3. Call a VLM provider to caption the image (default ``ollama:minicpm-v``).
    4. Call an embedding provider to encode the *caption* as a placeholder
       visual embedding. W2 will swap this for a real CLIP encoder.
    5. Return :class:`ProcessResult` with metadata, caption, tags, and
       ``embeddings = {"visual": ..., "text": ...}``.

Provider construction is deferred until ``process()`` so the worker can
boot without Ollama running (gRPC HealthCheck still answers).
"""

from __future__ import annotations

import io
from datetime import datetime
from typing import Any, Optional

from PIL import ExifTags, Image

from ..config import get_settings
from ..logging import get_logger
from ..providers import (
    EmbeddingProvider,
    ProviderError,
    VLMProvider,
    get_embedding_provider,
    get_vlm_provider,
)
from .base import EmbeddingRow, EmbeddingSet, Entity, FileRef, ProcessResult

log = get_logger(__name__)


# Reverse-map for human-readable EXIF tag names.
_EXIF_NAMES = {v: k for k, v in ExifTags.TAGS.items()}
_GPS_NAMES = {v: k for k, v in ExifTags.GPSTAGS.items()}


class ImageProcessor:
    """Process raster images via VLM + visual embedding."""

    name = "image"
    accepts = ["image/*"]

    def __init__(
        self,
        vlm: Optional[VLMProvider] = None,
        embedder: Optional[EmbeddingProvider] = None,
    ):
        # Allow dependency injection (mostly for tests). In production
        # these are resolved lazily on first process() to avoid touching
        # Ollama at import time.
        self._vlm = vlm
        self._embedder = embedder

    # ---- helpers ----

    def _resolve_vlm(self) -> VLMProvider:
        if self._vlm is None:
            self._vlm = get_vlm_provider(get_settings().default_vlm)
        return self._vlm

    def _resolve_embedder(self) -> EmbeddingProvider:
        if self._embedder is None:
            self._embedder = get_embedding_provider(
                get_settings().default_visual_embedding
            )
        return self._embedder

    # ---- main entrypoint ----

    def process(self, file: FileRef) -> ProcessResult:
        result = ProcessResult(processor=self.name)

        # 1. Decode + EXIF.
        try:
            img = Image.open(io.BytesIO(file.data))
            img.load()
        except Exception as exc:
            log.warning("image.decode_failed", file_id=file.file_id, error=str(exc))
            # Degrade gracefully: even an undecodable image gets *some* row in DB.
            result.metadata = {"decode_error": str(exc)}
            return result

        exif_meta = _extract_exif(img)
        result.metadata = {
            "format": img.format,
            "mode": img.mode,
            "width": img.width,
            "height": img.height,
            **exif_meta,
        }

        # GPS -> entity (place)
        if "gps" in exif_meta:
            lat = exif_meta["gps"].get("lat")
            lng = exif_meta["gps"].get("lng")
            if lat is not None and lng is not None:
                result.entities.append(
                    Entity(
                        type="place",
                        name="",
                        metadata={"lat": lat, "lng": lng, "source": "exif"},
                        confidence=1.0,
                    )
                )

        # 2. VLM caption (degrades to "" on provider error so we still
        #    return *something*; the pipeline status will mark as PARTIAL).
        caption = ""
        try:
            vlm = self._resolve_vlm()
            caption = vlm.caption(file.data)
        except (ProviderError, NotImplementedError) as exc:
            log.warning("image.vlm_failed", file_id=file.file_id, error=str(exc))
            result.metadata["vlm_error"] = str(exc)
        except Exception as exc:  # noqa: BLE001 — last-line defense
            log.exception("image.vlm_unexpected", file_id=file.file_id)
            result.metadata["vlm_error"] = str(exc)
        result.caption = caption or None

        # 3. Embed the caption as a *text* embedding (W2 will add a real
        #    CLIP visual embedding alongside).
        if caption:
            try:
                embedder = self._resolve_embedder()
                vectors = embedder.embed_text([caption])
                if vectors:
                    result.embeddings["visual"] = EmbeddingSet(
                        provider=embedder.name,
                        dim=len(vectors[0]),
                        rows=[
                            EmbeddingRow(
                                values=vectors[0],
                                index=0,
                                chunk_text=caption,
                                metadata={"source": "vlm_caption"},
                            )
                        ],
                    )
            except (ProviderError, NotImplementedError) as exc:
                log.warning("image.embed_failed", file_id=file.file_id, error=str(exc))
                result.metadata["embed_error"] = str(exc)

        return result


# ---------------------------------------------------------------------------
# EXIF helpers
# ---------------------------------------------------------------------------

def _extract_exif(img: Image.Image) -> dict[str, Any]:
    """Pull common EXIF fields into a JSON-safe dict."""
    out: dict[str, Any] = {}
    try:
        raw = img.getexif()
    except Exception:
        return out
    if not raw:
        return out

    # Top-level EXIF tags
    for tag_id, val in raw.items():
        name = ExifTags.TAGS.get(tag_id)
        if not name:
            continue
        if name == "DateTimeOriginal" or name == "DateTime":
            ts = _parse_exif_dt(val)
            if ts:
                out.setdefault("timeline_at", ts.isoformat())
        elif name == "Make":
            out["camera_make"] = str(val).strip()
        elif name == "Model":
            out["camera_model"] = str(val).strip()
        elif name == "Orientation":
            out["orientation"] = int(val) if isinstance(val, int) else val

    # GPS sub-IFD
    try:
        gps_ifd = raw.get_ifd(ExifTags.IFD.GPSInfo)
    except (AttributeError, KeyError):
        gps_ifd = None
    if gps_ifd:
        lat, lng = _parse_gps(gps_ifd)
        if lat is not None and lng is not None:
            out["gps"] = {"lat": lat, "lng": lng}

    return out


def _parse_exif_dt(val: Any) -> Optional[datetime]:
    """EXIF datetime: ``"YYYY:MM:DD HH:MM:SS"``."""
    if not isinstance(val, str):
        return None
    try:
        return datetime.strptime(val.strip(), "%Y:%m:%d %H:%M:%S")
    except ValueError:
        return None


def _parse_gps(gps_ifd: dict) -> tuple[Optional[float], Optional[float]]:
    def _to_deg(rationals) -> Optional[float]:
        try:
            d, m, s = rationals
            return float(d) + float(m) / 60.0 + float(s) / 3600.0
        except (TypeError, ValueError):
            return None

    lat_ref = gps_ifd.get(_GPS_NAMES.get("GPSLatitudeRef", 1), "N")
    lat_raw = gps_ifd.get(_GPS_NAMES.get("GPSLatitude", 2))
    lng_ref = gps_ifd.get(_GPS_NAMES.get("GPSLongitudeRef", 3), "E")
    lng_raw = gps_ifd.get(_GPS_NAMES.get("GPSLongitude", 4))

    lat = _to_deg(lat_raw) if lat_raw else None
    lng = _to_deg(lng_raw) if lng_raw else None
    if lat is not None and isinstance(lat_ref, str) and lat_ref.upper() == "S":
        lat = -lat
    if lng is not None and isinstance(lng_ref, str) and lng_ref.upper() == "W":
        lng = -lng
    return lat, lng
