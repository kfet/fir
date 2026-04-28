#!/bin/sh
# install.sh — download and install fir for the current platform.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/kfet/fir/main/install.sh | sh
#
# Binaries are distributed from the public kfet/fir-dist repo, so no
# authentication is required.
#
# Options (environment variables):
#   INSTALL_DIR  — where to install (default: /usr/local/bin, or ~/.local/bin if no write access)
#   VERSION      — specific version to install (default: latest)

set -e

REPO="kfet/fir-dist"
BINARY="fir"

# --------------------------------------------------------------------------
# Detect platform
# --------------------------------------------------------------------------

detect_platform() {
    OS=$(uname -s | tr '[:upper:]' '[:lower:]')
    ARCH=$(uname -m)

    case "$OS" in
        darwin) ;;
        linux)  ;;
        *)
            echo "Error: unsupported OS: $OS" >&2
            echo "  Build from source: go install github.com/$REPO/cmd/fir@latest" >&2
            exit 1
            ;;
    esac

    case "$ARCH" in
        x86_64|amd64)       ARCH="amd64" ;;
        arm64|aarch64)      ARCH="arm64" ;;
        armv6l)             ARCH="arm6"  ;;
        armv7l)             ARCH="arm6"  ;; # ARMv6 binary runs on ARMv7
        *)
            echo "Error: unsupported architecture: $ARCH" >&2
            echo "  Build from source: go install github.com/$REPO/cmd/fir@latest" >&2
            exit 1
            ;;
    esac

    PLATFORM="${OS}-${ARCH}"
    ASSET_NAME="fir-${PLATFORM}"
}

# --------------------------------------------------------------------------
# Resolve version
# --------------------------------------------------------------------------

resolve_version() {
    if [ -n "$VERSION" ]; then
        # Ensure v prefix
        case "$VERSION" in
            v*) TAG="$VERSION" ;;
            *)  TAG="v$VERSION" ;;
        esac
        return
    fi

    # Fetch latest tag from the public releases API.
    URL="https://api.github.com/repos/$REPO/releases/latest"
    TAG=$(curl -fsSL "$URL" 2>/dev/null \
        | grep '"tag_name"' | head -1 | sed 's/.*"tag_name": *"//;s/".*//')

    if [ -z "$TAG" ]; then
        echo "Error: could not determine latest version from $URL" >&2
        echo "  Check your network or install from source:" >&2
        echo "  go install github.com/kfet/fir/cmd/fir@latest" >&2
        exit 1
    fi
}

# --------------------------------------------------------------------------
# Resolve install directory
# --------------------------------------------------------------------------

resolve_install_dir() {
    if [ -n "$INSTALL_DIR" ]; then
        return
    fi

    if [ -w /usr/local/bin ]; then
        INSTALL_DIR="/usr/local/bin"
    elif [ -d "$HOME/.local/bin" ] || mkdir -p "$HOME/.local/bin" 2>/dev/null; then
        INSTALL_DIR="$HOME/.local/bin"
    else
        INSTALL_DIR="/usr/local/bin"
    fi
}

# --------------------------------------------------------------------------
# Download
# --------------------------------------------------------------------------

download() {
    DEST="$INSTALL_DIR/$BINARY"
    TMP=$(mktemp "${TMPDIR:-/tmp}/fir-install.XXXXXX")
    trap 'rm -f "$TMP"' EXIT

    echo "Installing fir $TAG ($PLATFORM) to $INSTALL_DIR..."

    ASSET_URL="https://github.com/$REPO/releases/download/$TAG/$ASSET_NAME"

    if command -v curl >/dev/null 2>&1; then
        curl -fsSL "$ASSET_URL" -o "$TMP"
    elif command -v wget >/dev/null 2>&1; then
        wget -q "$ASSET_URL" -O "$TMP"
    else
        echo "Error: neither curl nor wget found" >&2
        exit 1
    fi

    install_binary
}

install_binary() {
    chmod +x "$TMP"

    # Try direct move; fall back to sudo
    if [ -w "$INSTALL_DIR" ]; then
        mv "$TMP" "$DEST"
    else
        echo "Need sudo to write to $INSTALL_DIR"
        sudo mv "$TMP" "$DEST"
    fi

    echo "Successfully installed fir $TAG to $DEST"
    install_completions

    echo ""
    echo "Run 'fir --version' to verify."

    # Check if INSTALL_DIR is in PATH
    case ":$PATH:" in
        *":$INSTALL_DIR:"*) ;;
        *)
            echo ""
            echo "Note: $INSTALL_DIR is not in your PATH."
            echo "Add it:  export PATH=\"$INSTALL_DIR:\$PATH\""
            ;;
    esac
}

# Install bash + zsh completion to per-user directories. We never sudo here —
# system-wide install is left to package managers (Homebrew etc.). Each shell
# is handled independently so a failure on one doesn't skip the other. The
# completion script is generated to a temp file first and only moved into
# place after a successful run, so an existing valid file is never replaced
# by an empty one if `fir completion` happens to error.
install_completions() {
    bash_dir="$HOME/.local/share/bash-completion/completions"
    zsh_dir="$HOME/.local/share/zsh/site-functions"

    if mkdir -p "$bash_dir" 2>/dev/null; then
        tmp=$(mktemp "${TMPDIR:-/tmp}/fir-completion.XXXXXX") || tmp=""
        if [ -n "$tmp" ] && "$DEST" completion bash > "$tmp" 2>/dev/null; then
            chmod 644 "$tmp"
            mv "$tmp" "$bash_dir/fir"
            echo "Installed bash completion: $bash_dir/fir"
        else
            [ -n "$tmp" ] && rm -f "$tmp"
        fi
    fi

    if mkdir -p "$zsh_dir" 2>/dev/null; then
        tmp=$(mktemp "${TMPDIR:-/tmp}/fir-completion.XXXXXX") || tmp=""
        if [ -n "$tmp" ] && "$DEST" completion zsh > "$tmp" 2>/dev/null; then
            chmod 644 "$tmp"
            mv "$tmp" "$zsh_dir/_fir"
            echo "Installed zsh completion:  $zsh_dir/_fir"
            echo "  (ensure '$zsh_dir' is on your zsh \$fpath before compinit)"
        else
            [ -n "$tmp" ] && rm -f "$tmp"
        fi
    fi
}

# --------------------------------------------------------------------------
# Main
# --------------------------------------------------------------------------

detect_platform
resolve_version
resolve_install_dir
download
