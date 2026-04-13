#!/bin/sh
# Install skep — single binary, no dependencies.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/ChaitanyaPinapaka/skep/main/install.sh | sh
#
# Environment overrides:
#   SKEP_VERSION   pin a specific tag (e.g. v0.1.0); default: latest release
#   SKEP_PREFIX    install prefix; default: /usr/local/bin (falls back to $HOME/.local/bin)

set -eu

REPO="ChaitanyaPinapaka/skep"
VERSION="${SKEP_VERSION:-}"
PREFIX="${SKEP_PREFIX:-/usr/local/bin}"

err() { printf 'error: %s\n' "$*" >&2; exit 1; }
info() { printf '%s\n' "$*"; }

need() {
  command -v "$1" >/dev/null 2>&1 || err "required tool not found: $1"
}

need uname
need curl
need tar

# ---- detect platform -------------------------------------------------------
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)

case "$ARCH" in
  x86_64|amd64)  ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) err "unsupported architecture: $ARCH" ;;
esac

case "$OS" in
  linux|darwin) ;;
  *) err "unsupported OS: $OS" ;;
esac

# ---- resolve version -------------------------------------------------------
if [ -z "$VERSION" ]; then
  info "Resolving latest release..."
  VERSION=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep -m1 '"tag_name"' \
    | sed -E 's/.*"tag_name"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/')
  [ -n "$VERSION" ] || err "could not determine latest release tag"
fi

# Strip leading "v" for filenames that GoReleaser emits (skep_0.1.0_...).
VER_NUM="${VERSION#v}"

ARCHIVE="skep_${VER_NUM}_${OS}_${ARCH}.tar.gz"
CHECKSUMS="skep_${VER_NUM}_checksums.txt"
BASE_URL="https://github.com/${REPO}/releases/download/${VERSION}"
ARCHIVE_URL="${BASE_URL}/${ARCHIVE}"
CHECKSUMS_URL="${BASE_URL}/${CHECKSUMS}"

info "Installing skep ${VERSION} for ${OS}/${ARCH}"

# ---- download to a temp dir ------------------------------------------------
TMP=$(mktemp -d 2>/dev/null || mktemp -d -t skep)
trap 'rm -rf "$TMP"' EXIT INT HUP TERM

info "Downloading ${ARCHIVE}..."
if ! curl -fsSL "$ARCHIVE_URL" -o "${TMP}/${ARCHIVE}"; then
  err "failed to download ${ARCHIVE_URL} — has this platform been released yet?"
fi

info "Downloading ${CHECKSUMS}..."
if ! curl -fsSL "$CHECKSUMS_URL" -o "${TMP}/${CHECKSUMS}"; then
  err "failed to download checksums file"
fi

# ---- verify sha256 ---------------------------------------------------------
EXPECTED=$(grep " ${ARCHIVE}\$" "${TMP}/${CHECKSUMS}" | awk '{print $1}')
[ -n "$EXPECTED" ] || err "checksum for ${ARCHIVE} not found in ${CHECKSUMS}"

if command -v sha256sum >/dev/null 2>&1; then
  ACTUAL=$(sha256sum "${TMP}/${ARCHIVE}" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
  ACTUAL=$(shasum -a 256 "${TMP}/${ARCHIVE}" | awk '{print $1}')
else
  err "no sha256 tool available (need sha256sum or shasum)"
fi

if [ "$EXPECTED" != "$ACTUAL" ]; then
  err "checksum mismatch: expected $EXPECTED, got $ACTUAL"
fi
info "Checksum OK"

# ---- extract ---------------------------------------------------------------
tar -xzf "${TMP}/${ARCHIVE}" -C "${TMP}"
[ -f "${TMP}/skep" ] || err "archive did not contain a skep binary"
chmod +x "${TMP}/skep"

# ---- install ---------------------------------------------------------------
install_to() {
  dest_dir="$1"
  mkdir -p "$dest_dir"
  # install(1) is atomic and idempotent — overwrite is fine.
  if [ -w "$dest_dir" ]; then
    install -m 0755 "${TMP}/skep" "${dest_dir}/skep"
  else
    if command -v sudo >/dev/null 2>&1; then
      sudo install -m 0755 "${TMP}/skep" "${dest_dir}/skep"
    else
      err "cannot write to ${dest_dir} and sudo is not available"
    fi
  fi
  INSTALLED="${dest_dir}/skep"
}

INSTALLED=""
if [ -d "$PREFIX" ] || mkdir -p "$PREFIX" 2>/dev/null; then
  if [ -w "$PREFIX" ] || command -v sudo >/dev/null 2>&1; then
    install_to "$PREFIX"
  fi
fi

if [ -z "$INSTALLED" ]; then
  install_to "${HOME}/.local/bin"
  case ":$PATH:" in
    *":${HOME}/.local/bin:"*) ;;
    *) info "note: add ${HOME}/.local/bin to your PATH" ;;
  esac
fi

info ""
info "skep installed to: ${INSTALLED}"
"$INSTALLED" --version 2>/dev/null || true
info ""
info "Quick start:"
info "  cd your-project"
info "  skep init         # set up the per-repo agent"
info "  skep doctor       # verify environment"
info "  skep index ask \"auth\"  # query the index"
