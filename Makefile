.PHONY: build build-all install install-completions test test-e2e test-cover test-race test-live vet fmt clean pgo generate-models check-uv lint-python test-python test-python-sdk test-python-ext test-python-schedule test-python-tmuxspinner install-uv publish deploy tidy check-size notices check-licenses _all_parallel $(CROSS_TARGETS)

# Output directory for all build artifacts
BINDIR    := bin
BINARY    := $(BINDIR)/fir

# Go binary install path (for tmux commands that need full paths)
GOBIN     := $(shell go env GOPATH)/bin
BINARY_PGO := $(BINDIR)/fir.pgo
BINARY_MAX_SIZE := 20971520
BINARY_SIZE_BASELINE := $(shell cat BINARY_SIZE_BASELINE 2>/dev/null || echo 0)
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

LDFLAGS   := -s -w -X main.version=$(VERSION) -X main.licensesURL=https://github.com/kfet/fir-dist/releases/tag/v$(shell cat VERSION 2>/dev/null || echo 0.0.0)

# No cgo dependencies: build a static, pure-Go binary. Avoids invoking the C
# toolchain, which noticeably speeds up builds on hosts with a slow C compiler
# (e.g. Raspberry Pi) and removes the need for a C compiler at all.
export CGO_ENABLED := 0

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
	@$(MAKE) --no-print-directory check-size

# `make all` runs fmt first, then everything else in parallel via recursive make -j.
# TIDY_DONE=1 tells sub-targets to skip redundant go-mod-tidy (already ran here).
all: fmt tidy
	@$(MAKE) -j --no-print-directory _all_parallel TIDY_DONE=1

_all_parallel: vet test-race build-all lint-python test-python-sdk test-python-ext test-python-schedule test-python-tmuxspinner check-licenses

fmt:
	@gofmt -s -w .

install:
	go install -ldflags="$(LDFLAGS)" ./cmd/fir/
	@$(MAKE) --no-print-directory install-completions

# install-completions drops shell completion files into common locations.
# Falls back to per-user paths when system dirs aren't writable.
# PREFIX defaults to /usr/local. Override with `make install-completions PREFIX=...`.
PREFIX ?= /usr/local
install-completions:
	@bash_dir="$(PREFIX)/etc/bash_completion.d"; \
	zsh_dir="$(PREFIX)/share/zsh/site-functions"; \
	if [ ! -w "$(PREFIX)" ] && [ ! -w "$$bash_dir" ]; then \
		bash_dir="$$HOME/.local/share/bash-completion/completions"; \
		zsh_dir="$$HOME/.local/share/zsh/site-functions"; \
	fi; \
	mkdir -p "$$bash_dir" "$$zsh_dir" 2>/dev/null || true; \
	if [ -w "$$bash_dir" ]; then \
		cp cmd/fir/completions/fir.bash "$$bash_dir/fir" && \
		printf "  %-28s ✓ (bash → %s)\n" "install-completions" "$$bash_dir/fir"; \
	else \
		printf "  %-28s skip (no write access to %s)\n" "install-completions" "$$bash_dir"; \
	fi; \
	if [ -w "$$zsh_dir" ]; then \
		cp cmd/fir/completions/_fir "$$zsh_dir/_fir" && \
		printf "  %-28s ✓ (zsh  → %s)\n" "install-completions" "$$zsh_dir/_fir"; \
	else \
		printf "  %-28s skip (no write access to %s)\n" "install-completions" "$$zsh_dir"; \
	fi

# Binary size guard — runs after every native build.
# Fails on: absolute cap (20 MB) exceeded OR >5% growth vs BINARY_SIZE_BASELINE.
check-size:
	@size=$$(stat -f%z $(BINARY) 2>/dev/null || stat -c%s $(BINARY)); \
	baseline=$(BINARY_SIZE_BASELINE); \
	max=$(BINARY_MAX_SIZE); \
	mb=$$((size / 1048576)); \
	if [ "$$size" -gt "$$max" ]; then \
		printf "  %-28s FAIL ($$mb MB exceeds %d MB cap)\n" "check-size" $$((max / 1048576)); exit 1; \
	fi; \
	if [ "$$baseline" -gt 0 ]; then \
		pct=$$(( (size - baseline) * 100 / baseline )); \
		if [ "$$pct" -gt 5 ]; then \
			printf "  %-28s FAIL ($$mb MB, +$$pct%% vs baseline — update BINARY_SIZE_BASELINE if intended)\n" "check-size"; exit 1; \
		fi; \
	fi

