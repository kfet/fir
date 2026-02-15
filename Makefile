.PHONY: build build-all install test test-cover test-race test-live vet clean pgo

# Output directory for all build artifacts
BINDIR  := bin
BINARY  := $(BINDIR)/tau
LDFLAGS := -s -w

build:
	@mkdir -p $(BINDIR)
	go build -ldflags="$(LDFLAGS)" -o $(BINARY) ./cmd/tau/

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

test-live:
	TAU_TEST_LIVE=1 go test ./pkg/ai/providers/... -run TestLive

vet:
	go vet ./...

pgo:
	@mkdir -p $(BINDIR)
	go test -cpuprofile=$(BINDIR)/default.pgo ./cmd/tau/
	go build -pgo=$(BINDIR)/default.pgo -ldflags="$(LDFLAGS)" -o $(BINARY) ./cmd/tau/

clean:
	rm -rf $(BINDIR)
