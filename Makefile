.PHONY: build build-all install test test-e2e test-cover test-race test-live vet clean pgo generate-models check-uv lint-python test-python install-uv publish deploy

# Output directory for all build artifacts
BINDIR    := bin
BINARY    := $(BINDIR)/fir
BINARY_PGO := $(BINDIR)/fir.pgo
VERSION   := $(shell cat VERSION 2>/dev/null || echo dev)

# Compute a rich version: if HEAD is the exact release tag, use VERSION as-is.
# Otherwise append -dev+<commit>[.dirty] so non-release builds are obvious.
GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null)
GIT_TAG    := $(shell git describe --exact-match --tags HEAD 2>/dev/null)
GIT_DIRTY  := $(shell git diff --quiet 2>/dev/null || echo .dirty)
ifneq ($(GIT_TAG),v$(VERSION))
  ifneq ($(GIT_COMMIT),)
    VERSION := $(VERSION)-dev+$(GIT_COMMIT)$(GIT_DIRTY)
  endif
endif

LDFLAGS   := -s -w -X main.version=$(VERSION)

# Stamp file records the Go source-tree hash for which default.pgo was last generated.
# Only regenerate when .go files have changed (or the stamp is missing).
PGO_STAMP := default.pgo.stamp

build:
	@mkdir -p $(BINDIR)
	go mod tidy
	go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY) ./cmd/fir/

all: test-race build build-all lint-python test-python

install:
	go install -ldflags="$(LDFLAGS)" ./cmd/fir/

# Cross-compile for all targets
build-all: build
	GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY)-darwin-arm64 ./cmd/fir/
	GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY)-darwin-amd64 ./cmd/fir/
	GOOS=linux GOARCH=arm GOARM=6 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY)-linux-arm6 ./cmd/fir/
	GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY)-linux-arm64 ./cmd/fir/
	GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY)-linux-amd64 ./cmd/fir/

test:
	go test ./...

test-e2e:
	go test -v -count=1 -tags=e2e -timeout 120s ./tests/e2e/

# Build test binaries for end-to-end testing
test-bins:
	@mkdir -p $(BINDIR)
	go test -c -o $(BINDIR)/acp.test ./pkg/modes/acp/
	go test -c -o $(BINDIR)/mcp.test ./pkg/mcp/

# Build fir binary for end-to-end testing
e2e-binary:
	go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINDIR)/fir-e2e ./cmd/fir/

test-cover:
	@mkdir -p $(BINDIR)
	go test -coverprofile=$(BINDIR)/coverage.out ./...
	go tool cover -func=$(BINDIR)/coverage.out

test-race:
	go test -race ./...

vet:
	go vet ./...

pgo:
	@mkdir -p $(BINDIR)
	@SOURCE_HASH=$$(git ls-tree -r HEAD 2>/dev/null | grep '\.go$$' | git hash-object --stdin 2>/dev/null || echo "no-git"); \
	if [ -f $(PGO_STAMP) ] && [ "$$(cat $(PGO_STAMP))" = "$$SOURCE_HASH" ]; then \
		echo "PGO profile up to date (source hash $$SOURCE_HASH), skipping regeneration."; \
	else \
		echo "Generating PGO profile (source hash $$SOURCE_HASH)..."; \
		go test -cpuprofile=default.pgo -o $(BINARY_PGO) ./cmd/fir/ && \
		rm -f $(BINARY_PGO) && \
		echo "$$SOURCE_HASH" > $(PGO_STAMP) && \
		$(MAKE) build; \
	fi

generate-models:
	@mkdir -p $(BINDIR)
	go build -o $(BINDIR)/generate-models ./cmd/generate-models/
	$(BINDIR)/generate-models

clean:
	rm -rf $(BINDIR)

# ---------------------------------------------------------------------------
# Release publishing & remote deployment
# ---------------------------------------------------------------------------

RELEASE_TAG := v$(shell cat VERSION 2>/dev/null || echo 0.0.0)

publish: build
	@echo "Publishing $(RELEASE_TAG)..."
	git push origin main $(RELEASE_TAG)
	@echo "Pushed $(RELEASE_TAG). GoReleaser CI will build and upload assets."

# Deploy to a remote host via scp (auto-detects OS and arch)
# Usage: make deploy HOST=myhost
deploy: build-all
	@if [ -z "$(HOST)" ]; then echo "Usage: make deploy HOST=<hostname>"; exit 1; fi
	@INFO=$$(ssh -o ConnectTimeout=5 $(HOST) "uname -s -m") || { echo "Cannot reach $(HOST)"; exit 1; }; \
	OS=$$(echo "$$INFO" | awk '{print $$1}'); \
	ARCH=$$(echo "$$INFO" | awk '{print $$2}'); \
	case "$$OS-$$ARCH" in \
		Linux-aarch64|Linux-arm64)   BIN=$(BINARY)-linux-arm64 ;; \
		Linux-armv6l)                BIN=$(BINARY)-linux-arm6 ;; \
		Linux-armv7l)                BIN=$(BINARY)-linux-arm6 ;; \
		Linux-x86_64)                BIN=$(BINARY)-linux-amd64 ;; \
		Darwin-arm64)                BIN=$(BINARY)-darwin-arm64 ;; \
		Darwin-x86_64)               BIN=$(BINARY)-darwin-amd64 ;; \
		*) echo "Unsupported platform: $$OS $$ARCH"; exit 1 ;; \
	esac; \
	echo "Deploying to $(HOST) ($$OS/$$ARCH → $$BIN)..."; \
	scp -q $$BIN $(HOST):~/.local/bin/fir && \
	ssh $(HOST) "chmod +x ~/.local/bin/fir && ~/.local/bin/fir --version"

# ---------------------------------------------------------------------------
# Python SDK & extensions
# ---------------------------------------------------------------------------

PYTHON_DIRS := pkg/extension/sdk/python .fir/extensions

install-uv:
	@echo "Installing uv via Astral installer..."
	curl -LsSf https://astral.sh/uv/install.sh | sh

check-uv:
	@command -v uv >/dev/null 2>&1 || { echo "uv not found. Run 'make install-uv' first."; exit 1; }

lint-python: check-uv
	uvx ruff check $(PYTHON_DIRS)
	uvx ty check $(PYTHON_DIRS)

test-python: check-uv
	PYTHONPATH=pkg/extension/sdk/python python3 -m unittest discover -s pkg/extension/sdk/python -p '*_test.py' -v
	PYTHONPATH=pkg/extension/sdk/python python3 -m unittest discover -s pkg/resources/testdata -p '*_test.py' -v
