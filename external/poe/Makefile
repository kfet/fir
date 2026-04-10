# external/poe — poe channel bridge
#
# Conventions aligned with fir's root Makefile: same bin dir, same
# cross-compile targets, same quiet-output style, same vet/fmt/test flow.
# `make all` is the full pipeline. `make` alone = default (fmt + vet + test + build).

# Use fir's shared bin dir (two levels up from external/poe)
ROOT_BINDIR := ../../bin
BINDIR      := $(ROOT_BINDIR)
BIN         := poe-bridge
CMD_DIR     := ./cmd/poe-bridge
COVER_OUT   := coverage.out
COVER_HTML  := coverage.html
PKGS        := ./...

GO      ?= go
GOFLAGS ?=

VERSION := $(shell grep 'version ' cmd/poe-bridge/main.go | head -1 | sed 's/.*"\(.*\)"/\1/')
LDFLAGS := -s -w -X main.version=$(VERSION)

# Quiet build helpers (same pattern as fir Makefile)
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

.PHONY: all default build build-all run test test-race cover cover-html vet fmt fmt-check tidy clean help $(CROSS_TARGETS)

default: fmt-check vet test build ## fmt-check + vet + test + build (default)

all: fmt-check vet test-race cover build-all ## full pipeline: fmt-check + vet + race tests + coverage + cross-compile

help: ## show this help
	@awk 'BEGIN{FS=":.*##"; printf "targets:\n"} /^[a-zA-Z_-]+:.*##/{printf "  %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# --- build targets (matching fir's cross-compile matrix) ---

build: | $(BINDIR) ## build native binary
	$(call RUN,build (native),$(GO) build $(GOFLAGS) -trimpath -ldflags="$(LDFLAGS)" -o $(BINDIR)/$(BIN) $(CMD_DIR))

CROSS_TARGETS := build-darwin-arm64 build-darwin-amd64 build-linux-armv6 build-linux-arm64 build-linux-amd64

build-all: $(CROSS_TARGETS) build ## cross-compile for all targets

build-darwin-arm64: | $(BINDIR)
	$(call RUN,build darwin/arm64,GOOS=darwin GOARCH=arm64 $(GO) build $(GOFLAGS) -trimpath -ldflags="$(LDFLAGS)" -o $(BINDIR)/$(BIN)-darwin-arm64 $(CMD_DIR))

build-darwin-amd64: | $(BINDIR)
	$(call RUN,build darwin/amd64,GOOS=darwin GOARCH=amd64 $(GO) build $(GOFLAGS) -trimpath -ldflags="$(LDFLAGS)" -o $(BINDIR)/$(BIN)-darwin-amd64 $(CMD_DIR))

build-linux-armv6: | $(BINDIR)
	$(call RUN,build linux/armv6,GOOS=linux GOARCH=arm GOARM=6 $(GO) build $(GOFLAGS) -trimpath -ldflags="$(LDFLAGS)" -o $(BINDIR)/$(BIN)-linux-armv6 $(CMD_DIR))

build-linux-arm64: | $(BINDIR)
	$(call RUN,build linux/arm64,GOOS=linux GOARCH=arm64 $(GO) build $(GOFLAGS) -trimpath -ldflags="$(LDFLAGS)" -o $(BINDIR)/$(BIN)-linux-arm64 $(CMD_DIR))

build-linux-amd64: | $(BINDIR)
	$(call RUN,build linux/amd64,GOOS=linux GOARCH=amd64 $(GO) build $(GOFLAGS) -trimpath -ldflags="$(LDFLAGS)" -o $(BINDIR)/$(BIN)-linux-amd64 $(CMD_DIR))

$(BINDIR):
	@mkdir -p $(BINDIR)

# --- test targets ---

run: build ## build and run
	$(BINDIR)/$(BIN)

test: ## run unit tests
	$(call RUN,test,$(GO) test $(GOFLAGS) $(PKGS))

test-race: ## run unit tests with -race
	$(call RUN,test (race),$(GO) test $(GOFLAGS) -race $(PKGS))

cover: ## run tests with coverage and print summary
	@$(GO) test $(GOFLAGS) -covermode=atomic -coverprofile=$(COVER_OUT) $(PKGS)
	@$(GO) tool cover -func=$(COVER_OUT) | tail -n 20
	@total=$$($(GO) tool cover -func=$(COVER_OUT) | awk '/^total:/ {print $$3}'); \
		echo "total coverage: $$total"

cover-html: cover ## generate an HTML coverage report
	$(GO) tool cover -html=$(COVER_OUT) -o $(COVER_HTML)
	@echo "wrote $(COVER_HTML)"

# --- lint targets ---

vet: ## run go vet
	$(call RUN,vet,$(GO) vet $(PKGS))

fmt: ## format sources
	@gofmt -s -w .

fmt-check: ## fail if sources are not gofmt-clean
	@diff=$$(gofmt -l .); \
		if [ -n "$$diff" ]; then \
			echo "gofmt issues in:"; echo "$$diff"; exit 1; \
		fi

tidy: ## go mod tidy
	$(GO) mod tidy

clean: ## remove build + coverage artifacts
	rm -f $(BINDIR)/$(BIN) $(BINDIR)/$(BIN)-*
	rm -f $(COVER_OUT) $(COVER_HTML)
