VERSION ?= go-dev

.PHONY: build web-deps web-build test test-web-contract race vet

web-deps:
	npm --prefix web run install:deps

web-build:
	npm --prefix web run build

build: web-build
	CGO_ENABLED=0 go build -trimpath -ldflags '-s -w -X main.version=$(VERSION)' -o bin/codex-lb ./cmd/codex-lb

test:
	go test ./...

test-web-contract: build
	CODEX_LB_TEST_BINARY="$(CURDIR)/bin/codex-lb" npm --prefix web run test -- src/__integration__/go-runtime.test.ts

race:
	go test -race ./...

vet:
	go vet ./...
