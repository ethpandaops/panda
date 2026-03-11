#!/bin/sh
# install.sh — curl|sh installer for the panda CLI binary.
#
# Usage:
#   curl -sSfL https://raw.githubusercontent.com/ethpandaops/panda/master/scripts/install.sh | sh
#   curl -sSfL ... | sh -s -- --install-dir /usr/local/bin
#
# Downloads the latest panda release from GitHub and installs it to
# ~/.local/bin (or a custom directory via --install-dir).

set -e

# --------------------------------------------------------------------------- #
# Colors (disabled when not writing to a terminal)
# --------------------------------------------------------------------------- #

setup_colors() {
    if [ -t 1 ]; then
        RED='\033[0;31m'
        GREEN='\033[0;32m'
        YELLOW='\033[0;33m'
        BOLD='\033[1m'
        RESET='\033[0m'
    else
        RED=''
        GREEN=''
        YELLOW=''
        BOLD=''
        RESET=''
    fi
}

# --------------------------------------------------------------------------- #
# Logging helpers
# --------------------------------------------------------------------------- #

info()    { printf "${GREEN}[INFO]${RESET}  %s\n" "$*"; }
warn()    { printf "${YELLOW}[WARN]${RESET}  %s\n" "$*"; }
error()   { printf "${RED}[ERROR]${RESET} %s\n" "$*" >&2; }
fatal()   { error "$@"; exit 1; }

# --------------------------------------------------------------------------- #
# Detect OS and architecture
# --------------------------------------------------------------------------- #

detect_os() {
    os="$(uname -s | tr '[:upper:]' '[:lower:]')"
    case "$os" in
        linux)  OS="linux" ;;
        darwin) OS="darwin" ;;
        *)      fatal "Unsupported operating system: $os" ;;
    esac
}

detect_arch() {
    arch="$(uname -m)"
    case "$arch" in
        x86_64)         ARCH="amd64" ;;
        amd64)          ARCH="amd64" ;;
        aarch64|arm64)  ARCH="arm64" ;;
        *)              fatal "Unsupported architecture: $arch" ;;
    esac
}

# --------------------------------------------------------------------------- #
# Parse CLI arguments
# --------------------------------------------------------------------------- #

parse_args() {
    INSTALL_DIR="${HOME}/.local/bin"

    while [ $# -gt 0 ]; do
        case "$1" in
            --install-dir)
                if [ -z "${2:-}" ]; then
                    fatal "--install-dir requires a directory argument"
                fi
                INSTALL_DIR="$2"
                shift 2
                ;;
            --install-dir=*)
                INSTALL_DIR="${1#*=}"
                shift
                ;;
            -h|--help)
                usage
                exit 0
                ;;
            *)
                fatal "Unknown argument: $1"
                ;;
        esac
    done
}

usage() {
    cat <<EOF
Usage: install.sh [OPTIONS]

Install the panda CLI binary from the latest GitHub release.

Options:
  --install-dir DIR   Install binary to DIR (default: ~/.local/bin)
  -h, --help          Show this help message
EOF
}

# --------------------------------------------------------------------------- #
# Fetch latest release tag from GitHub API
# --------------------------------------------------------------------------- #

