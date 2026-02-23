.PHONY: build build-all install test test-cover test-race test-live vet clean pgo generate-models

# Output directory for all build artifacts
BINDIR    := bin
BINARY    := $(BINDIR)/fir
BINARY_PGO := $(BINDIR)/fir.pgo
VERSION   := $(shell cat VERSION 2>/dev/null || echo dev)
LDFLAGS   := -s -w -X main.version=$(VERSION)

# Stamp file records the Go source-tree hash for which default.pgo was last generated.
# Only regenerate when .go files have changed (or the stamp is missing).
PGO_STAMP := default.pgo.stamp

build:
	@mkdir -p $(BINDIR)
	@cp CHANGELOG.md cmd/fir/CHANGELOG.md
	go build -ldflags="$(LDFLAGS)" -o $(BINARY) ./cmd/fir/

all: test-race pgo build build-all

install:
	@cp CHANGELOG.md cmd/fir/CHANGELOG.md
	go install -ldflags="$(LDFLAGS)" ./cmd/fir/

# Cross-compile for all targets
build-all:
	@mkdir -p $(BINDIR)
	@cp CHANGELOG.md cmd/fir/CHANGELOG.md
	GOOS=darwin GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o $(BINARY)-darwin-arm64 ./cmd/fir/
	GOOS=darwin GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o $(BINARY)-darwin-amd64 ./cmd/fir/
	GOOS=linux GOARCH=arm GOARM=6 go build -ldflags="$(LDFLAGS)" -o $(BINARY)-linux-arm6 ./cmd/fir/
	GOOS=linux GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o $(BINARY)-linux-arm64 ./cmd/fir/
	GOOS=linux GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o $(BINARY)-linux-amd64 ./cmd/fir/

test:
	go test ./...

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
