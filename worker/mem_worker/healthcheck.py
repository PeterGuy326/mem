"""Process-local gRPC liveness probe for the mem Worker container."""

from __future__ import annotations

import sys

import grpc

from .config import get_settings
from .proto import processor_pb2, processor_pb2_grpc


def main() -> int:
    settings = get_settings()
    target = f"127.0.0.1:{settings.grpc_port}"
    channel = grpc.insecure_channel(target)
    try:
        grpc.channel_ready_future(channel).result(timeout=3)
        response = processor_pb2_grpc.ProcessorServiceStub(channel).HealthCheck(
            processor_pb2.HealthCheckRequest(),
            timeout=3,
        )
        if response.status != processor_pb2.HealthCheckResponse.SERVING:
            return 1
        return 0
    except (grpc.RpcError, grpc.FutureTimeoutError):
        return 1
    finally:
        channel.close()


if __name__ == "__main__":
    sys.exit(main())