get_latest_version() {
    RELEASES_URL="https://api.github.com/repos/ethpandaops/panda/releases/latest"
    info "Querying latest release from GitHub..."

    response="$(curl -sSfL "$RELEASES_URL" 2>&1)" || \
        fatal "Failed to fetch latest release from GitHub. Check your network connection."

    # Extract tag_name from JSON. Try jq first, fall back to grep+sed.
    if command -v jq >/dev/null 2>&1; then
        VERSION="$(printf '%s' "$response" | jq -r '.tag_name')"
    else
        VERSION="$(printf '%s' "$response" | grep '"tag_name"' | sed -E 's/.*"tag_name":[[:space:]]*"([^"]+)".*/\1/')"
    fi

    if [ -z "$VERSION" ] || [ "$VERSION" = "null" ]; then
        fatal "Could not determine latest release version"
    fi

    info "Latest version: ${BOLD}${VERSION}${RESET}"
}

# --------------------------------------------------------------------------- #
# Download and install the binary
# --------------------------------------------------------------------------- #

download_binary() {
    # goreleaser strips the leading 'v' from the tag for archive names.
    CLEAN_VERSION="${VERSION#v}"
    ASSET_NAME="panda_${CLEAN_VERSION}_${OS}_${ARCH}.tar.gz"
    DOWNLOAD_URL="https://github.com/ethpandaops/panda/releases/download/${VERSION}/${ASSET_NAME}"

    info "Downloading ${BOLD}${ASSET_NAME}${RESET}..."

    # Create a temporary directory for extraction.
    tmpdir="$(mktemp -d)" || fatal "Failed to create temporary directory"
    # shellcheck disable=SC2064
    trap "rm -rf '$tmpdir'" EXIT INT TERM

    tmpfile="${tmpdir}/archive.tar.gz"
    http_code="$(curl -sSfL -w '%{http_code}' -o "$tmpfile" "$DOWNLOAD_URL" 2>/dev/null)" || true

    if [ ! -s "$tmpfile" ]; then
        fatal "Download failed for ${DOWNLOAD_URL} (HTTP ${http_code:-unknown}). Asset may not exist for ${OS}/${ARCH}."
    fi

    # Extract the panda binary from the archive.
    tar -xzf "$tmpfile" -C "$tmpdir" panda 2>/dev/null || \
        fatal "Failed to extract panda binary from archive"

    tmpfile="${tmpdir}/panda"

    if [ ! -f "$tmpfile" ]; then
        fatal "panda binary not found in archive"
    fi

    info "Download complete"
}

install_binary() {
    # Ensure install directory exists.
    if ! mkdir -p "$INSTALL_DIR" 2>/dev/null; then
        fatal "Cannot create install directory: ${INSTALL_DIR}. Check permissions."
    fi

    DEST="${INSTALL_DIR}/panda"

    if ! mv "$tmpfile" "$DEST" 2>/dev/null; then
        # mv may fail across filesystems; fall back to cp + rm.
        if ! cp "$tmpfile" "$DEST" 2>/dev/null; then
            fatal "Cannot write to ${DEST}. Check permissions or use --install-dir."
        fi
        rm -f "$tmpfile"
    fi

    chmod +x "$DEST" || fatal "Failed to make ${DEST} executable"

    info "Installed ${BOLD}panda${RESET} to ${BOLD}${DEST}${RESET}"
}

# --------------------------------------------------------------------------- #
# PATH check
# --------------------------------------------------------------------------- #

check_path() {
    case ":${PATH}:" in
        *":${INSTALL_DIR}:"*)
            # Already in PATH, nothing to do.
            ;;
        *)
            warn "${INSTALL_DIR} is not in your PATH."
            warn "Add it by appending the following to your shell profile:"
            warn "  export PATH=\"${INSTALL_DIR}:\$PATH\""
            ;;
    esac
}

# --------------------------------------------------------------------------- #
# Main
# --------------------------------------------------------------------------- #

main() {
    setup_colors
    parse_args "$@"

    info "Installing ${BOLD}panda${RESET} CLI..."

    detect_os
    detect_arch
    info "Detected platform: ${BOLD}${OS}/${ARCH}${RESET}"

    get_latest_version
    download_binary
    install_binary
    check_path

    printf "\n"
    info "${GREEN}Installation complete!${RESET}"

    case ":${PATH}:" in
        *":${INSTALL_DIR}:"*)
            info "Run ${BOLD}panda init${RESET} to get started."
            ;;
        *)
            info "Restart your shell (or run ${BOLD}export PATH=\"${INSTALL_DIR}:\$PATH\"${RESET}), then run ${BOLD}panda init${RESET} to get started."
            ;;
    esac
}

main "$@"
