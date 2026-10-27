VERSION ?= go-dev
OPENSPEC ?= npx --yes @fission-ai/openspec@1.12.0
GOLANGCI_LINT ?= golangci-lint
GOVULNCHECK ?= govulncheck

.PHONY: build web-deps web-build web-check test test-web-contract race vet lint vuln spec-validate check release

web-deps:
	npm --prefix web run install:deps

web-build:
	npm --prefix web run build

build: web-build
	CGO_ENABLED=0 go build -trimpath -ldflags '-s -w -X main.version=$(VERSION)' -o bin/codex-lb ./cmd/codex-lb

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...

lint:
	$(GOLANGCI_LINT) run ./...

vuln:
	$(GOVULNCHECK) ./...

spec-validate:
	OPENSPEC_TELEMETRY=0 $(OPENSPEC) validate --specs --no-interactive

# The complete local gate; CI runs the same targets.
check: vet lint test race vuln spec-validate

web-check:
	npm --prefix web run lint
	npm --prefix web run typecheck
	npm --prefix web run test

test-web-contract: build
	CODEX_LB_TEST_BINARY="$(CURDIR)/bin/codex-lb" npm --prefix web run test -- src/__integration__/go-runtime.test.ts

# Builds reproducible Linux archives and SHA256SUMS for VERSION into dist/VERSION.
release:
	scripts/release.sh $(VERSION) dist/$(VERSION)
