#!/bin/sh
# install.sh — download and install fir for the current platform.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/kfet/fir/main/install.sh | sh
#
# For private repos, either:
#   1. Have gh CLI installed and authenticated (gh auth login)
#   2. Or set GITHUB_TOKEN before running the script
#
# Options (environment variables):
#   INSTALL_DIR  — where to install (default: /usr/local/bin, or ~/.local/bin if no write access)
#   VERSION      — specific version to install (default: latest)
#   GITHUB_TOKEN — used for curl auth if gh is not available (private repos)

set -e

REPO="kfet/fir"
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

    # Fetch latest tag via gh or curl
    if command -v gh >/dev/null 2>&1; then
        TAG=$(gh api "repos/$REPO/releases/latest" --jq '.tag_name' 2>/dev/null) || true
    fi

    if [ -z "$TAG" ]; then
        URL="https://api.github.com/repos/$REPO/releases/latest"
        if [ -n "$GITHUB_TOKEN" ]; then
            TAG=$(curl -fsSL -H "Authorization: Bearer $GITHUB_TOKEN" "$URL" 2>/dev/null \
                | grep '"tag_name"' | head -1 | sed 's/.*"tag_name": *"//;s/".*//')
        else
            TAG=$(curl -fsSL "$URL" 2>/dev/null \
                | grep '"tag_name"' | head -1 | sed 's/.*"tag_name": *"//;s/".*//')
        fi
    fi

    if [ -z "$TAG" ]; then
        echo "Error: could not determine latest version." >&2
        echo "  For private repos: install gh (https://cli.github.com) and run 'gh auth login'" >&2
        echo "  Or set GITHUB_TOKEN and retry." >&2
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

    # Try gh first (handles private repos with SSH/OAuth)
    if command -v gh >/dev/null 2>&1; then
        if gh release download "$TAG" \
            --repo "$REPO" \
            --pattern "$ASSET_NAME" \
            --output "$TMP" \
            --clobber 2>/dev/null; then
            install_binary
            return
        fi
        # gh failed (maybe not authed for this repo) — fall through to curl/wget
    fi

    # Direct HTTPS download (public repos, or private with GITHUB_TOKEN)
    ASSET_URL="https://github.com/$REPO/releases/download/$TAG/$ASSET_NAME"

    if command -v curl >/dev/null 2>&1; then
        if [ -n "$GITHUB_TOKEN" ]; then
            curl -fsSL -H "Authorization: Bearer $GITHUB_TOKEN" "$ASSET_URL" -o "$TMP"
        else
            curl -fsSL "$ASSET_URL" -o "$TMP"
        fi
    elif command -v wget >/dev/null 2>&1; then
        if [ -n "$GITHUB_TOKEN" ]; then
            wget -q --header="Authorization: Bearer $GITHUB_TOKEN" "$ASSET_URL" -O "$TMP"
        else
            wget -q "$ASSET_URL" -O "$TMP"
        fi
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

# --------------------------------------------------------------------------
# Main
# --------------------------------------------------------------------------

detect_platform
resolve_version
resolve_install_dir
download
