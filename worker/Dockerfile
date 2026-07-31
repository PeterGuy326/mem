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
FROM python:3.11.15-slim AS builder

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    UV_COMPILE_BYTECODE=1 \
    UV_LINK_MODE=copy

# Install a versioned uv binary rather than following a mutable latest tag.
COPY --from=ghcr.io/astral-sh/uv:0.11.14 /uv /usr/local/bin/uv

WORKDIR /app

# Copy the locked dependency inputs before source for layer caching.
COPY pyproject.toml uv.lock README.md ./

ARG WORKER_EXTRAS=""
# Install locked third-party dependencies without the local project. Only the
# explicitly supported production extras are accepted.
RUN set -eu; set -f; extra_args=""; old_ifs="${IFS}"; IFS=,; \
    for extra in ${WORKER_EXTRAS}; do \
      case "${extra}" in \
        asr|clip|face) extra_args="${extra_args} --extra ${extra}" ;; \
        *) echo "WORKER_EXTRAS accepts only comma-separated asr, clip, and face" >&2; exit 2 ;; \
      esac; \
    done; \
    IFS="${old_ifs}"; \
    printf '%s\n' "${extra_args}" >/tmp/worker-extra-args; \
    uv sync --frozen --no-dev --no-install-project ${extra_args}

# Copy the actual source + proto and install the project without re-resolving.
COPY proto/ ./proto/
COPY mem_worker/ ./mem_worker/
COPY Makefile README.md ./
RUN extra_args=$(cat /tmp/worker-extra-args); \
    uv sync --frozen --no-dev ${extra_args}

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
FROM python:3.11.15-slim AS runtime

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    MEM_WORKER_GRPC_HOST=0.0.0.0 \
    PATH="/app/.venv/bin:$PATH"

# Optional numeric/model wheels need libgomp; the model-free image does not.
ARG WORKER_EXTRAS=""
RUN if [ -n "${WORKER_EXTRAS}" ]; then \
      apt-get update \
      && apt-get install -y --no-install-recommends libgomp1 \
      && rm -rf /var/lib/apt/lists/*; \
    fi \
    && useradd --create-home --shell /bin/bash mem

WORKDIR /app
COPY --from=builder /app /app
RUN chown -R mem:mem /app
USER mem

EXPOSE 50051

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD python -m mem_worker.healthcheck || exit 1

ENTRYPOINT ["python", "-m", "mem_worker.server"]
