"""Smoke tests that don't require a live Ollama / S3.

Verifies the gRPC server module + generated proto stubs are importable and
the servicer's HealthCheck returns SERVING.
"""

from __future__ import annotations

import importlib

import grpc
import pytest


def test_mem_worker_imports():
    import mem_worker  # noqa: F401
    assert mem_worker.__version__


def test_proto_stubs_present_or_skip():
    """If the proto stubs are missing the test is skipped (CI may not have
    run ``make proto``); when present, we exercise HealthCheck end-to-end."""
    try:
        pb = importlib.import_module("mem_worker.proto.processor_pb2")
        pbg = importlib.import_module("mem_worker.proto.processor_pb2_grpc")
    except ImportError:
        pytest.skip("proto stubs not generated yet (run `make proto`)")
        return

    from mem_worker.server import ProcessorServicer

    servicer = ProcessorServicer(pb, pbg)
    resp = servicer.HealthCheck(pb.HealthCheckRequest(), context=None)
    assert resp.status == pb.HealthCheckResponse.SERVING
    assert resp.version


def test_process_returns_skipped_for_unknown_mime():
    try:
        pb = importlib.import_module("mem_worker.proto.processor_pb2")
        pbg = importlib.import_module("mem_worker.proto.processor_pb2_grpc")
    except ImportError:
        pytest.skip("proto stubs not generated yet (run `make proto`)")
        return

    from mem_worker.server import ProcessorServicer

    servicer = ProcessorServicer(pb, pbg)
    req = pb.ProcessRequest(
        file_id="00000000-0000-0000-0000-000000000000",
        storage_uri="file:///nonexistent",
        mime="application/x-unsupported-by-mem",
        sha256="",
        user_id="u",
        name="x.bin",
    )
    resp = servicer.Process(req, context=None)
    assert resp.status == pb.STATUS_SKIPPED


def test_chat_rpc_is_retired_and_llm_override_is_ignored():
    pb = importlib.import_module("mem_worker.proto.processor_pb2")
    pbg = importlib.import_module("mem_worker.proto.processor_pb2_grpc")

    from mem_worker.server import ProcessorServicer

    class Aborted(RuntimeError):
        pass

    class Context:
        def abort(self, code, details):
            assert code == grpc.StatusCode.UNIMPLEMENTED
            assert "mem_context" in details
            raise Aborted(details)

    servicer = ProcessorServicer(pb, pbg)
    with pytest.raises(Aborted):
        servicer.Chat(pb.ChatRequest(), Context())

    base = servicer._registry.find("text/plain")
    assert servicer._pick_processor(
        "text/plain", {"llm_provider": "openai:should-never-run"}
    ) is base


@pytest.mark.parametrize(
    ("mime", "embedder_getter"),
    [
        ("text/plain", lambda proc: proc._embedder),
        ("application/pdf", lambda proc: proc._text._embedder),
        ("audio/mpeg", lambda proc: proc._text._embedder),
    ],
)
def test_embedding_override_reaches_text_derived_processors(
    monkeypatch, mime, embedder_getter
):
    pb = importlib.import_module("mem_worker.proto.processor_pb2")
    pbg = importlib.import_module("mem_worker.proto.processor_pb2_grpc")
    from mem_worker import providers
    from mem_worker.server import ProcessorServicer

    marker = object()
    monkeypatch.setattr(providers, "get_embedding_provider", lambda spec: marker)

    proc = ProcessorServicer(pb, pbg)._pick_processor(
        mime, {"embedding_provider": "test:pinned-space"}
    )
    assert embedder_getter(proc) is marker
