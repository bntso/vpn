#!/usr/bin/env bash
# Install bntsovpn, optionally scaffold the control plane, or join a mesh node.
#
#   curl -fsSL https://raw.githubusercontent.com/bntso/vpn/main/install.sh | sudo bash
#   curl -fsSL https://raw.githubusercontent.com/bntso/vpn/main/install.sh | sudo bash -s -- --init
#   curl -fsSL https://raw.githubusercontent.com/bntso/vpn/main/install.sh | sudo bash -s -- '<join-blob>'
#
# The join blob comes from `bntsovpn invite <node>` on the admin machine.
set -euo pipefail

REPO="bntso/vpn"
BIN_PATH="/usr/local/bin/bntsovpn"
COMPLETION_PATH="/usr/share/bash-completion/completions/bntsovpn"
SELF_URL="https://raw.githubusercontent.com/${REPO}/main/install.sh"
RUN_INIT=0
JOIN_BLOB="${JOIN_BLOB:-}"

if [ "${1:-}" = "--init" ]; then
    RUN_INIT=1
    shift
elif [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
    echo "usage: install.sh [--init | <join-blob>]"
    exit 0
elif [[ "${1:-}" == -* ]]; then
    echo "error: unknown option: $1" >&2
    exit 1
fi
if [ "$#" -gt 1 ]; then
    echo "error: expected --init or one join blob" >&2
    exit 1
fi
if [ "$#" -eq 1 ]; then
    if [ "$RUN_INIT" = "1" ] || [ -n "$JOIN_BLOB" ]; then
        echo "error: --init and a join blob are mutually exclusive" >&2
        exit 1
    fi
    JOIN_BLOB="$1"
fi
if [ "$RUN_INIT" = "1" ] && [ -n "$JOIN_BLOB" ]; then
    echo "error: --init and a join blob are mutually exclusive" >&2
    exit 1
fi

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
        if [ "$RUN_INIT" = "1" ]; then
            set -- --init
        elif [ -n "$JOIN_BLOB" ]; then
            set -- "$JOIN_BLOB"
        else
            set --
        fi
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

chmod 0755 "${TMP}/${ASSET}"
"${TMP}/${ASSET}" completion bash > "${TMP}/bash-completion"

install -m 0755 "${TMP}/${ASSET}" "$BIN_PATH"
echo "installed ${BIN_PATH}"
install -d "$(dirname "$COMPLETION_PATH")"
install -m 0644 "${TMP}/bash-completion" "$COMPLETION_PATH"
echo "installed ${COMPLETION_PATH}"

if [ -n "$JOIN_BLOB" ]; then
    echo "joining mesh..."
    "$BIN_PATH" join "$JOIN_BLOB"
elif [ "$RUN_INIT" = "1" ]; then
    if [ -f /etc/bntsovpn/config.yaml ] && [ ! -f /etc/bntsovpn/config.hcl ]; then
        echo "error: /etc/bntsovpn/config.yaml uses legacy YAML; run bntsovpn convert-config first" >&2
        exit 1
    fi
    if [ ! -f /etc/bntsovpn/config.hcl ]; then
        "$BIN_PATH" init
        echo "scaffolded /etc/bntsovpn/config.hcl — edit it, then re-run: bntsovpn apply"
    else
        echo "/etc/bntsovpn/config.hcl already exists, skipping init"
        "$BIN_PATH" apply
    fi
else
    cat <<'EOF'

Next steps on the admin machine:

    bntsovpn init                      # scaffold /etc/bntsovpn/config.hcl
    $EDITOR /etc/bntsovpn/config.hcl  # define your nodes
    bntsovpn apply                     # bring up wg0, install + start bntsovpn

Next step on each node (blob from `bntsovpn invite <node>` on the admin machine):

    bntsovpn join '<join-blob>'        # bring up wg0, install + start bntsovpn

EOF
fi
