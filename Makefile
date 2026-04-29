SHELL := /bin/bash
.PHONY: build run test tidy fmt generate vm-up vm-down vm-logs

build:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/monitor-server ./cmd/monitor

run:
	go run ./cmd/monitor --config ./config/monitor.yaml

test:
	go test ./... -race -count=1

tidy:
	go mod tidy

fmt:
	go fmt ./...

generate:
	go generate ./...

# --- dev VictoriaMetrics (Phase 2) ----------------------------------------
# `make vm-up` brings up VictoriaMetrics on 127.0.0.1:8428 in a Docker
# container. The monitor binary (run via `make run`) pushes to it via
# the default cfg.metrics.vm_url=http://127.0.0.1:8428.

vm-up:
	docker compose -f deploy/docker-compose.dev.yml up -d

vm-down:
	docker compose -f deploy/docker-compose.dev.yml down

vm-logs:
	docker compose -f deploy/docker-compose.dev.yml logs -f victoriametrics
