# mem-worker — Python AI Worker (gRPC, :50051)
# Multi-stage build: uv-based deps install -> slim runtime image.
#
# Build:   docker build -t mem-worker:dev .
# Run:     docker run --rm -p 50051:50051 \
#              -e OLLAMA_BASE_URL=http://host.docker.internal:11434 \
#              mem-worker:dev

# -----------------------------------------------------------------------------
# Stage 1: build (proto codegen + dep install with uv)
# -----------------------------------------------------------------------------
FROM python:3.11-slim AS builder

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    UV_COMPILE_BYTECODE=1 \
    UV_LINK_MODE=copy

# Build tools needed for some wheels (faster-whisper -> CTranslate2 etc.)
RUN apt-get update && apt-get install -y --no-install-recommends \
        build-essential \
        ca-certificates \
        curl \
    && rm -rf /var/lib/apt/lists/*

# Install uv (single static binary).
COPY --from=ghcr.io/astral-sh/uv:latest /uv /usr/local/bin/uv

WORKDIR /app

# Copy lockfile-relevant inputs first for better layer caching.
# README.md is referenced by pyproject.toml's project.readme field — must
# be present at install time even though it's only metadata.
COPY pyproject.toml README.md ./
# Optional: uv.lock would go here once we generate one.

# Resolve + install into a project-local venv at /app/.venv.
# Runtime image does NOT need the [test] extra (and pyproject.toml has no
# [test] group — uv would reject the flag).
RUN uv venv /app/.venv \
 && uv pip install --python /app/.venv/bin/python -e .

# Now copy the actual source + proto.
COPY proto/ ./proto/
COPY mem_worker/ ./mem_worker/
COPY Makefile README.md ./

# Generate gRPC stubs (output lives inside mem_worker/proto/).
RUN /app/.venv/bin/python -m grpc_tools.protoc \
        -Iproto \
        --python_out=mem_worker/proto \
        --grpc_python_out=mem_worker/proto \
        proto/processor.proto \
 && sed -i 's/^import processor_pb2/from . import processor_pb2/' \
        mem_worker/proto/processor_pb2_grpc.py

# -----------------------------------------------------------------------------
# Stage 2: runtime
# -----------------------------------------------------------------------------
FROM python:3.11-slim AS runtime

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    MEM_WORKER_GRPC_HOST=0.0.0.0 \
    PATH="/app/.venv/bin:$PATH"

# Minimal runtime libs (libgomp for some numeric wheels; libstdc++ comes by default).
RUN apt-get update && apt-get install -y --no-install-recommends \
        libgomp1 \
        ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --create-home --shell /bin/bash mem

WORKDIR /app
COPY --from=builder /app /app
RUN chown -R mem:mem /app
USER mem

EXPOSE 50051

# Healthcheck: a tiny inline grpc call is overkill; instead probe that the
# Python module imports — proto stubs missing here means the build was wrong.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD python -c "import mem_worker, mem_worker.proto.processor_pb2_grpc" || exit 1

ENTRYPOINT ["python", "-m", "mem_worker.server"]
