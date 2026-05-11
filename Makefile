# mem — Phase 1 MVP Makefile

.PHONY: help up down logs reset proto server worker web test fmt lint

help:
	@echo "mem dev commands:"
	@echo "  make up          - 启动 postgres/redis/minio (docker compose)"
	@echo "  make down        - 停止所有依赖"
	@echo "  make logs        - 跟踪 docker logs"
	@echo "  make reset       - ⚠️ 删除所有 volume 数据后重启"
	@echo "  make proto       - 编译 .proto -> Go/Python stubs"
	@echo "  make server      - 启动 Go 服务 (memd)"
	@echo "  make worker      - 启动 Python AI worker"
	@echo "  make web         - 启动 React 前端 (vite dev)"
	@echo "  make test        - 跑所有测试"

up:
	docker compose up -d

down:
	docker compose down

logs:
	docker compose logs -f --tail=100

reset:
	docker compose down -v
	docker compose up -d

proto:
	@echo "TODO: 在 W1 配置 buf / protoc 编译规则"

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
	cd worker && pytest

fmt:
	cd server && gofmt -w .
	cd worker && ruff format .
	cd web && npm run format

lint:
	cd server && go vet ./...
	cd worker && ruff check .
	cd web && npm run lint