# Ensure modules are tidy once; other targets depend on this.
# Skipped when TIDY_DONE=1 (set by `make all` after running tidy upfront).
tidy:
ifndef TIDY_DONE
	@go mod tidy
endif

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

test-cover: tidy
	@mkdir -p $(BINDIR)
	go test -coverprofile=$(BINDIR)/coverage.out ./...
	go tool cover -func=$(BINDIR)/coverage.out

test-race: tidy
	$(call RUN,test (race),go test -race ./...)

vet:
	$(call RUN,vet,go vet ./...)

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
	rm -f THIRD_PARTY_NOTICES.md

# ---------------------------------------------------------------------------
# Third-party license notices
#
# `make notices` generates THIRD_PARTY_NOTICES.md from go.mod / go.sum via
# go-licenses. The resulting file is uploaded as a release asset alongside
# the binaries so users (and packagers) can locate the attribution text.
#
# `make check-licenses` fails the build if any dependency is under a license
# we do not allow (GPL, AGPL, etc.). Runs in CI and before release.
# ---------------------------------------------------------------------------

GO_LICENSES := go run github.com/google/go-licenses@v1.6.0

notices: THIRD_PARTY_NOTICES.md

THIRD_PARTY_NOTICES.md: go.mod go.sum hack/notices.tpl
	$(call RUN,generate notices,$(GO_LICENSES) report ./cmd/fir --template hack/notices.tpl > $@ 2>/dev/null)

check-licenses:
	$(call RUN,check licenses,$(GO_LICENSES) check ./cmd/fir --disallowed_types=forbidden,restricted 2>/dev/null)


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
# Python 3.9 minimum — macOS ships with 3.9 and we want out-of-the-box
# compatibility on a fresh install. See AGENTS.md and pyproject.toml.
# ---------------------------------------------------------------------------

PYTHON_DIRS := pkg/extension/sdk/python pkg/resources/testdata .fir/extensions

install-uv:
	@echo "Installing uv via Astral installer..."
	curl -LsSf https://astral.sh/uv/install.sh | sh

check-uv:
	@command -v uv >/dev/null 2>&1 || { echo "uv not found. Run 'make install-uv' first."; exit 1; }

lint-python: check-uv
	$(call RUN,lint python (ruff),uvx ruff check $(PYTHON_DIRS))
	$(call RUN,lint python (ty),uvx ty check $(PYTHON_DIRS))

test-python: test-python-sdk test-python-ext test-python-schedule test-python-tmuxspinner

test-python-sdk: check-uv
	$(call RUN,test python (sdk),PYTHONPATH=pkg/extension/sdk/python python3 -m unittest discover -s pkg/extension/sdk/python -p '*_test.py')

