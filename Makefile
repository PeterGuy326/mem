# mem — Phase 1 MVP Makefile

.PHONY: help up down logs reset proto server cli mcp worker web bootstrap \
        test test-server test-worker test-web test-recall recall-baseline \
        test-race test-env-up \
        test-env-down test-integration test-integration-race test-acceptance \
        test-agent-certification test-deploy test-deploy-build \
        test-all fmt lint build build-memd build-mem build-mem-mcp \
        proto-go proto-python

BIN_DIR ?= bin
PYTHON ?= python3
MEM_TEST_PG_PORT ?= 55432
MEM_TEST_DB ?= postgres://mem:mem@127.0.0.1:$(MEM_TEST_PG_PORT)/mem_integration_test?sslmode=disable
MEM_TEST_PROJECT ?= mem-test-$(shell pwd -P | cksum | awk '{print $$1}')

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
	@echo "  make bootstrap    - 安装锁定依赖并生成 Worker protobuf"
	@echo "  make test         - Go/Worker/Web 无数据库回归"
	@echo "  make test-recall  - 离线多语言召回基线、确定性与泄漏门禁"
	@echo "  make recall-baseline - 重录 lexical-reference 信息性基线"
	@echo "  make test-race    - 高风险 Go 路径 race 回归"
	@echo "  make test-env-up  - 启动隔离 PostgreSQL 测试环境 (:$(MEM_TEST_PG_PORT))"
	@echo "  make test-integration      - PostgreSQL migration + 集成回归"
	@echo "  make test-integration-race - PostgreSQL 集成 race 回归"
	@echo "  make test-acceptance - 隔离进程级 HTTP/CLI/MCP Agent-memory 验收"
	@echo "  make test-agent-certification - 用显式 MEM_MCP_CERT_BINARY 验证 Agent host contract"
	@echo "  make test-deploy    - 校验生产 Compose 与 Helm 配置"
	@echo "  make test-deploy-build - 校验部署配置并构建三个生产镜像"
	@echo "  make test-env-down         - 删除隔离测试环境"
	@echo "  make test-all     - 执行全部回归与进程验收（要求测试 DB 已启动）"
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
		--go_out=server/internal/workerpb --go_opt=paths=source_relative \
		--go-grpc_out=server/internal/workerpb --go-grpc_opt=paths=source_relative \
		worker/proto/processor.proto

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

bootstrap:
	cd server && go mod download
	cd worker && uv sync --locked --extra test
	$(MAKE) -C worker proto
	cd web && npm ci
	cd web && if [ "$$(uname -s)" = Linux ]; then \
		npx playwright install --with-deps chromium; \
	else \
		npx playwright install chromium; \
	fi

test:
	./scripts/verify.sh unit

test-server:
	./scripts/verify.sh server

test-worker:
	./scripts/verify.sh worker

test-web:
	./scripts/verify.sh web

test-recall:
	PYTHONDONTWRITEBYTECODE=1 \
		$(PYTHON) -m unittest discover -s benchmarks/recall/tests -v
	PYTHONDONTWRITEBYTECODE=1 $(PYTHON) -m benchmarks.recall verify

recall-baseline:
	PYTHONDONTWRITEBYTECODE=1 $(PYTHON) -m benchmarks.recall run \
		--output benchmarks/recall/baselines/lexical-reference.v1.json

test-race:
	./scripts/verify.sh race

test-env-up:
	MEM_TEST_PROJECT='$(MEM_TEST_PROJECT)' \
	MEM_TEST_PG_PORT=$(MEM_TEST_PG_PORT) \
		docker compose -p '$(MEM_TEST_PROJECT)' \
			-f docker-compose.test.yml up -d --wait

test-env-down:
	MEM_TEST_PROJECT='$(MEM_TEST_PROJECT)' \
	MEM_TEST_PG_PORT=$(MEM_TEST_PG_PORT) \
		docker compose -p '$(MEM_TEST_PROJECT)' \
			-f docker-compose.test.yml down --volumes --remove-orphans

test-integration:
	MEM_TEST_DB='$(MEM_TEST_DB)' ./scripts/verify.sh integration

test-integration-race:
	MEM_TEST_DB='$(MEM_TEST_DB)' ./scripts/verify.sh integration-race

test-acceptance:
	./scripts/acceptance_agent_memory.sh

test-agent-certification:
	@test -n "$(MEM_MCP_CERT_BINARY)" || { \
		echo "MEM_MCP_CERT_BINARY must name an explicit mem-mcp binary"; \
		exit 2; \
	}
	MEM_MCP_CERT_BINARY='$(MEM_MCP_CERT_BINARY)' \
		python3 -m unittest discover \
			-s scripts/agent_certification -p 'test_*.py' -v

test-deploy:
	./scripts/validate_deploy.sh

test-deploy-build:
	MEM_VALIDATE_BUILD_IMAGES=1 ./scripts/validate_deploy.sh

test-all:
	$(MAKE) test-recall
	MEM_TEST_DB='$(MEM_TEST_DB)' ./scripts/verify.sh all
	./scripts/acceptance_agent_memory.sh

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
