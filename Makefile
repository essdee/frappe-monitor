SHELL := /bin/bash
.PHONY: build run test tidy fmt generate vm-up vm-down vm-logs loki-logs

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

# --- dev backends (Phase 2 + Phase 3) -------------------------------------
# `make vm-up` brings up the full dev stack: VictoriaMetrics on
# 127.0.0.1:8428 and Loki on 127.0.0.1:3100. The monitor binary (run via
# `make run`) pushes to them via the configured cfg.metrics.vm_url and
# cfg.logs.loki_url defaults.
#
# Volumes (frappe-monitor-vm-data, frappe-monitor-loki-data) persist
# across down/up cycles. To wipe history: `docker volume rm <name>`.

vm-up:
	docker compose -f deploy/docker-compose.dev.yml up -d

vm-down:
	docker compose -f deploy/docker-compose.dev.yml down

vm-logs:
	docker compose -f deploy/docker-compose.dev.yml logs -f victoriametrics

loki-logs:
	docker compose -f deploy/docker-compose.dev.yml logs -f loki
