#!/usr/bin/env bash
# Install the bntsovpn binary on a mesh node and optionally join the mesh.
#
#   curl -fsSL https://raw.githubusercontent.com/bntso/vpn/main/install-agent.sh | sudo bash
#   curl -fsSL https://raw.githubusercontent.com/bntso/vpn/main/install-agent.sh | sudo bash -s -- '<join-blob>'
#
# The join blob comes from `bntsovpn invite <node>` on the admin machine.
set -euo pipefail

REPO="bntso/vpn"
BIN_PATH="/usr/local/bin/bntsovpn"
SELF_URL="https://raw.githubusercontent.com/${REPO}/main/install-agent.sh"
JOIN_BLOB="${1:-${JOIN_BLOB:-}}"

download() {
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL "$1" -o "$2"
    elif command -v wget >/dev/null 2>&1; then
        wget -qO "$2" "$1"
    else
        echo "error: need curl or wget" >&2
        exit 1
    fi
}

if [ "$(id -u)" -ne 0 ]; then
    if command -v sudo >/dev/null 2>&1; then
        echo "re-running with sudo..."
        SELF="$(mktemp)"
        download "$SELF_URL" "$SELF"
        exec sudo bash "$SELF" "$@"
    fi
    echo "error: must run as root (try: curl ... | sudo bash)" >&2
    exit 1
fi

case "$(uname -m)" in
    x86_64|amd64) ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *) echo "error: unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac
if [ "$(uname -s)" != "Linux" ]; then
    echo "error: bntsovpn only supports Linux" >&2
    exit 1
fi

BASE="https://github.com/${REPO}/releases/latest/download"
ASSET="bntsovpn-linux-${ARCH}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "downloading ${ASSET} (latest release)..."
download "${BASE}/${ASSET}" "${TMP}/${ASSET}"

if command -v sha256sum >/dev/null 2>&1; then
    download "${BASE}/checksums.txt" "${TMP}/checksums.txt"
    (cd "$TMP" && grep " ${ASSET}\$" checksums.txt | sha256sum -c - >/dev/null) || {
        echo "error: checksum verification failed" >&2
        exit 1
    }
    echo "checksum verified"
fi

install -m 0755 "${TMP}/${ASSET}" "$BIN_PATH"
echo "installed ${BIN_PATH}"

if [ -n "$JOIN_BLOB" ]; then
    echo "joining mesh..."
    "$BIN_PATH" join "$JOIN_BLOB"
else
    cat <<'EOF'

Next step — join this node to the mesh (blob from `bntsovpn invite <node>` on the admin machine):

    bntsovpn join '<join-blob>'

EOF
fi
