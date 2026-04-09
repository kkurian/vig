#!/usr/bin/env bash
#
# vig installer. Fetches the latest release binary for the current
# macOS architecture, verifies its SHA-256 against SHA256SUMS, and
# installs it (defaulting to /usr/local/bin). After this, run
# `vig install` to register the LaunchAgent.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/kkurian/vig/main/install.sh | bash
#
# Environment variables:
#   VIG_INSTALL_DIR   Where to place the binary (default: /usr/local/bin)
#   VIG_VERSION       Specific tag to install (default: latest release)

set -euo pipefail

REPO="kkurian/vig"
BIN_NAME="vig"
INSTALL_DIR="${VIG_INSTALL_DIR:-/usr/local/bin}"

say() { printf '%s\n' "$*"; }
die() { printf 'vig install: %s\n' "$*" >&2; exit 1; }

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

# ---- resolve version ----
VERSION="${VIG_VERSION:-}"
if [ -z "$VERSION" ]; then
    # Parse the "latest" release tag from the GitHub API response.
    VERSION=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
        | grep '"tag_name"' | head -1 | cut -d'"' -f4)
fi
if [ -z "$VERSION" ]; then
    die "could not determine latest release version"
fi

BIN_FILE="${BIN_NAME}-darwin-${ARCH}"
BASE_URL="https://github.com/${REPO}/releases/download/${VERSION}"

say "Installing vig $VERSION for darwin/$ARCH..."

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
# There are three cases:
#
#   1. The install directory is writable by us → mv into place.
#   2. The install directory is NOT writable but an existing target
#      file IS writable (common on macOS where /usr/local/bin is root-
#      owned but a Homebrew user already chown'd a vig binary into it)
#      → cp over the file in place. This avoids a needless sudo prompt
#      during upgrades.
#   3. Neither → sudo.
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
say "vig $VERSION installed to ${INSTALL_DIR}/${BIN_NAME}"
say ""
say "Next step — register as a Login Item so it auto-starts at login:"
say "    vig install"
