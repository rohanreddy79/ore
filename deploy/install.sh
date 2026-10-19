#!/bin/sh
# Install the ore binary from a release: downloads the archive for this OS and
# architecture, verifies it against SHA256SUMS, and places `ore` in
# ~/.local/bin (or /usr/local/bin when that is writable or --system is given).
# No sudo is run for you: if the target is not writable the script says so and
# stops.
#
#   curl -fsSL https://example.com/ore/install.sh | sh
#   ORE_VERSION=0.1.0 ORE_INSTALL_URL=https://example.com/ore/releases ORE_INSTALL_DIR=/opt/bin sh install.sh
#
# ORE_INSTALL_URL may be a file:// URL (a release directory on disk) for air-gapped installs.
set -eu

ORE_INSTALL_URL="${ORE_INSTALL_URL:-https://get.ore.dev/releases}"
ORE_VERSION="${ORE_VERSION:-}"
ORE_INSTALL_DIR="${ORE_INSTALL_DIR:-}"
SYSTEM=0
for a in "$@"; do
  case "$a" in
    --system) SYSTEM=1 ;;
    --dir=*) ORE_INSTALL_DIR="${a#--dir=}" ;;
    --version=*) ORE_VERSION="${a#--version=}" ;;
    --url=*) ORE_INSTALL_URL="${a#--url=}" ;;
    -h|--help) sed -n '2,12p' "$0"; exit 0 ;;
    *) echo "install.sh: unknown argument $a" >&2; exit 2 ;;
  esac
done

say() { printf '%s\n' "$*" >&2; }
die() { say "install.sh: $*"; exit 1; }

need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required"; }
need tar
if command -v curl >/dev/null 2>&1; then
  fetch() { curl -fsSL "$1" -o "$2"; }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget -q "$1" -O "$2"; }
else
  die "curl or wget is required"
fi
if command -v sha256sum >/dev/null 2>&1; then
  sum() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
  sum() { shasum -a 256 "$1" | cut -d' ' -f1; }
else
  die "sha256sum or shasum is required"
fi

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in linux|darwin) ;; *) die "unsupported OS $os (linux and darwin releases only)";; esac
arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) die "unsupported architecture $arch" ;;
esac

work=$(mktemp -d)
trap 'rm -rf "${work:?}"' EXIT

if [ -z "$ORE_VERSION" ]; then
  if fetch "$ORE_INSTALL_URL/VERSION" "$work/VERSION" 2>/dev/null; then
    ORE_VERSION=$(tr -d '[:space:]' < "$work/VERSION")
  fi
  [ -n "$ORE_VERSION" ] || die "set ORE_VERSION (no $ORE_INSTALL_URL/VERSION found)"
fi

# choose the install directory before downloading, so a permission problem is the first thing you hear
if [ -z "$ORE_INSTALL_DIR" ]; then
  if [ "$SYSTEM" = 1 ]; then
    ORE_INSTALL_DIR=/usr/local/bin
  elif [ -d /usr/local/bin ] && [ -w /usr/local/bin ]; then
    ORE_INSTALL_DIR=/usr/local/bin
  else
    ORE_INSTALL_DIR="${HOME:?HOME is not set}/.local/bin"
  fi
fi
mkdir -p "$ORE_INSTALL_DIR" 2>/dev/null || die "cannot create $ORE_INSTALL_DIR: no permission. Re-run with ORE_INSTALL_DIR=<a directory you can write>, or as a user that can write it; this script never runs sudo for you."
[ -w "$ORE_INSTALL_DIR" ] || die "$ORE_INSTALL_DIR is not writable by $(id -un). Re-run with ORE_INSTALL_DIR=<a directory you can write> (for example \$HOME/.local/bin), or as a user that can write it; this script never runs sudo for you."

name="ore_${ORE_VERSION}_${os}_${arch}"
say "downloading $name.tar.gz from $ORE_INSTALL_URL"
fetch "$ORE_INSTALL_URL/$name.tar.gz" "$work/$name.tar.gz" || die "download failed: $ORE_INSTALL_URL/$name.tar.gz"
fetch "$ORE_INSTALL_URL/SHA256SUMS" "$work/SHA256SUMS" || die "download failed: $ORE_INSTALL_URL/SHA256SUMS"
expected=$(grep " $name.tar.gz\$" "$work/SHA256SUMS" | cut -d' ' -f1 | head -1)
[ -n "$expected" ] || die "$name.tar.gz is not listed in SHA256SUMS"
actual=$(sum "$work/$name.tar.gz")
[ "$expected" = "$actual" ] || die "checksum mismatch for $name.tar.gz: expected $expected, got $actual"
say "checksum verified"
tar -xzf "$work/$name.tar.gz" -C "$work"
install -m 0755 "$work/$name/ore" "$ORE_INSTALL_DIR/ore.tmp.$$"
mv -f "$ORE_INSTALL_DIR/ore.tmp.$$" "$ORE_INSTALL_DIR/ore"
say "installed $("$ORE_INSTALL_DIR/ore" version) to $ORE_INSTALL_DIR/ore"
case ":$PATH:" in
  *":$ORE_INSTALL_DIR:"*) ;;
  *) say "note: $ORE_INSTALL_DIR is not on your PATH; add it, e.g.  export PATH=\"$ORE_INSTALL_DIR:\$PATH\"" ;;
esac
