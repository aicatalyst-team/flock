#!/usr/bin/env sh
# Flock installer — single-command install for macOS and Linux.
#
# Usage:
#   curl -fsSL https://get.flock.dev | sh
#   curl -fsSL https://get.flock.dev | sh -s -- join <leader-url>?token=<token>
#
set -eu

REPO="hadihonarvar/flock"
INSTALL_DIR="${FLOCK_INSTALL_DIR:-/usr/local/bin}"
TMPDIR="$(mktemp -d 2>/dev/null || mktemp -d -t 'flock')"

trap 'rm -rf "$TMPDIR"' EXIT

note() { printf "\033[1;34m▶\033[0m %s\n" "$*"; }
ok()   { printf "\033[1;32m✔\033[0m %s\n" "$*"; }
err()  { printf "\033[1;31m✖\033[0m %s\n" "$*" >&2; }

detect_platform() {
    OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
    ARCH="$(uname -m)"
    case "$ARCH" in
        x86_64|amd64) ARCH="amd64" ;;
        arm64|aarch64) ARCH="arm64" ;;
        *) err "unsupported architecture: $ARCH"; exit 1 ;;
    esac
    case "$OS" in
        darwin|linux) : ;;
        *) err "unsupported OS: $OS"; exit 1 ;;
    esac
    PLATFORM="${OS}-${ARCH}"
    note "detected platform: $PLATFORM"
}

fetch_latest_version() {
    if command -v curl >/dev/null 2>&1; then
        VERSION="$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
            | grep '"tag_name":' | head -1 | cut -d'"' -f4)"
    elif command -v wget >/dev/null 2>&1; then
        VERSION="$(wget -qO- "https://api.github.com/repos/$REPO/releases/latest" \
            | grep '"tag_name":' | head -1 | cut -d'"' -f4)"
    else
        err "need curl or wget"; exit 1
    fi
    [ -n "$VERSION" ] || { err "could not fetch latest version"; exit 1; }
    note "installing flock $VERSION"
}

download_binary() {
    URL="https://github.com/$REPO/releases/download/$VERSION/flock-${PLATFORM}.tar.gz"
    note "downloading $URL"
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL "$URL" -o "$TMPDIR/flock.tar.gz"
    else
        wget -qO "$TMPDIR/flock.tar.gz" "$URL"
    fi
    tar -xzf "$TMPDIR/flock.tar.gz" -C "$TMPDIR"
}

install_binary() {
    if [ -w "$INSTALL_DIR" ]; then
        mv "$TMPDIR/flock" "$INSTALL_DIR/flock"
    else
        note "installing to $INSTALL_DIR (sudo required)"
        sudo mv "$TMPDIR/flock" "$INSTALL_DIR/flock"
    fi
    ok "installed flock to $INSTALL_DIR/flock"
}

main() {
    detect_platform
    fetch_latest_version
    download_binary
    install_binary

    if [ "${1:-}" = "join" ] && [ -n "${2:-}" ]; then
        note "joining cluster..."
        "$INSTALL_DIR/flock" join "$2"
    else
        cat <<EOF

  Next steps:
    flock up                          # start a local Flock node
    flock --help                      # see all commands

EOF
    fi
}

main "$@"
