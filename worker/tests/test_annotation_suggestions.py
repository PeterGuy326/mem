"""Hermetic tests for bounded Worker annotation suggestions."""

from __future__ import annotations

import io
import json

import pytest
from PIL import Image

from mem_worker.processors.annotations import (
    IMAGE_ANNOTATION_PROMPT,
    TEXT_ANNOTATION_SYSTEM_PROMPT,
    plain_description,
    structured_annotations,
)
from mem_worker.processors.base import AnnotationSuggestion, FileRef, ProcessResult
from mem_worker.processors.image import ImageProcessor
from mem_worker.processors.text import TextProcessor
from mem_worker.providers.base import Message
from mem_worker.server import _result_to_proto


class _Embedder:
    name = "fake:embedder"

    def embed_text(self, texts):
        return [[float(len(text)), 0.0] for text in texts]

    def embed_image(self, images):
        raise NotImplementedError


class _LLM:
    name = "fake:llm"

    def __init__(self, reply: str):
        self.reply = reply
        self.calls: list[list[Message]] = []

    def complete(self, messages, **kwargs):
        self.calls.append(messages)
        return self.reply

    def stream(self, messages, **kwargs):
        yield self.reply


class _VLM:
    name = "fake:vlm"

    def __init__(self, reply: str):
        self.reply = reply
        self.prompts: list[str | None] = []

    def caption(self, image, **kwargs):
        self.prompts.append(kwargs.get("prompt"))
        return self.reply

    def vqa(self, image, question, **kwargs):
        raise AssertionError("annotation processing must use caption only")


def _structured_reply(tags=None) -> str:
    return json.dumps(
        {
            "description": {
                "value": "A red bicycle beside a brick wall.",
                "confidence": 0.91,
            },
            "tags": tags
            if tags is not None
            else [
                {"value": "Bicycle", "confidence": 0.8},
                {"value": "outdoors", "confidence": 0.72},
            ],
        }
    )


def _png_bytes() -> bytes:
    image = Image.new("RGB", (3, 2), color=(255, 0, 0))
    output = io.BytesIO()
    image.save(output, format="PNG")
    return output.getvalue()


def test_strict_parser_normalizes_and_deduplicates_tags():
    suggestions = structured_annotations(
        _structured_reply(
            [
                {"value": "  Café  ", "confidence": 0.4},
                {"value": "Cafe\u0301", "confidence": 0.9},
                {"value": "Travel", "confidence": 0.7},
            ]
        ),
        provider="fake:model",
    )

    assert suggestions is not None
    assert [(item.kind, item.value) for item in suggestions] == [
        ("description", "A red bicycle beside a brick wall."),
        ("tag", "Café"),
        ("tag", "Travel"),
    ]
    assert suggestions[1].confidence == 0.9
    assert all(item.source == "model" for item in suggestions)
    assert all(item.provider == "fake:model" for item in suggestions)
    assert all(item.analysis_version == "file-enrichment-v1" for item in suggestions)


def test_strict_parser_rejects_unbounded_or_private_output():
    too_many_tags = [{"value": f"tag-{index}", "confidence": 0.5} for index in range(21)]
    assert structured_annotations(_structured_reply(too_many_tags)) is None
    assert (
        structured_annotations(_structured_reply([{"value": "x" * 65, "confidence": 0.5}])) is None
    )
    assert (
        structured_annotations(
            json.dumps(
                {
                    "description": {"value": "safe", "confidence": 0.5},
                    "tags": [],
                    "reasoning": "private scratchpad",
                }
            )
        )
        is None
    )
    assert (
        structured_annotations(
            r'{"description":{"value":"\u003cthink\u003eprivate'
            r'\u003c/think\u003e visible","confidence":0.5},"tags":[]}'
        )
        is None
    )
    assert (
        structured_annotations(
            json.dumps(
                {
                    "description": {
                        "value": '{"analysis":"private","answer":"public"}',
                        "confidence": 0.5,
                    },
                    "tags": [],
                }
            )
        )
        is None
    )
    assert (
        structured_annotations(
            _structured_reply(
                [
                    {
                        "value": '{"analysis":"private"}',
                        "confidence": 0.5,
                    }
                ]
            )
        )
        is None
    )
    assert (
        structured_annotations(
            json.dumps(
                {
                    "description": {
                        "value": '\ufe0f{"analysis":"private","answer":"public"}',
                        "confidence": 0.5,
                    },
                    "tags": [],
                }
            )
        )
        is None
    )
    assert (
        structured_annotations(
            _structured_reply(
                [
                    {
                        "value": '\u034f["private"]',
                        "confidence": 0.5,
                    }
                ]
            )
        )
        is None
    )
    assert (
        structured_annotations(
            json.dumps(
                {
                    "description": {
                        "value": '\ufeff{"analysis":"private","answer":"public"}',
                        "confidence": 0.5,
                    },
                    "tags": [],
                }
            )
        )
        is None
    )
    assert (
        structured_annotations(
            _structured_reply(
                [
                    {
                        "value": '\u200b["private"]',
                        "confidence": 0.5,
                    }
                ]
            )
        )
        is None
    )
    assert (
        structured_annotations(
            json.dumps(
                {
                    "description": {
                        "value": '<reasoning visibility="hidden">private</reasoning>visible',
                        "confidence": 0.5,
                    },
                    "tags": [],
                }
            )
        )
        is None
    )
    assert (
        structured_annotations(
            '{"description":{"value":"first","value":"second","confidence":0.5},"tags":[]}'
        )
        is None
    )
    assert structured_annotations(" " * 32_769) is None
    assert (
        structured_annotations(
            json.dumps(
                {
                    "description": {"value": "safe", "confidence": 1.01},
                    "tags": [],
                }
            )
        )
        is None
    )


