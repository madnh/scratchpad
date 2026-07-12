#!/bin/sh
# scratchpad installer — https://github.com/madnh/scratchpad
#
#   curl -fsSL https://madnh.github.io/scratchpad/install.sh | sh
#
# Detects OS/arch, downloads the matching binary from the latest GitHub
# Release, verifies its SHA256 checksum, and installs it to ~/.local/bin.
#
# Asset naming contract (see .goreleaser.yaml): scratchpad_<os>_<arch>
# with a checksums.txt next to it, both under releases/latest/download/.
set -eu

REPO="madnh/scratchpad"
BASE_URL="https://github.com/${REPO}/releases/latest/download"
INSTALL_DIR="${SCRATCHPAD_INSTALL_DIR:-$HOME/.local/bin}"
BINARY="scratchpad"

err() { printf 'install.sh: %s\n' "$*" >&2; exit 1; }

os=$(uname -s)
case "$os" in
  Darwin) os=darwin ;;
  Linux)  os=linux ;;
  *) err "unsupported OS: $os (prebuilt binaries cover darwin/linux; build from source: go install github.com/${REPO}/cmd/scratchpad@latest)" ;;
esac

arch=$(uname -m)
case "$arch" in
  x86_64|amd64)  arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) err "unsupported architecture: $arch (prebuilt binaries cover amd64/arm64)" ;;
esac

asset="${BINARY}_${os}_${arch}"

# SCRATCHPAD_DRY_RUN=1: print what would be downloaded/installed, touch nothing.
if [ "${SCRATCHPAD_DRY_RUN:-0}" = "1" ]; then
  printf 'dry-run: os=%s arch=%s\n' "$os" "$arch"
  printf 'dry-run: binary    %s/%s\n' "$BASE_URL" "$asset"
  printf 'dry-run: checksums %s/checksums.txt\n' "$BASE_URL"
  printf 'dry-run: install   %s/%s\n' "$INSTALL_DIR" "$BINARY"
  exit 0
fi

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT INT TERM

printf 'Downloading %s from the latest release of %s ...\n' "$asset" "$REPO"
curl -fsSL -o "$tmpdir/$asset" "$BASE_URL/$asset" \
  || err "download failed: $BASE_URL/$asset"
curl -fsSL -o "$tmpdir/checksums.txt" "$BASE_URL/checksums.txt" \
  || err "download failed: $BASE_URL/checksums.txt"

expected=$(awk -v a="$asset" '$2 == a { print $1 }' "$tmpdir/checksums.txt")
[ -n "$expected" ] || err "no checksum entry for $asset in checksums.txt"

if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$tmpdir/$asset" | awk '{ print $1 }')
elif command -v shasum >/dev/null 2>&1; then
  actual=$(shasum -a 256 "$tmpdir/$asset" | awk '{ print $1 }')
else
  err "need sha256sum or shasum to verify the download"
fi
[ "$actual" = "$expected" ] || err "checksum mismatch for $asset (expected $expected, got $actual)"
printf 'Checksum OK.\n'

mkdir -p "$INSTALL_DIR"
install -m 0755 "$tmpdir/$asset" "$INSTALL_DIR/$BINARY" 2>/dev/null \
  || { cp "$tmpdir/$asset" "$INSTALL_DIR/$BINARY" && chmod 0755 "$INSTALL_DIR/$BINARY"; }

printf 'Installed %s to %s\n' "$BINARY" "$INSTALL_DIR/$BINARY"
"$INSTALL_DIR/$BINARY" version || true

case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *)
    printf '\nNote: %s is not on your PATH. Add it, e.g.:\n' "$INSTALL_DIR"
    printf '  export PATH="%s:$PATH"\n' "$INSTALL_DIR"
    ;;
esac