# Fast extension tests (bundled) — everything except the slow ones.
_SLOW_EXT_TESTS := schedule_test.py tmuxspinner_test.py
_ALL_EXT_TESTS  := $(notdir $(wildcard pkg/resources/testdata/*_test.py))
_FAST_EXT_TESTS := $(filter-out $(_SLOW_EXT_TESTS),$(_ALL_EXT_TESTS))
test-python-ext: check-uv
	$(call RUN,test python (extensions),cd pkg/resources/testdata && PYTHONPATH=../../../pkg/extension/sdk/python python3 -m unittest $(_FAST_EXT_TESTS))

# Slow extension tests (separate targets so make -j can parallelise them).
test-python-schedule: check-uv
	$(call RUN,test python (schedule),PYTHONPATH=pkg/extension/sdk/python python3 -m unittest discover -s pkg/resources/testdata -p 'schedule_test.py')

test-python-tmuxspinner: check-uv
	$(call RUN,test python (tmuxspinner),PYTHONPATH=pkg/extension/sdk/python python3 -m unittest discover -s pkg/resources/testdata -p 'tmuxspinner_test.py')

# ---------------------------------------------------------------------------
# External bridges (opt-in, not part of default `all`)
# Each bridge has its own go.mod and Makefile under external/.
# `make bridges` builds all of them. `make bridges-test` tests all.
# ---------------------------------------------------------------------------

BRIDGE_DIRS := external/poe

.PHONY: bridges bridges-test bridges-all bridges-install

bridges: ## build all external bridges (opt-in)
	@for d in $(BRIDGE_DIRS); do \
		printf "  %-28s" "bridge: $$d"; \
		$(MAKE) -C $$d build BINDIR=$(abspath $(BINDIR)) --no-print-directory && printf " ✓\n" || { printf " ✗\n"; exit 1; }; \
	done

bridges-test: ## test all external bridges
	@for d in $(BRIDGE_DIRS); do \
		printf "  %-28s" "bridge-test: $$d"; \
		$(MAKE) -C $$d test-race --no-print-directory && printf " ✓\n" || { printf " ✗\n"; exit 1; }; \
	done

bridges-all: ## full pipeline for all external bridges
	@for d in $(BRIDGE_DIRS); do \
		printf "  bridge-all: $$d\n"; \
		$(MAKE) -C $$d all BINDIR=$(abspath $(BINDIR)) --no-print-directory || exit 1; \
	done

bridges-install: bridges ## install all external bridges to GOBIN
	@for d in $(BRIDGE_DIRS); do \
		printf "  %-28s" "bridge-install: $$d"; \
		$(MAKE) -C $$d install BINDIR=$(abspath $(BINDIR)) --no-print-directory && printf " ✓\n" || { printf " ✗\n"; exit 1; }; \
	done

# ---------------------------------------------------------------------------
# Poe deploy: test, install, and rolling-restart relay + all agents.
#
# Everything runs in one tmux session "poe":
#   relay      — poe-bridge --relay
#   catch-all  — fir (claims new conversations, spawns agents)
#   <conv-id>  — dedicated agent per conversation
#
# All windows use remain-on-exit. Deploy uses respawn-window -k to kill
# the old process and re-run the original command with the new binary.
#
# Usage: make poe-deploy        # test + install + restart all
#        make poe-start          # initial setup (create session)
# ---------------------------------------------------------------------------

POE_SESSION    := poe
POE_RELAY_DIR  := $(HOME)/.local/state/fir/poe/relay
POE_CATCHALL   := $(HOME)/.local/state/fir/agents/catch-all

.PHONY: poe-deploy poe-start

poe-start: install bridges-install ## create the poe tmux session with relay + catch-all
	@mkdir -p $(POE_RELAY_DIR) $(POE_CATCHALL)/.fir
	@test -f $(POE_CATCHALL)/.fir/mcp.json || printf '{"mcpServers":{"poe":{"command":"$(GOBIN)/poe-bridge","args":["--agent","ws://localhost:9090/ws"],"env":{"POE_BOT_NAME":"fir-air"}}}}\n' > $(POE_CATCHALL)/.fir/mcp.json
	@if tmux has-session -t $(POE_SESSION) 2>/dev/null; then \
		echo "session '$(POE_SESSION)' already exists"; \
	else \
		echo "creating tmux session '$(POE_SESSION)'..."; \
		tmux new-session -d -s $(POE_SESSION) -n relay \
			"cd $(POE_RELAY_DIR) && exec $(GOBIN)/poe-bridge --relay"; \
		tmux set-option -t $(POE_SESSION):relay remain-on-exit on; \
		tmux new-window -t $(POE_SESSION) -n catch-all \
			"cd $(POE_CATCHALL) && exec $(GOBIN)/fir --session-name catch-all"; \
		tmux set-option -t $(POE_SESSION):catch-all remain-on-exit on; \
		echo "poe session ready ✓"; \
	fi

poe-deploy: test bridges-test install bridges-install ## deploy poe: test → install → respawn all
	@echo ""
	@if ! tmux has-session -t $(POE_SESSION) 2>/dev/null; then \
		echo "no '$(POE_SESSION)' session — run 'make poe-start' first"; \
		exit 1; \
	fi
	@echo "=== Poe deploy: respawning relay ==="
	@tmux respawn-window -k -t $(POE_SESSION):relay 2>/dev/null && \
		echo "  relay ✓" || echo "  relay: window not found (run poe-start)"
	@sleep 2
	@echo "=== Poe deploy: respawning agents ==="
	@tmux list-windows -t $(POE_SESSION) -F '#{window_index} #{window_name}' 2>/dev/null | \
	while read idx name; do \
		case "$$idx" in 0) continue ;; esac; \
		pid=$$(tmux display-message -t "$(POE_SESSION):$$idx" -p '#{pane_pid}' 2>/dev/null); \
		if [ -n "$$pid" ] && [ "$$pid" != "-1" ]; then \
			for cpid in $$(pgrep -P "$$pid" 2>/dev/null); do kill "$$cpid" 2>/dev/null; done; \
			kill "$$pid" 2>/dev/null; \
			sleep 0.3; \
		fi; \
		tmux respawn-window -k -t "$(POE_SESSION):$$idx" 2>/dev/null && \
			echo "  $$name ✓" || echo "  $$name: respawn failed"; \
	done
	@echo ""
	@echo "Deploy complete. All windows respawned with new binaries."