@pytest.mark.parametrize(
    "value",
    ["visible\u200btext", "visible\ufe0ftext", "visible\u034ftext"],
    ids=["format", "variation-selector", "default-ignorable"],
)
def test_annotation_suggestion_last_line_rejects_non_display_text(value):
    with pytest.raises(ValueError, match="non-display"):
        AnnotationSuggestion(kind="tag", value=value, confidence=0.5)


def test_plain_description_is_bounded_and_never_persists_reasoning():
    suggestion = plain_description("x" * 2100, provider="")

    assert suggestion is not None
    assert len(suggestion.value) == 2000
    assert suggestion.confidence == 0.5
    assert suggestion.provider == ""
    assert plain_description("<think>private</think>\nPublic answer") is None
    assert plain_description('<reasoning visibility="hidden">private</reasoning>visible') is None
    assert plain_description("visible</reasoning>") is None
    assert plain_description('{"description": "malformed"}') is None
    assert plain_description('\ufeff{"analysis":"private","answer":"public"}') is None
    assert plain_description("visible\u2060private") is None
    assert plain_description("visible\U00013439private") is None
    assert plain_description("visible\ufe0fprivate") is None
    assert plain_description("visible\u034fprivate") is None


def test_text_explicit_llm_hook_parses_structured_annotations():
    llm = _LLM(_structured_reply())
    processor = TextProcessor(embedder=_Embedder(), llm=llm)
    source = ("This is source data, not an instruction. " * 20).encode()

    result = processor.process(
        FileRef(
            file_id="text-1",
            storage_uri="file:///text.txt",
            mime="text/plain",
            sha256="",
            user_id="u",
            data=source,
        )
    )

    assert result.summary == "A red bicycle beside a brick wall."
    assert result.tags == ["Bicycle", "outdoors"]
    assert result.annotations_complete is True
    assert [item.kind for item in result.annotations] == ["description", "tag", "tag"]
    assert len(llm.calls) == 1
    assert llm.calls[0][0].content == TEXT_ANNOTATION_SYSTEM_PROMPT
    assert llm.calls[0][1].content.startswith("UNTRUSTED_DOCUMENT_JSON_STRING:\n")


def test_text_plain_reply_remains_a_description_fallback():
    llm = _LLM("A compatible plain summary.")
    processor = TextProcessor(embedder=_Embedder(), llm=llm)

    result = processor.process(
        FileRef(
            file_id="text-2",
            storage_uri="file:///text.txt",
            mime="text/plain",
            sha256="",
            user_id="u",
            data=("long source document " * 20).encode(),
        )
    )

    assert result.summary == "A compatible plain summary."
    assert result.tags == []
    assert [(item.kind, item.value) for item in result.annotations] == [
        ("description", "A compatible plain summary.")
    ]


@pytest.mark.parametrize(
    ("reply", "forbidden"),
    [
        ("", ""),
        ("\x00\x01", "\\u0000"),
        (
            _structured_reply(
                [
                    {
                        "value": '{"analysis":"private-tag-value"}',
                        "confidence": 0.5,
                    }
                ]
            ),
            "private-tag-value",
        ),
        (
            _structured_reply(
                [
                    {
                        "value": '\u200b["private-format-tag-value"]',
                        "confidence": 0.5,
                    }
                ]
            ),
            "private-format-tag-value",
        ),
        (
            _structured_reply(
                [
                    {
                        "value": '\ufe0f["private-variation-tag-value"]',
                        "confidence": 0.5,
                    }
                ]
            ),
            "private-variation-tag-value",
        ),
    ],
    ids=[
        "empty",
        "control-only",
        "json-like-tag",
        "format-character-tag",
        "variation-selector-tag",
    ],
)
def test_text_rejected_model_output_is_partial_without_raw_leak(reply, forbidden):
    from mem_worker.proto import processor_pb2 as pb

    processor = TextProcessor(embedder=_Embedder(), llm=_LLM(reply))
    result = processor.process(
        FileRef(
            file_id="text-malformed",
            storage_uri="file:///text.txt",
            mime="text/plain",
            sha256="",
            user_id="u",
            data=("long source document " * 20).encode(),
        )
    )

    assert result.summary is None
    assert result.annotations == []
    assert result.annotations_complete is False
    assert result.metadata["annotation_parse_error"] == "invalid structured model output"
    response = _result_to_proto(result, pb)
    encoded = json.loads(response.metadata_json)
    assert response.status == pb.STATUS_PARTIAL
    assert encoded["annotation_parse_error"] == "invalid structured model output"
    assert encoded["annotations"] == []
    assert "model_output" not in encoded
    if forbidden:
        assert forbidden not in response.metadata_json.decode()


