"""Opt-in real-model acceptance for multilingual natural-language image search.

This is deliberately excluded from the hermetic default suite: it loads a
large model checkpoint and may need network access on first use. Run it before
changing the production visual embedding space:

    MEM_RUN_VISUAL_MODEL_EVAL=1 \
    MEM_VISUAL_EVAL_PROVIDER=clip:xlm-roberta-base-ViT-B-32:laion5b_s13b_b90k \
      .venv/bin/python -m pytest \
      tests/test_multilingual_visual_acceptance.py -q
"""

from __future__ import annotations

import os
from pathlib import Path

import pytest

from mem_worker.providers import get_embedding_provider


pytestmark = pytest.mark.skipif(
    os.getenv("MEM_RUN_VISUAL_MODEL_EVAL") != "1",
    reason="set MEM_RUN_VISUAL_MODEL_EVAL=1 to load the real visual model",
)


def test_multilingual_visual_model_ranks_reference_images() -> None:
    provider_spec = os.getenv(
        "MEM_VISUAL_EVAL_PROVIDER",
        "clip:xlm-roberta-base-ViT-B-32:laion5b_s13b_b90k",
    )
    provider = get_embedding_provider(provider_spec)
    embed_image = getattr(provider, "embed_image", None)
    assert callable(embed_image), f"{provider_spec} has no image tower"

    image_dir = Path(__file__).resolve().parents[2] / "scripts" / "demo_data" / "images"
    images = [
        image_dir / "golden_retriever_grass.jpg",
        image_dir / "cat.jpg",
        image_dir / "river_landscape.jpg",
    ]
    image_vectors = embed_image([path.read_bytes() for path in images])
    queries = [
        ("a golden retriever standing on green grass", "golden_retriever_grass.jpg"),
        ("草地上的金毛", "golden_retriever_grass.jpg"),
        ("河流穿过山谷的风景", "river_landscape.jpg"),
    ]
    query_vectors = provider.embed_text([query for query, _ in queries])

    assert len(image_vectors) == len(images)
    assert all(len(vector) == 512 for vector in image_vectors)
    assert all(len(vector) == 512 for vector in query_vectors)

    for (query, expected), query_vector in zip(queries, query_vectors, strict=True):
        ranked = sorted(
            (
                (
                    sum(left * right for left, right in zip(query_vector, image_vector)),
                    image_path.name,
                )
                for image_path, image_vector in zip(images, image_vectors, strict=True)
            ),
            reverse=True,
        )
        assert ranked[0][1] == expected, (
            f"{provider_spec!r} ranked {ranked[0][1]!r} first for {query!r}; "
            f"full ranking: {ranked!r}"
        )
