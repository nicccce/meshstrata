SHELL := /bin/bash
.SHELLFLAGS := -e -o pipefail -c
.DEFAULT_GOAL := build

export GOMAXPROCS ?= 2
export CGO_ENABLED ?= 0
export GOCACHE ?= $(CURDIR)/.local/cache/go
export GOTMPDIR ?= $(CURDIR)/.local/cache/go-tmp
export npm_config_cache ?= $(CURDIR)/.local/cache/npm
GO_PARALLEL ?= 2
VERSION ?= dev
COMMIT := $(shell git rev-parse --short HEAD)
BUILD_TIME := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
BUILD_PACKAGE := github.com/nicccce/meshstrata/internal/buildinfo
LDFLAGS := -X $(BUILD_PACKAGE).Version=$(VERSION) -X $(BUILD_PACKAGE).Commit=$(COMMIT) -X $(BUILD_PACKAGE).BuildTime=$(BUILD_TIME)

.PHONY: build web-build web-install generate check check-generated test smoke dev-controller dev-agent dev-web go-prepare
.NOTPARALLEL: check

go-prepare:
	mkdir -p "$(GOTMPDIR)"

web-install: controller/web/node_modules/.meshstrata-installed

controller/web/node_modules/.meshstrata-installed: controller/web/package.json controller/web/package-lock.json
	npm --prefix controller/web ci --no-audit --no-fund
	touch $@

web-build: web-install
	find controller/internal/webui/dist -mindepth 1 ! -name .gitkeep -delete
	npm --prefix controller/web run build

build: web-build go-prepare
	mkdir -p bin
	CGO_ENABLED=0 go build -p $(GO_PARALLEL) -trimpath -ldflags '$(LDFLAGS)' -o bin/meshstrata-controller ./controller/cmd
	CGO_ENABLED=0 go build -p $(GO_PARALLEL) -trimpath -ldflags '$(LDFLAGS)' -o bin/meshstrata-agent ./agent/cmd

generate:
	sqlc generate

check-generated:
	python3 scripts/check_generated.py

test: go-prepare
	go test -p $(GO_PARALLEL) ./...
	go vet -p $(GO_PARALLEL) ./...

check: check-generated test build
	python3 scripts/smoke.py

smoke: build
	python3 scripts/smoke.py

dev-controller: web-build go-prepare
	go run -p $(GO_PARALLEL) ./controller/cmd run -config controller/configs/controller.dev.json

dev-agent: go-prepare
	go run -p $(GO_PARALLEL) ./agent/cmd run -config agent/configs/agent.dev.json

dev-web: web-install
	npm --prefix controller/web run dev
