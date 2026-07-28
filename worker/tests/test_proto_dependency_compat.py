"""Keep declared runtime floors compatible with checked-in generated stubs."""

import re
import tomllib
from pathlib import Path

WORKER_ROOT = Path(__file__).resolve().parents[1]


def _version_tuple(value: str) -> tuple[int, ...]:
    parts = tuple(int(part) for part in value.split("."))
    return parts + (0,) * max(0, 3 - len(parts))


def _dependency_floor(dependencies: list[str], name: str) -> tuple[int, ...]:
    pattern = re.compile(rf"^{re.escape(name)}>=(\d+(?:\.\d+)*)$")
    for requirement in dependencies:
        if match := pattern.fullmatch(requirement):
            return _version_tuple(match.group(1))
    raise AssertionError(f"{name} must declare an explicit >= runtime floor")


def test_generated_proto_versions_fit_declared_runtime_floors() -> None:
    project = tomllib.loads((WORKER_ROOT / "pyproject.toml").read_text())
    dependencies = project["project"]["dependencies"]

    protobuf_stub = (
        WORKER_ROOT / "mem_worker" / "proto" / "processor_pb2.py"
    ).read_text()
    grpc_stub = (
        WORKER_ROOT / "mem_worker" / "proto" / "processor_pb2_grpc.py"
    ).read_text()

    protobuf_match = re.search(
        r"^# Protobuf Python Version: (\d+(?:\.\d+)*)$",
        protobuf_stub,
        re.MULTILINE,
    )
    grpc_match = re.search(
        r"^GRPC_GENERATED_VERSION = ['\"](\d+(?:\.\d+)*)['\"]$",
        grpc_stub,
        re.MULTILINE,
    )
    assert protobuf_match, "generated protobuf runtime version is missing"
    assert grpc_match, "generated gRPC runtime version is missing"

    protobuf_generated = _version_tuple(protobuf_match.group(1))
    grpc_generated = _version_tuple(grpc_match.group(1))

    assert _dependency_floor(dependencies, "protobuf") >= protobuf_generated
    assert _dependency_floor(dependencies, "grpcio") >= grpc_generated
    assert _dependency_floor(dependencies, "grpcio-tools") >= grpc_generated
