.PHONY: build build-all test test-cover test-race test-live vet clean

# Default target
BINARY := pi-go
LDFLAGS := -s -w

build:
	go build -ldflags="$(LDFLAGS)" -o $(BINARY) ./cmd/pi/

# Cross-compile for all targets
build-all:
	GOOS=darwin GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o $(BINARY)-darwin-arm64 ./cmd/pi/
	GOOS=darwin GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o $(BINARY)-darwin-amd64 ./cmd/pi/
	GOOS=linux GOARCH=arm GOARM=6 go build -ldflags="$(LDFLAGS)" -o $(BINARY)-linux-arm6 ./cmd/pi/
	GOOS=linux GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o $(BINARY)-linux-arm64 ./cmd/pi/
	GOOS=linux GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o $(BINARY)-linux-amd64 ./cmd/pi/

test:
	go test ./...

test-cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

test-race:
	go test -race ./...

test-live:
	PI_TEST_LIVE=1 go test ./pkg/ai/providers/... -run TestLive

vet:
	go vet ./...

clean:
	rm -f $(BINARY) $(BINARY)-* coverage.out
