.PHONY: build build-all install test test-e2e test-cover test-race test-live vet fmt clean pgo generate-models check-uv lint-python test-python install-uv publish deploy tidy _all_parallel $(CROSS_TARGETS)

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

# ---------------------------------------------------------------------------
# Quiet build helpers — print a short step name, show output only on failure.
# Usage: $(call RUN,label,command)
# Set V=1 for verbose output: make all V=1
# ---------------------------------------------------------------------------
ifdef V
  define RUN
	@printf "  %-28s\n" "$(1)"
	$(2)
  endef
else
  define RUN
	@_log=$$(mktemp) && ( $(2) ) > $$_log 2>&1 \
		&& { printf "  %-28s ✓\n" "$(1)"; rm -f $$_log; } \
		|| { printf "  %-28s ✗\n" "$(1)"; cat $$_log; rm -f $$_log; exit 1; }
  endef
endif

# Stamp file records the Go source-tree hash for which default.pgo was last generated.
# Only regenerate when .go files have changed (or the stamp is missing).
PGO_STAMP := default.pgo.stamp

build: tidy
	@mkdir -p $(BINDIR)
	$(call RUN,build (native),go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY) ./cmd/fir/)

# `make all` runs fmt first, then everything else in parallel via recursive make -j.
# The _all_parallel target declares independent prerequisites that make -j can schedule.
all: fmt tidy
	@$(MAKE) -j --no-print-directory _all_parallel

_all_parallel: test-race build-all lint-python test-python

fmt:
	@gofmt -s -w .

install:
	go install -ldflags="$(LDFLAGS)" ./cmd/fir/

# Ensure modules are tidy once; other targets depend on this.
tidy:
	@go mod tidy

# Cross-compile for all targets (each is an independent phony target for parallelism)
CROSS_TARGETS := build-darwin-arm64 build-darwin-amd64 build-linux-armv6 build-linux-arm64 build-linux-amd64

build-all: $(CROSS_TARGETS) build

build-darwin-arm64: | $(BINDIR)
	$(call RUN,build darwin/arm64,GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY)-darwin-arm64 ./cmd/fir/)

build-darwin-amd64: | $(BINDIR)
	$(call RUN,build darwin/amd64,GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY)-darwin-amd64 ./cmd/fir/)

build-linux-armv6: | $(BINDIR)
	$(call RUN,build linux/armv6,GOOS=linux GOARCH=arm GOARM=6 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY)-linux-armv6 ./cmd/fir/)

build-linux-arm64: | $(BINDIR)
	$(call RUN,build linux/arm64,GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY)-linux-arm64 ./cmd/fir/)

build-linux-amd64: | $(BINDIR)
	$(call RUN,build linux/amd64,GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY)-linux-amd64 ./cmd/fir/)

$(BINDIR):
	@mkdir -p $(BINDIR)

test: tidy
	go test ./...

test-e2e: tidy
	go test -v -count=1 -tags=e2e -timeout 120s ./tests/e2e/

# Build test binaries for end-to-end testing
test-bins: tidy
	@mkdir -p $(BINDIR)
	go test -c -o $(BINDIR)/acp.test ./pkg/modes/acp/
	go test -c -o $(BINDIR)/mcp.test ./pkg/mcp/

# Build fir binary for end-to-end testing
e2e-binary:
	go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINDIR)/fir-e2e ./cmd/fir/

test-cover: tidy
	@mkdir -p $(BINDIR)
	go test -coverprofile=$(BINDIR)/coverage.out ./...
	go tool cover -func=$(BINDIR)/coverage.out

test-race: tidy
	$(call RUN,test (race),go test -race ./...)

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

publish: pgo build
	@if ! git diff --quiet default.pgo default.pgo.stamp 2>/dev/null; then \
		echo "PGO profile updated, amending release commit..."; \
		git add default.pgo default.pgo.stamp && \
		git commit --amend --no-edit && \
		git tag -f -a $(RELEASE_TAG) -m "release: $(RELEASE_TAG)"; \
	fi
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
		Linux-armv6l)                BIN=$(BINARY)-linux-armv6 ;; \
		Linux-armv7l)                BIN=$(BINARY)-linux-armv6 ;; \
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
	$(call RUN,lint python (ruff),uvx ruff check $(PYTHON_DIRS))
	$(call RUN,lint python (ty),uvx ty check $(PYTHON_DIRS))

test-python: check-uv
	$(call RUN,test python (sdk),PYTHONPATH=pkg/extension/sdk/python python3 -m unittest discover -s pkg/extension/sdk/python -p '*_test.py')
	$(call RUN,test python (resources),PYTHONPATH=pkg/extension/sdk/python python3 -m unittest discover -s pkg/resources/testdata -p '*_test.py')
