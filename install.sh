#!/usr/bin/env bash
#
# vig installer. Fetches the latest release binary for the current
# macOS architecture, verifies its SHA-256 against SHA256SUMS, and
# installs it into a user-owned directory. After this, run
# `vig install` to register the macOS Login Item.
#
# Why a user-owned directory by default: macOS 15 (Sequoia) syspolicyd
# can cache negative Gatekeeper decisions for binaries at specific
# paths under /usr/local/bin. Once cached, an in-place overwrite of
# the same path won't invalidate the decision (the cache is path-
# keyed), and the binary gets SIGKILLed before main() runs — exit 137
# with no diagnostic. Installing to ~/.local/bin avoids the cache
# entirely, needs no sudo, and works out of the box on modern macOS.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/kkurian/vig/main/install.sh | bash
#
# Environment variables:
#   VIG_INSTALL_DIR   Where to place the binary. If unset, an in-PATH
#                     user-owned directory is auto-detected, falling
#                     back to ~/.local/bin.
#   VIG_VERSION       Specific tag to install (default: latest release)

set -euo pipefail

REPO="kkurian/vig"
BIN_NAME="vig"

say() { printf '%s\n' "$*"; }
die() { printf 'vig install: %s\n' "$*" >&2; exit 1; }

# path_contains checks whether $1 is present as a literal entry in PATH.
path_contains() {
    case ":$PATH:" in
        *":$1:"*) return 0 ;;
        *) return 1 ;;
    esac
}

# ---- platform detection ----
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
if [ "$OS" != "darwin" ]; then
    die "vig only supports macOS. Detected: $OS"
fi

case "$(uname -m)" in
    x86_64|amd64)  ARCH=amd64 ;;
    arm64|aarch64) ARCH=arm64 ;;
    *) die "unsupported arch: $(uname -m)" ;;
esac

# ---- choose install directory ----
#
# If the user set VIG_INSTALL_DIR, honor it. Otherwise auto-detect:
# prefer an existing, in-PATH, user-writable directory, falling back
# to ~/.local/bin which we create if it doesn't exist.
if [ -n "${VIG_INSTALL_DIR:-}" ]; then
    INSTALL_DIR="$VIG_INSTALL_DIR"
else
    INSTALL_DIR=""
    for candidate in "$HOME/bin" "$HOME/.local/bin" "${GOPATH:-$HOME/go}/bin"; do
        if [ -d "$candidate" ] && [ -w "$candidate" ] && path_contains "$candidate"; then
            INSTALL_DIR="$candidate"
            break
        fi
    done
    if [ -z "$INSTALL_DIR" ]; then
        INSTALL_DIR="$HOME/.local/bin"
    fi
fi

# ---- resolve version ----
VERSION="${VIG_VERSION:-}"
if [ -z "$VERSION" ]; then
    VERSION=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
        | grep '"tag_name"' | head -1 | cut -d'"' -f4)
fi
if [ -z "$VERSION" ]; then
    die "could not determine latest release version"
fi

BIN_FILE="${BIN_NAME}-darwin-${ARCH}"
BASE_URL="https://github.com/${REPO}/releases/download/${VERSION}"

say "Installing vig $VERSION for darwin/$ARCH to ${INSTALL_DIR}..."

TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT

# ---- download binary + checksums ----
curl -fsSL "${BASE_URL}/${BIN_FILE}" -o "${TMPDIR}/${BIN_NAME}"
curl -fsSL "${BASE_URL}/SHA256SUMS"  -o "${TMPDIR}/SHA256SUMS"

# ---- verify ----
EXPECTED=$(grep " ${BIN_FILE}\$" "${TMPDIR}/SHA256SUMS" | awk '{print $1}')
if [ -z "$EXPECTED" ]; then
    die "no checksum entry for ${BIN_FILE} in SHA256SUMS"
fi
ACTUAL=$(shasum -a 256 "${TMPDIR}/${BIN_NAME}" | awk '{print $1}')
if [ "$EXPECTED" != "$ACTUAL" ]; then
    die "checksum mismatch: expected ${EXPECTED}, got ${ACTUAL}"
fi

chmod +x "${TMPDIR}/${BIN_NAME}"

# ---- install ----
#
# We prefer user-owned directories, but still handle the case where
# VIG_INSTALL_DIR points at something that needs sudo.
mkdir -p "$INSTALL_DIR" 2>/dev/null || true
TARGET="${INSTALL_DIR}/${BIN_NAME}"
if [ -w "$INSTALL_DIR" ]; then
    mv "${TMPDIR}/${BIN_NAME}" "$TARGET"
elif [ -e "$TARGET" ] && [ -w "$TARGET" ]; then
    cp "${TMPDIR}/${BIN_NAME}" "$TARGET"
else
    say "Installing to $INSTALL_DIR requires sudo..."
    sudo mv "${TMPDIR}/${BIN_NAME}" "$TARGET"
fi

say ""
say "vig $VERSION installed to $TARGET"

# ---- PATH guidance ----
if ! path_contains "$INSTALL_DIR"; then
    say ""
    say "Note: $INSTALL_DIR is not in your PATH. Add it to your shell profile:"
    if [ -f "$HOME/.zshrc" ]; then
        say "    echo 'export PATH=\"$INSTALL_DIR:\$PATH\"' >> ~/.zshrc"
        say "    source ~/.zshrc"
    else
        say "    export PATH=\"$INSTALL_DIR:\$PATH\""
    fi
fi

say ""
say "To finish setup, run:"
say "    $TARGET install"
say ""
say "That will wrap vig in ~/Applications/vig.app, add it to Login"
say "Items → Open at Login, and start it immediately."
