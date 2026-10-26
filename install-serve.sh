#!/usr/bin/env bash
# Install the bntsovpn binary on the admin (control-plane) machine.
#
#   curl -fsSL https://raw.githubusercontent.com/bntso/vpn/main/install-serve.sh | sudo bash
#
# Then scaffold the mesh config, edit it, and reconcile:
#
#   bntsovpn init                      # writes /etc/bntsovpn/config.yaml
#   $EDITOR /etc/bntsovpn/config.yaml  # define your nodes
#   bntsovpn apply                     # brings up wg0 + installs/starts bntsovpn-serve
#
# `--init` runs init + apply for you (fine for a first-time setup you will
# edit and re-apply afterwards):
#
#   curl -fsSL https://raw.githubusercontent.com/bntso/vpn/main/install-serve.sh | sudo bash -s -- --init
set -euo pipefail

REPO="bntso/vpn"
BIN_PATH="/usr/local/bin/bntsovpn"
SELF_URL="https://raw.githubusercontent.com/${REPO}/main/install-serve.sh"
RUN_INIT=0
[ "${1:-}" = "--init" ] && RUN_INIT=1

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

if [ "$RUN_INIT" = "1" ]; then
    if [ ! -f /etc/bntsovpn/config.yaml ]; then
        "$BIN_PATH" init
        echo "scaffolded /etc/bntsovpn/config.yaml — edit it, then re-run: bntsovpn apply"
    else
        echo "/etc/bntsovpn/config.yaml already exists, skipping init"
        "$BIN_PATH" apply
    fi
else
    cat <<'EOF'

Next steps:

    bntsovpn init                      # scaffold /etc/bntsovpn/config.yaml
    $EDITOR /etc/bntsovpn/config.yaml  # define your nodes
    bntsovpn apply                     # bring up wg0, install + start bntsovpn-serve

EOF
fi
