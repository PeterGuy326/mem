"""ImageProcessor — handles ``image/*``.

Current scope:
    1. Decode bytes via Pillow.
    2. Extract EXIF: capture datetime, GPS lat/lng (if any), camera make/model.
    3. Call a VLM provider to caption the image (default ``ollama:minicpm-v``).
    4. Encode the image with CLIP into the shared visual search space.
    5. Return :class:`ProcessResult` with metadata, caption, tags, and
       ``embeddings = {"visual": ...}``.

Provider construction is deferred until ``process()`` so the worker can
boot without Ollama running (gRPC HealthCheck still answers).
"""

from __future__ import annotations

import io
import math
from datetime import datetime
from typing import Any

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
from .annotations import (
    IMAGE_ANNOTATION_PROMPT,
    description_value,
    plain_description,
    structured_annotations,
    tag_values,
)
from .base import (
    PROVIDER_ERROR_MARKER,
    EmbeddingRow,
    EmbeddingSet,
    Entity,
    FileRef,
    ProcessResult,
)

log = get_logger(__name__)


# Reverse-map for human-readable EXIF tag names.
_EXIF_NAMES = {v: k for k, v in ExifTags.TAGS.items()}
_GPS_NAMES = {v: k for k, v in ExifTags.GPSTAGS.items()}

# Dimension of the embeddings_visual.embedding column in the DB schema
# (server/internal/db/migrations/0001_init.sql -> vector(512), CLIP ViT-B/32).
# Visual vectors of any other dimension are skipped rather than inserted, so a
# degraded text-embedder fallback can never abort the indexing transaction.
VISUAL_EMBED_DIM = 512


