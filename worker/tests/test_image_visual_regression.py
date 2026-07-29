"""Regression protection for the natural-language image-search ingest path.

These tests stay hermetic: they prove that ImageProcessor uses an image tower
without loading CLIP, Ollama, S3, or the optional face model.
"""

from __future__ import annotations

import io

import pytest
from PIL import Image
from structlog.testing import capture_logs

from mem_worker.processors.base import FileRef
from mem_worker.processors.image import ImageProcessor
from mem_worker.providers.base import ProviderError

VISUAL_DIM = 512


class FakeVLM:
    name = "fake:vlm"

    def __init__(self, caption: str = "a golden retriever on grass"):
        self.reply = caption
        self.calls: list[bytes] = []

    def caption(self, image: bytes, **_kwargs) -> str:
        self.calls.append(image)
        return self.reply

    def vqa(self, image: bytes, question: str, **_kwargs) -> str:
        return f"{question}:{len(image)}"


class FailingVLM(FakeVLM):
    def __init__(self, error: Exception):
        super().__init__()
        self.error = error

    def caption(self, image: bytes, **_kwargs) -> str:
        self.calls.append(image)
        raise self.error


class FakeVisualProvider:
    """A deterministic stand-in for CLIP's image tower."""

    name = "clip:test-image-tower"
    dim = VISUAL_DIM

    def __init__(self, *, vector_dim: int = VISUAL_DIM, error: Exception | None = None):
        self.vector = [1.0] + [0.0] * (vector_dim - 1)
        self.error = error
        self.image_calls: list[list[bytes]] = []
        self.text_calls: list[list[str]] = []

    def embed_image(self, images: list[bytes]) -> list[list[float]]:
        self.image_calls.append(list(images))
        if self.error is not None:
            raise self.error
        return [list(self.vector) for _ in images]

    def embed_text(self, texts: list[str]) -> list[list[float]]:
        self.text_calls.append(list(texts))
        raise AssertionError("an image-capable provider must not use the caption text tower")


class CaptionOnlyProvider:
    """Models a text embedder that explicitly has no image-tower support."""

    name = "fake:caption-text"
    dim = VISUAL_DIM

    def __init__(self):
        self.image_calls: list[list[bytes]] = []
        self.text_calls: list[list[str]] = []
        self.vector = [0.0, 1.0] + [0.0] * (VISUAL_DIM - 2)

    def embed_image(self, images: list[bytes]) -> list[list[float]]:
        self.image_calls.append(list(images))
        raise NotImplementedError

    def embed_text(self, texts: list[str]) -> list[list[float]]:
        self.text_calls.append(list(texts))
        return [list(self.vector) for _ in texts]


class FailingFaceAnalyzer:
    def __init__(self, error: Exception):
        self.error = error

    def detect(self, _image: bytes):
        raise self.error


@pytest.fixture(autouse=True)
def _disable_optional_face_model(monkeypatch):
    class NoFaces:
        def detect(self, _image: bytes):
            return []

    monkeypatch.setattr(
        "mem_worker.providers.face.default_analyzer",
        lambda: NoFaces(),
    )


def _png_bytes() -> bytes:
    image = Image.new("RGB", (5, 4), color=(32, 160, 48))
    buffer = io.BytesIO()
    image.save(buffer, format="PNG")
    return buffer.getvalue()


def _file(raw: bytes) -> FileRef:
    return FileRef(
        file_id="visual-regression",
        storage_uri="file:///visual-regression.png",
        mime="image/png",
        sha256="test-sha256",
        user_id="test-user",
        name="visual-regression.png",
        data=raw,
    )


def test_image_processor_sends_original_bytes_to_image_tower():
    raw = _png_bytes()
    vlm = FakeVLM()
    provider = FakeVisualProvider()

    result = ImageProcessor(vlm=vlm, embedder=provider).process(_file(raw))

    assert vlm.calls == [raw]
    assert provider.image_calls == [[raw]]
    assert provider.text_calls == []
    visual = result.embeddings["visual"]
    assert visual.provider == provider.name
    assert visual.dim == VISUAL_DIM
    assert visual.rows[0].values == provider.vector
    assert visual.rows[0].chunk_text == vlm.reply
    assert visual.rows[0].metadata == {"source": "image"}


