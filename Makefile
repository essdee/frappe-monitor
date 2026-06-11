SHELL := /bin/bash
.PHONY: build build-no-web web-build web-install run test tidy fmt generate \
        vm-up vm-down vm-logs loki-logs install uninstall local-test

# `make build` runs the frontend build first and then the Go build with
# the embed_dist tag so the SPA bundle ends up in the binary. For Go-
# only iteration without a frontend, `make build-no-web` skips Vite
# and the binary serves a "frontend not built" placeholder page.

build: web-build
	CGO_ENABLED=0 go build -tags=embed_dist -ldflags="-s -w" -o bin/monitor-server ./cmd/monitor

build-no-web:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/monitor-server ./cmd/monitor

web-install:
	cd web && npm install

web-build: web-install
	cd web && npm run build

run:
	go run -tags=embed_dist ./cmd/monitor --config ./config/monitor.yaml

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

# --- production install ---------------------------------------------------
# `make install` runs deploy/install.sh, which auto-detects the OS:
#   Linux  → systemd units + a dedicated frappe-monitor system user under
#            /etc + /var/lib + /opt (needs root, so we prepend sudo).
#   macOS  → a launchd LaunchAgent under ~/.frappe-monitor (NO sudo — the
#            script refuses to run as root there).
# Either way it builds the binary, brings up VM + Loki via the prod
# compose, writes config (auto-generating a dashboard password if you
# don't pass one), starts the service, and health-checks it. Idempotent —
# safe to re-run for upgrades. See docs/guide/deployment.md.

install:
	@if [ "$$(uname -s)" = "Darwin" ]; then deploy/install.sh; else sudo deploy/install.sh; fi

uninstall:
	@if [ "$$(uname -s)" = "Darwin" ]; then deploy/install.sh --uninstall; else sudo deploy/install.sh --uninstall; fi

# `make local-test` brings up the whole stack on a laptop with seeded
# demo data so every page has something to show. No sudo, no systemd,
# no real bench server required. Ctrl+C to stop and tear down. See
# docs/guide/local-test.md for the full walkthrough.
local-test:
	deploy/local-test.sh