class ImageProcessor:
    """Process raster images via VLM + visual embedding."""

    name = "image"
    accepts = ["image/*"]

    def __init__(
        self,
        vlm: VLMProvider | None = None,
        embedder: EmbeddingProvider | None = None,
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
            self._embedder = get_embedding_provider(get_settings().default_visual_embedding)
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

        # 2. VLM caption + annotation suggestions in one call. Legacy VLMs
        # that return an ordinary caption instead of the requested JSON remain
        # compatible; malformed JSON-like output is never persisted verbatim.
        caption = ""
        try:
            vlm = self._resolve_vlm()
            model_output = vlm.caption(file.data, prompt=IMAGE_ANNOTATION_PROMPT)
            suggestions = structured_annotations(
                model_output,
                provider=getattr(vlm, "name", ""),
            )
            if suggestions is not None:
                result.annotations = suggestions
                result.annotations_complete = True
                caption = description_value(suggestions) or ""
                result.tags = tag_values(suggestions)
            else:
                fallback = plain_description(
                    model_output,
                    provider=getattr(vlm, "name", ""),
                )
                if fallback is not None:
                    result.annotations = [fallback]
                    result.annotations_complete = True
                    caption = fallback.value
                else:
                    result.metadata["annotation_parse_error"] = "invalid structured model output"
        except (ProviderError, NotImplementedError):
            log.warning(
                "image.vlm_failed",
                file_id=file.file_id,
                error=PROVIDER_ERROR_MARKER,
            )
            result.metadata["vlm_error"] = PROVIDER_ERROR_MARKER
        except Exception:  # noqa: BLE001 — last-line defense must stay redacted
            log.error(
                "image.vlm_unexpected",
                file_id=file.file_id,
                error=PROVIDER_ERROR_MARKER,
            )
            result.metadata["vlm_error"] = PROVIDER_ERROR_MARKER
        result.caption = caption or None

        # Provider availability probes only need to prove that the selected
        # VLM can inspect an image and return a caption. Stop here so a probe
        # never loads CLIP/face models or calls unrelated providers.
        if file.options.get("provider_probe") is True:
            result.metadata["provider_probe"] = True
            return result

        # 3. Visual embedding via the configured visual embedder.
        # If it has an embed_image() method (CLIP), use that for true visual
        # encoding; otherwise fall back to caption-text-embedding (W1 path).
        try:
            embedder = self._resolve_embedder()
            vec: list[float] | None = None
            src = "image"
            embed_image = getattr(embedder, "embed_image", None)
            if callable(embed_image):
                # The method may exist on the interface but raise
                # NotImplementedError if the concrete provider only handles text
                # (e.g. ollama/openai/anthropic text-embedding models). In that
                # case we fall back to embedding the VLM caption.
                try:
                    rows = embed_image([file.data])
                    if rows:
                        vec = rows[0]
                except NotImplementedError:
                    embed_image = None
            if vec is None and caption:
                # Fallback: caption-text-embed (degraded "visual" search).
                rows = embedder.embed_text([caption])
                if rows:
                    vec = rows[0]
                    src = "vlm_caption"

            # The embeddings_visual column is fixed at vector(VISUAL_EMBED_DIM)
            # (CLIP ViT-B/32 = 512-d) in the DB schema. A degraded fallback
            # embedder (e.g. ollama:nomic-embed-text = 768-d) would produce a
            # vector of the wrong dimension; inserting it aborts the whole
            # indexing transaction and marks the file `failed`. Guard here:
            # only emit a visual embedding when its dimension matches the
            # schema, otherwise skip it (the text link is unaffected, the
            # image still indexes with caption/EXIF metadata).
            if vec is not None and len(vec) != VISUAL_EMBED_DIM:
                log.warning(
                    "image.visual_dim_mismatch",
                    file_id=file.file_id,
                    got_dim=len(vec),
                    want_dim=VISUAL_EMBED_DIM,
                    source=src,
                    provider=embedder.name,
                )
                result.metadata["visual_embed_skipped"] = (
                    f"dim {len(vec)} != schema {VISUAL_EMBED_DIM}"
                )
                vec = None

            if vec:
                result.embeddings["visual"] = EmbeddingSet(
                    provider=embedder.name,
                    dim=len(vec),
                    rows=[
                        EmbeddingRow(
                            values=vec,
                            index=0,
                            chunk_text=caption or "",
                            metadata={"source": src},
                        )
                    ],
                )
        except (ProviderError, NotImplementedError):
            log.warning(
                "image.embed_failed",
                file_id=file.file_id,
                error=PROVIDER_ERROR_MARKER,
            )
            result.metadata["embed_error"] = PROVIDER_ERROR_MARKER
        except Exception:  # noqa: BLE001 — keep image metadata on provider bugs
            log.error(
                "image.embed_unexpected",
                file_id=file.file_id,
                error=PROVIDER_ERROR_MARKER,
            )
            result.metadata["embed_error"] = PROVIDER_ERROR_MARKER

        # 4. Face detection (opt-in via `face` extra). Each detected face
        # becomes one row in result.embeddings["face"], with bbox in the row
        # metadata; the indexer turns those into entities + embeddings_face.
        try:
            from ..providers.face import default_analyzer

            detections = default_analyzer().detect(file.data)
            if detections:
                result.embeddings["face"] = EmbeddingSet(
                    provider="insightface:buffalo_l",
                    dim=len(detections[0].embedding),
                    rows=[
                        EmbeddingRow(
                            values=d.embedding,
                            index=i,
                            chunk_text="",
                            metadata={
                                "bbox": d.bbox,
                                "confidence": d.confidence,
                            },
                        )
                        for i, d in enumerate(detections)
                    ],
                )
                # Add one person entity per face — the indexer-side clusterer
                # will reduce these to canonical clusters.
                for d in detections:
                    result.entities.append(
                        Entity(
                            type="person",
                            name="",
                            metadata={"bbox": d.bbox, "source": "insightface"},
                            confidence=d.confidence,
                        )
                    )
        except RuntimeError:
            # insightface missing or model load failed — log and continue.
            log.warning(
                "image.face_skipped",
                file_id=file.file_id,
                error=PROVIDER_ERROR_MARKER,
            )
        except Exception:  # noqa: BLE001 — last-line defense must stay redacted
            log.error(
                "image.face_unexpected",
                file_id=file.file_id,
                error=PROVIDER_ERROR_MARKER,
            )
            result.metadata["face_error"] = PROVIDER_ERROR_MARKER

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

    # Pillow exposes camera fields at the top level and capture fields in the
    # EXIF sub-IFD depending on the image/encoder. Merge both views so a real
    # DateTimeOriginal + OffsetTimeOriginal pair is not missed.
    fields = dict(raw.items())
    try:
        fields.update(raw.get_ifd(ExifTags.IFD.Exif))
    except (AttributeError, KeyError):
        pass

    # Top-level EXIF tags
    for tag_id, val in fields.items():
        name = ExifTags.TAGS.get(tag_id)
        if not name:
            continue
        if name == "Make":
            out["camera_make"] = str(val).strip()
        elif name == "Model":
            out["camera_model"] = str(val).strip()
        elif name == "Orientation":
            out["orientation"] = int(val) if isinstance(val, int) else val

    # Prefer the original capture instant, then digitized, then generic image
    # datetime. When the matching offset tag is absent, retain the timestamp
    # as naive; the Go indexer marks it timezone-unknown and never assumes UTC.
    for datetime_name, offset_name in (
        ("DateTimeOriginal", "OffsetTimeOriginal"),
        ("DateTimeDigitized", "OffsetTimeDigitized"),
        ("DateTime", "OffsetTime"),
    ):
        value = fields.get(_EXIF_NAMES.get(datetime_name))
        if value is None:
            continue
        ts = _parse_exif_dt(value, fields.get(_EXIF_NAMES.get(offset_name)))
        if ts:
            out["timeline_at"] = ts.isoformat()
            break

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


def _parse_exif_dt(val: Any, offset: Any = None) -> datetime | None:
    """Parse an EXIF datetime and its optional ``±HH:MM`` offset tag."""
    if not isinstance(val, str):
        return None
    try:
        naive = datetime.strptime(val.strip(), "%Y:%m:%d %H:%M:%S")
    except ValueError:
        return None
    if offset is None:
        return naive
    if not isinstance(offset, str):
        return None
    try:
        return datetime.strptime(
            f"{val.strip()}{offset.strip()}",
            "%Y:%m:%d %H:%M:%S%z",
        )
    except ValueError:
        return None


def _parse_gps(gps_ifd: dict) -> tuple[float | None, float | None]:
    def _to_deg(rationals: Any, max_degrees: float) -> float | None:
        try:
            d, m, s = rationals
            degrees, minutes, seconds = float(d), float(m), float(s)
        except (TypeError, ValueError, ArithmeticError):
            return None
        if not all(math.isfinite(value) for value in (degrees, minutes, seconds)):
            return None
        if (
            degrees < 0
            or degrees > max_degrees
            or minutes < 0
            or minutes >= 60
            or seconds < 0
            or seconds >= 60
            or (degrees == max_degrees and (minutes != 0 or seconds != 0))
        ):
            return None
        return degrees + minutes / 60.0 + seconds / 3600.0

    def _hemisphere(value: Any, allowed: set[str]) -> str | None:
        if not isinstance(value, str):
            return None
        normalized = value.strip().upper()
        return normalized if normalized in allowed else None

    lat_ref = _hemisphere(
        gps_ifd.get(_GPS_NAMES.get("GPSLatitudeRef", 1)),
        {"N", "S"},
    )
    lat_raw = gps_ifd.get(_GPS_NAMES.get("GPSLatitude", 2))
    lng_ref = _hemisphere(
        gps_ifd.get(_GPS_NAMES.get("GPSLongitudeRef", 3)),
        {"E", "W"},
    )
    lng_raw = gps_ifd.get(_GPS_NAMES.get("GPSLongitude", 4))
    if lat_ref is None or lng_ref is None:
        return None, None

    lat = _to_deg(lat_raw, 90)
    lng = _to_deg(lng_raw, 180)
    if lat is None or lng is None:
        return None, None
    if lat_ref == "S":
        lat = -lat
    if lng_ref == "W":
        lng = -lng
    return lat, lng
