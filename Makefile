SHELL := /bin/bash
.PHONY: build run test tidy fmt generate

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