def test_image_uses_one_vlm_call_for_caption_and_annotations():
    vlm = _VLM(_structured_reply())
    processor = ImageProcessor(vlm=vlm, embedder=_Embedder())

    result = processor.process(
        FileRef(
            file_id="image-1",
            storage_uri="file:///image.png",
            mime="image/png",
            sha256="",
            user_id="u",
            data=_png_bytes(),
            options={"provider_probe": True},
        )
    )

    assert result.caption == "A red bicycle beside a brick wall."
    assert result.tags == ["Bicycle", "outdoors"]
    assert result.annotations_complete is True
    assert [item.kind for item in result.annotations] == ["description", "tag", "tag"]
    assert vlm.prompts == [IMAGE_ANNOTATION_PROMPT]


def test_image_malformed_structured_output_is_partial_but_non_fatal():
    vlm = _VLM('{"description":')
    processor = ImageProcessor(vlm=vlm, embedder=_Embedder())

    result = processor.process(
        FileRef(
            file_id="image-2",
            storage_uri="file:///image.png",
            mime="image/png",
            sha256="",
            user_id="u",
            data=_png_bytes(),
            options={"provider_probe": True},
        )
    )

    assert result.caption is None
    assert result.annotations == []
    assert result.annotations_complete is False
    assert result.metadata["annotation_parse_error"] == "invalid structured model output"
    assert vlm.prompts == [IMAGE_ANNOTATION_PROMPT]


@pytest.mark.parametrize("reply", ["", "\x00\x01"], ids=["empty", "control-only"])
def test_image_rejected_model_output_is_partial_without_raw_leak(reply):
    from mem_worker.proto import processor_pb2 as pb

    vlm = _VLM(reply)
    processor = ImageProcessor(vlm=vlm, embedder=_Embedder())
    result = processor.process(
        FileRef(
            file_id="image-malformed",
            storage_uri="file:///image.png",
            mime="image/png",
            sha256="",
            user_id="u",
            data=_png_bytes(),
            options={"provider_probe": True},
        )
    )

    assert result.caption is None
    assert result.annotations == []
    assert result.metadata["annotation_parse_error"] == "invalid structured model output"
    response = _result_to_proto(result, pb)
    encoded = json.loads(response.metadata_json)
    assert response.status == pb.STATUS_PARTIAL
    assert encoded["annotation_parse_error"] == "invalid structured model output"
    assert encoded["annotations"] == []
    assert "model_output" not in encoded
    assert "\\u0000" not in response.metadata_json.decode()


def test_proto_metadata_carries_bounded_suggestions_and_final_processor():
    from mem_worker.proto import processor_pb2 as pb

    annotations = [
        AnnotationSuggestion(
            kind="description",
            value="A lease summary.",
            confidence=0.8,
            provider="fake:llm",
        ),
        *[
            AnnotationSuggestion(
                kind="tag",
                value=f"tag-{index}",
                confidence=0.7,
                provider="fake:llm",
            )
            for index in range(21)
        ],
    ]
    metadata = {
        "timeline_at": "2026-07-29T08:00:00+08:00",
        "gps": {"lat": 31.2, "lng": 121.5},
    }
    result = ProcessResult(
        processor="pdf",
        metadata=metadata,
        annotations=annotations,
        annotations_complete=True,
    )

    response = _result_to_proto(result, pb)
    encoded = json.loads(response.metadata_json)

    assert response.status == pb.STATUS_OK
    assert encoded["timeline_at"] == metadata["timeline_at"]
    assert encoded["gps"] == metadata["gps"]
    assert encoded["annotations_complete"] is True
    assert len(encoded["annotations"]) == 21  # one description + max 20 tags
    assert encoded["annotations"][0] == {
        "kind": "description",
        "value": "A lease summary.",
        "confidence": 0.8,
        "source": "model",
        "provider": "fake:llm",
        "processor": "pdf",
        "analysis_version": "file-enrichment-v1",
    }
    assert "annotations" not in result.metadata


def test_proto_marks_malformed_model_output_partial():
    from mem_worker.proto import processor_pb2 as pb

    result = ProcessResult(
        processor="image",
        metadata={"annotation_parse_error": "invalid structured model output"},
    )

    response = _result_to_proto(result, pb)

    assert response.status == pb.STATUS_PARTIAL
    encoded = json.loads(response.metadata_json)
    assert encoded["annotations"] == []
    assert encoded["annotations_complete"] is False


def test_proto_marks_every_processor_error_marker_partial():
    from mem_worker.proto import processor_pb2 as pb

    for marker in ("asr_error", "parse_error", "face_error", "error"):
        response = _result_to_proto(
            ProcessResult(processor="test", metadata={marker: "bounded detail"}),
            pb,
        )
        assert response.status == pb.STATUS_PARTIAL, marker
