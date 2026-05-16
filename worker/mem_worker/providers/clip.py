"""CLIP embedding provider — shared text/image latent space.

SPEC §9.4: visual embedding default is "clip:ViT-B-32" (OpenAI weights).

The same model encodes both text and images into a shared 512-d (ViT-B/32)
or 768-d (ViT-L/14) space, which is what makes natural-language image
search work — a query like "草地上的金毛" gets encoded by the text tower
and ANN-matched against image-tower embeddings produced at index time.

Spec format:
    clip:ViT-B-32                       -> open_clip "ViT-B-32" / pretrained "openai"
    clip:ViT-B-32:openai                -> explicit pretrained tag
    clip:ViT-L-14:laion2b_s32b_b82k     -> alternative checkpoint

The model is loaded LAZILY on first call so importing this module costs
nothing — important because the worker may boot without the clip extra
installed at all.
"""

from __future__ import annotations

import io
import threading
from typing import Optional

from ..logging import get_logger
from .base import EmbeddingProvider, ProviderError, Vector

log = get_logger(__name__)


class CLIPEmbeddingProvider:
    """Embedder backed by ``open_clip_torch``.

    Implements the EmbeddingProvider protocol's ``embed_text`` AND a non-
    protocol ``embed_image`` used by ImageProcessor. Callers that only need
    text embedding can use this transparently like any other embedder.
    """

    name: str

    def __init__(self, model: str = "ViT-B-32", pretrained: str = "openai"):
        self._model_name = model
        self._pretrained = pretrained
        self.name = f"clip:{model}:{pretrained}"
        self._model = None
        self._tokenizer = None
        self._preprocess = None
        self._device = "cpu"  # WSL2 default; can be flipped via env later
        self._lock = threading.Lock()

    def _ensure_loaded(self) -> None:
        if self._model is not None:
            return
        with self._lock:
            if self._model is not None:
                return
            try:
                import open_clip  # noqa: WPS433 — lazy heavy dep
            except ImportError as exc:
                raise ProviderError(
                    "CLIP requires `open_clip_torch` and `torch`; install with "
                    "`uv sync --extra clip` (worker/) or `pip install mem-worker[clip]`."
                ) from exc

            log.info("clip.loading", model=self._model_name, pretrained=self._pretrained)
            model, _, preprocess = open_clip.create_model_and_transforms(
                self._model_name, pretrained=self._pretrained, device=self._device,
            )
            model.eval()
            self._model = model
            self._preprocess = preprocess
            self._tokenizer = open_clip.get_tokenizer(self._model_name)
            log.info("clip.loaded", model=self._model_name)

    # ---- text ----

    def embed_text(self, texts: list[str]) -> list[Vector]:
        if not texts:
            return []
        self._ensure_loaded()
        import torch  # local — only after _ensure_loaded succeeded

        with torch.no_grad():
            tokens = self._tokenizer(texts).to(self._device)
            feats = self._model.encode_text(tokens)
            feats = feats / feats.norm(dim=-1, keepdim=True)  # L2 normalize
            return [row.tolist() for row in feats.cpu()]

    # ---- image ----

    def embed_image(self, images_bytes: list[bytes]) -> list[Vector]:
        """Encode raw image bytes (PNG/JPEG/...) into CLIP-image-space vectors."""
        if not images_bytes:
            return []
        from PIL import Image as PILImage  # noqa: WPS433

        self._ensure_loaded()
        import torch

        tensors = []
        for raw in images_bytes:
            try:
                img = PILImage.open(io.BytesIO(raw)).convert("RGB")
            except Exception as exc:
                raise ProviderError(f"clip: decode image failed: {exc}") from exc
            tensors.append(self._preprocess(img))

        batch = torch.stack(tensors).to(self._device)
        with torch.no_grad():
            feats = self._model.encode_image(batch)
            feats = feats / feats.norm(dim=-1, keepdim=True)
            return [row.tolist() for row in feats.cpu()]

    # ---- introspection ----

    @property
    def dim(self) -> Optional[int]:
        """Best-effort dim peek; None until first call has loaded the model."""
        if self._model is None:
            return None
        # open_clip exposes visual.output_dim on the visual tower.
        try:
            return int(self._model.visual.output_dim)
        except AttributeError:
            return None
