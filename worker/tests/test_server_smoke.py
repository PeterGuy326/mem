"""Smoke tests that don't require a live Ollama / S3.

Verifies the gRPC server module + generated proto stubs are importable and
the servicer's HealthCheck returns SERVING.
"""

from __future__ import annotations

import importlib

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
