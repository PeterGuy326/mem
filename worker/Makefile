# mem-worker — build helpers
#
# Common targets:
#   make install   # uv sync (creates .venv, installs deps)
#   make proto     # regenerate Python gRPC stubs from proto/*.proto
#   make test      # pytest
#   make run       # python -m mem_worker.server
#   make lint      # ruff + mypy
#   make clean     # remove generated code + caches

PYTHON ?= python3.11
UV     ?= uv
PROTO_SRC := proto
PROTO_OUT := mem_worker/proto

.PHONY: install proto test run lint fmt clean docker

install:
	$(UV) sync --extra test

# Regenerate Python protobuf + grpc stubs.
# We rewrite the "import processor_pb2" -> "from . import processor_pb2"
# so the generated package is importable from anywhere.
proto:
	@mkdir -p $(PROTO_OUT)
	@touch $(PROTO_OUT)/__init__.py
	$(UV) run python -m grpc_tools.protoc \
		-I$(PROTO_SRC) \
		--python_out=$(PROTO_OUT) \
		--grpc_python_out=$(PROTO_OUT) \
		$(PROTO_SRC)/processor.proto
	@# Rewrite generated grpc stub to use relative imports (otherwise
	@# `from mem_worker.proto import processor_pb2_grpc` fails on import).
	@sed -i.bak 's/^import processor_pb2/from . import processor_pb2/' \
		$(PROTO_OUT)/processor_pb2_grpc.py && rm -f $(PROTO_OUT)/*.bak
	@echo "proto: generated $(PROTO_OUT)/processor_pb2.py and processor_pb2_grpc.py"

test:
	$(UV) run pytest

run:
	$(UV) run python -m mem_worker.server

lint:
	$(UV) run ruff check mem_worker tests
	$(UV) run mypy mem_worker || true

fmt:
	$(UV) run ruff format mem_worker tests
	$(UV) run ruff check --fix mem_worker tests

clean:
	rm -rf .venv .pytest_cache .ruff_cache .mypy_cache \
	       $(PROTO_OUT)/processor_pb2.py \
	       $(PROTO_OUT)/processor_pb2_grpc.py
	find . -name __pycache__ -type d -exec rm -rf {} + 2>/dev/null || true

docker:
	docker build -t mem-worker:dev .
