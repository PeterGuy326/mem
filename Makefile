# mem — Phase 1 MVP Makefile

.PHONY: help up down logs reset proto proto-go proto-python server cli mcp worker web test fmt lint \
        build build-memd build-mem build-mem-mcp

BIN_DIR ?= bin

help:
	@echo "mem dev commands:"
	@echo "  make up           - 启动 postgres/redis/minio (docker compose)"
	@echo "  make down         - 停止所有依赖"
	@echo "  make logs         - 跟踪 docker logs"
	@echo "  make reset        - ⚠️ 删除所有 volume 数据后重启"
	@echo "  make proto        - 编译 .proto -> Go/Python stubs"
	@echo "  make proto-go     - 仅生成 Go protobuf stubs"
	@echo "  make proto-python - 仅生成 Python protobuf stubs"
	@echo "  make server       - 启动 Go 服务 (memd)"
	@echo "  make cli          - 跑 CLI（go run）"
	@echo "  make mcp          - 跑 MCP server（go run, stdio）"
	@echo "  make worker       - 启动 Python AI worker"
	@echo "  make web          - 启动 React 前端 (vite dev)"
	@echo "  make test         - 跑所有测试"
	@echo "  make build        - 编译三个二进制到 $(BIN_DIR)/ (memd, mem, mem-mcp)"
	@echo "  make build-mem-mcp- 单独编译 MCP server，可放到 PATH 给 Claude Desktop 用"

up:
	docker compose up -d

down:
	docker compose down

logs:
	docker compose logs -f --tail=100

reset:
	docker compose down -v
	docker compose up -d

proto: proto-go proto-python

proto-go:
	@echo "[proto] generating Go stubs -> server/internal/workerpb/"
	@protoc -I worker/proto \
		--go_out=. --go_opt=paths=import \
		--go-grpc_out=. --go-grpc_opt=paths=import \
		worker/proto/processor.proto
	@# protoc writes under the go_package import path; move them into place.
	@mv -f github.com/PeterGuy326/mem/server/internal/workerpb/*.pb.go server/internal/workerpb/
	@rm -rf github.com

proto-python:
	@echo "[proto] generating Python stubs -> worker/mem_worker/proto/"
	@cd worker && $(MAKE) proto

server:
	cd server && go run ./cmd/memd

cli:
	cd server && go run ./cmd/mem

mcp:
	cd server && go run ./cmd/mem-mcp

worker:
	cd worker && python -m mem_worker.server

web:
	cd web && npm run dev

test:
	cd server && go test ./...
	cd worker && uv run pytest

fmt:
	cd server && gofmt -w .
	cd worker && uv run ruff format .
	cd web && npm run format

lint:
	cd server && go vet ./...
	cd worker && uv run ruff check .
	cd web && npm run lint

# --- release builds ---

build: build-memd build-mem build-mem-mcp
	@echo "✅ built: $(BIN_DIR)/memd $(BIN_DIR)/mem $(BIN_DIR)/mem-mcp"

build-memd:
	@mkdir -p $(BIN_DIR)
	cd server && go build -o ../$(BIN_DIR)/memd ./cmd/memd

build-mem:
	@mkdir -p $(BIN_DIR)
	cd server && go build -o ../$(BIN_DIR)/mem ./cmd/mem

build-mem-mcp:
	@mkdir -p $(BIN_DIR)
	cd server && go build -o ../$(BIN_DIR)/mem-mcp ./cmd/mem-mcp