@pytest.mark.parametrize(
    "failure",
    [
        pytest.param(ProviderError("provider secret must not persist"), id="provider-error"),
        pytest.param(NotImplementedError("vlm is not implemented"), id="not-implemented"),
        pytest.param(RuntimeError("caption backend unavailable"), id="unexpected-error"),
    ],
)
def test_image_tower_remains_available_when_captioning_fails(failure: Exception):
    raw = _png_bytes()
    provider = FakeVisualProvider()

    result = ImageProcessor(vlm=FailingVLM(failure), embedder=provider).process(_file(raw))

    assert result.caption is None
    assert result.metadata["vlm_error"] == "provider_unavailable"
    assert str(failure) not in repr(result.metadata)
    assert provider.image_calls == [[raw]]
    assert result.embeddings["visual"].rows[0].metadata == {"source": "image"}


def test_caption_text_fallback_is_explicitly_marked_as_degraded():
    raw = _png_bytes()
    vlm = FakeVLM(caption="green landscape")
    provider = CaptionOnlyProvider()

    result = ImageProcessor(vlm=vlm, embedder=provider).process(_file(raw))

    assert provider.image_calls == [[raw]]
    assert provider.text_calls == [["green landscape"]]
    visual = result.embeddings["visual"]
    assert visual.provider == provider.name
    assert visual.rows[0].values == provider.vector
    assert visual.rows[0].metadata == {"source": "vlm_caption"}


def test_wrong_dimension_visual_vector_is_rejected_before_persistence():
    raw = _png_bytes()
    provider = FakeVisualProvider(vector_dim=VISUAL_DIM - 1)

    result = ImageProcessor(vlm=FakeVLM(), embedder=provider).process(_file(raw))

    assert provider.image_calls == [[raw]]
    assert provider.text_calls == []
    assert "visual" not in result.embeddings
    assert result.metadata["visual_embed_skipped"] == "dim 511 != schema 512"


@pytest.mark.parametrize(
    "failure",
    [
        pytest.param(
            ProviderError("private CLIP upstream response"),
            id="provider-error",
        ),
        pytest.param(
            RuntimeError("private unexpected image-tower response"),
            id="unexpected-error",
        ),
    ],
)
def test_visual_provider_failure_degrades_without_aborting_image_processing(
    failure: Exception,
):
    raw = _png_bytes()
    provider = FakeVisualProvider(error=failure)

    result = ImageProcessor(vlm=FakeVLM(), embedder=provider).process(_file(raw))

    assert result.metadata["format"] == "PNG"
    assert result.caption == "a golden retriever on grass"
    assert provider.image_calls == [[raw]]
    assert provider.text_calls == []
    assert "visual" not in result.embeddings
    assert result.metadata["embed_error"] == "provider_unavailable"
    assert str(failure) not in repr(result.metadata)


@pytest.mark.parametrize(
    ("failure", "want_metadata"),
    [
        pytest.param(
            RuntimeError("private face model path"),
            False,
            id="optional-provider-unavailable",
        ),
        pytest.param(
            ValueError("private face provider response"),
            True,
            id="unexpected-provider-error",
        ),
    ],
)
def test_face_provider_failure_never_leaks_raw_error(
    monkeypatch,
    failure: Exception,
    want_metadata: bool,
):
    monkeypatch.setattr(
        "mem_worker.providers.face.default_analyzer",
        lambda: FailingFaceAnalyzer(failure),
    )

    with capture_logs() as logs:
        result = ImageProcessor(
            vlm=FakeVLM(),
            embedder=FakeVisualProvider(),
        ).process(_file(_png_bytes()))

    assert str(failure) not in repr(logs)
    assert str(failure) not in repr(result.metadata)
    if want_metadata:
        assert result.metadata["face_error"] == "provider_unavailable"
    else:
        assert "face_error" not in result.metadata
