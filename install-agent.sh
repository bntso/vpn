#!/usr/bin/env bash
# Deprecated compatibility URL; use install.sh directly.
set -euo pipefail

URL="https://raw.githubusercontent.com/bntso/vpn/main/install.sh"

if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$URL" | bash -s -- "$@"
elif command -v wget >/dev/null 2>&1; then
    wget -qO- "$URL" | bash -s -- "$@"
else
    echo "error: need curl or wget" >&2
    exit 1
fi
