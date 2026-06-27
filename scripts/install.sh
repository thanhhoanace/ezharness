#!/bin/sh
# Method 1: curl -fsSL https://ezharness.dev/install.sh | sh
# (host this file at ezharness.dev/install.sh, or use the raw GitHub URL)
# Detects OS/arch, downloads the matching release binary, installs `ezh`.
set -eu

OWNER="${EZH_OWNER:-thanhhoanace}"     # GitHub org/user
REPO="ezharness"
BIN="ezh"
INSTALL_DIR="${EZH_INSTALL_DIR:-/usr/local/bin}"

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
arch="$(uname -m)"
case "$arch" in
  x86_64|amd64) arch="amd64" ;;
  arm64|aarch64) arch="arm64" ;;
  *) echo "ezh: unsupported arch: $arch" >&2; exit 1 ;;
esac
case "$os" in
  linux|darwin) ;;
  *) echo "ezh: unsupported OS: $os" >&2; exit 1 ;;
esac

# Resolve the latest release tag (override with EZH_VERSION=v6.1.0).
tag="${EZH_VERSION:-}"
if [ -z "$tag" ]; then
  tag="$(curl -fsSL "https://api.github.com/repos/$OWNER/$REPO/releases/latest" \
    | sed -n 's/.*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1)"
fi
[ -n "$tag" ] || { echo "ezh: could not resolve latest release" >&2; exit 1; }

ver="${tag#v}"
url="https://github.com/$OWNER/$REPO/releases/download/$tag/ezh_${ver}_${os}_${arch}.tar.gz"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
echo "ezh: downloading $tag ($os/$arch)…"
curl -fsSL "$url" | tar -xz -C "$tmp"

if [ -w "$INSTALL_DIR" ]; then
  mv "$tmp/$BIN" "$INSTALL_DIR/$BIN"
else
  echo "ezh: installing to $INSTALL_DIR (sudo)…"
  sudo mv "$tmp/$BIN" "$INSTALL_DIR/$BIN"
fi
chmod +x "$INSTALL_DIR/$BIN"

echo "ezh: installed $("$INSTALL_DIR/$BIN" version)"
echo "Next: cd <your repo> && ezh attest --help"
