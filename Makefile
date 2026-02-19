.PHONY: build build-all install test test-cover test-race test-live vet clean pgo generate-models

# Output directory for all build artifacts
BINDIR  := bin
BINARY  := $(BINDIR)/tau
BINARY_PGO  := $(BINDIR)/tau.pgo
LDFLAGS := -s -w

build:
	@mkdir -p $(BINDIR)
	go build -ldflags="$(LDFLAGS)" -o $(BINARY) ./cmd/tau/

all: test-race pgo build-all

install:
	go install -ldflags="$(LDFLAGS)" ./cmd/tau/

# Cross-compile for all targets
build-all:
	@mkdir -p $(BINDIR)
	GOOS=darwin GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o $(BINARY)-darwin-arm64 ./cmd/tau/
	GOOS=darwin GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o $(BINARY)-darwin-amd64 ./cmd/tau/
	GOOS=linux GOARCH=arm GOARM=6 go build -ldflags="$(LDFLAGS)" -o $(BINARY)-linux-arm6 ./cmd/tau/
	GOOS=linux GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o $(BINARY)-linux-arm64 ./cmd/tau/
	GOOS=linux GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o $(BINARY)-linux-amd64 ./cmd/tau/

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
	go test -cpuprofile=default.pgo -o $(BINARY_PGO) ./cmd/tau/
	rm $(BINARY_PGO)
	@make build

generate-models:
	@mkdir -p $(BINDIR)
	go build -o $(BINDIR)/generate-models ./cmd/generate-models/
	$(BINDIR)/generate-models

clean:
	rm -rf $(BINDIR)
