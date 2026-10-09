#!/bin/sh
# Install bot-connect from GitHub Releases.
#
#   curl -fsSL https://raw.githubusercontent.com/chenhg5/bot-connect/main/install.sh | sh
#
# Environment:
#   BOT_CONNECT_VERSION      version to install (default: newest release, pre-releases included)
#   BOT_CONNECT_INSTALL_DIR  where to put the binary (default: ~/.local/bin)
set -eu

REPO="chenhg5/bot-connect"
DIR="${BOT_CONNECT_INSTALL_DIR:-$HOME/.local/bin}"

say() { printf '%s\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

command -v curl >/dev/null 2>&1 || die "curl is required"
command -v tar >/dev/null 2>&1 || die "tar is required"

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) die "unsupported OS $(uname -s) (macOS and Linux only)" ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) die "unsupported architecture $(uname -m)" ;;
esac

version="${BOT_CONNECT_VERSION:-}"
if [ -z "$version" ]; then
  # /releases/latest skips pre-releases, so take the newest entry of the list.
  version=$(curl -fsSL "https://api.github.com/repos/$REPO/releases?per_page=1" |
    sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1)
  [ -n "$version" ] || die "could not determine the latest version (set BOT_CONNECT_VERSION)"
fi

name="bot-connect-$version-$os-$arch"
base="https://github.com/$REPO/releases/download/$version"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

say "Downloading bot-connect $version ($os/$arch)…"
curl -fsSL -o "$tmp/$name.tar.gz" "$base/$name.tar.gz" || die "download failed: $base/$name.tar.gz"
curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt" || die "download failed: checksums.txt"

expected=$(grep " $name.tar.gz\$" "$tmp/checksums.txt" | cut -d' ' -f1)
[ -n "$expected" ] || die "no checksum for $name.tar.gz"
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$tmp/$name.tar.gz" | cut -d' ' -f1)
else
  actual=$(shasum -a 256 "$tmp/$name.tar.gz" | cut -d' ' -f1)
fi
[ "$expected" = "$actual" ] || die "checksum mismatch for $name.tar.gz"

tar -xzf "$tmp/$name.tar.gz" -C "$tmp"
mkdir -p "$DIR"
install -m 0755 "$tmp/$name/bot-connect" "$DIR/bot-connect"

say "Installed $("$DIR/bot-connect" version) to $DIR/bot-connect"
case ":$PATH:" in
  *":$DIR:"*) ;;
  *) say "Note: $DIR is not on your PATH. Add it, e.g.:  export PATH=\"$DIR:\$PATH\"" ;;
esac
say "Next: create a config (see https://github.com/$REPO/blob/$version/INSTALL.md), then run: bot-connect -config config.toml"
