.PHONY: build generate test test-unit test-integration test-e2e

VERSION ?= $(shell ./scripts/resolve-version.sh)
LDFLAGS ?= -s -w -X main.Version=$(VERSION)
CRS_GO_RUN = python3 ./scripts/crs_go.py

build:
	CGO_ENABLED=0 $(CRS_GO_RUN) build -ldflags="$(LDFLAGS)" -trimpath -o bin/server ./cmd/server

generate:
	go generate ./ent
	go generate ./cmd/server

test:
	$(CRS_GO_RUN) test ./...
	golangci-lint run ./...

test-unit:
	$(CRS_GO_RUN) test -tags=unit ./...

test-integration:
	$(CRS_GO_RUN) test -tags=integration ./...

test-e2e:
	./scripts/e2e-test.sh

test-e2e-local:
	$(CRS_GO_RUN) test -tags=e2e -v -timeout=300s ./internal/integration/...
