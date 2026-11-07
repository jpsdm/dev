#!/bin/sh
# dev installer for Linux and macOS.
#
#   curl -fsSL https://github.com/OWNER/REPO/releases/latest/download/install.sh | sh
#   sh install.sh                 install dev and set up bash, zsh, fish (and pwsh if present)
#   sh install.sh --uninstall     remove it again (tools and settings stay; add --purge to drop them)
#
# Where the dev program comes from, first match wins:
#   1. DEV_BINARY=/path/to/dev
#   2. a `dev` program next to this script, or dist/dev-<os>-<arch>[.tar.gz] next to it
#   3. DEV_DOWNLOAD_URL=https://host/path  ->  <url>/dev-<os>-<arch>.tar.gz
#   4. the GitHub release of DEV_REPO (filled in by the release workflow); DEV_VERSION picks one
#   5. this script's folder holds dev's source (Cargo.toml) and Rust is installed: build it
#      (DEV_FROM_SOURCE=1 makes this the first choice)
#
# Settings: DEV_INSTALL_DIR (default ~/.local/bin), DEV_NO_MODIFY_PATH=1, DEV_NO_SHELLS=1.
# Works with sh, bash, dash and zsh; run it as `sh install.sh` or `bash install.sh`.

set -eu

# Replaced with owner/name by the release workflow (.github/workflows/release.yml).
DEV_REPO="${DEV_REPO:-__DEV_REPO__}"
case "$DEV_REPO" in __*__) DEV_REPO="" ;; esac

say() { printf '%s\n' "dev-install: $*"; }
die() { printf '%s\n' "dev-install: error: $*" >&2; exit 1; }
has() { command -v "$1" >/dev/null 2>&1; }

# --- where we are -----------------------------------------------------------
case "$(uname -s)" in
  Linux) OS=linux ;;
  Darwin) OS=macos ;;
  *) die "this script is for Linux and macOS; on Windows run dev-setup.exe" ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) ARCH=x86_64 ;;
  arm64 | aarch64) ARCH=aarch64 ;;
  *) die "unsupported processor: $(uname -m)" ;;
esac

SCRIPT_DIR=""
case "$0" in
  */*) SCRIPT_DIR=$(cd "$(dirname "$0")" 2>/dev/null && pwd) || SCRIPT_DIR="" ;;
  *) [ -f "./install.sh" ] && SCRIPT_DIR=$(pwd) ;;
esac

INSTALL_DIR="${DEV_INSTALL_DIR:-$HOME/.local/bin}"
UNINSTALL=0
PURGE=0
for a in "$@"; do
  case "$a" in
    --uninstall) UNINSTALL=1 ;;
    --purge) PURGE=1 ;;
    -h | --help) sed -n '2,19p' "$0" 2>/dev/null || echo "see https://github.com/${DEV_REPO:-OWNER/REPO}#install"; exit 0 ;;
    *) die "unknown option: $a" ;;
  esac
done

# --- what dev needs at run time -------------------------------------------
for t in curl tar git; do
  has "$t" || say "warning: '$t' was not found; dev uses it (install it with your package manager)"
done

WORK=$(mktemp -d 2>/dev/null || mktemp -d -t dev-install)
trap 'rm -rf "$WORK"' EXIT INT TERM

download() {
  if has curl; then
    curl -fsSL --retry 2 -o "$2" "$1"
  elif has wget; then
    wget -q -O "$2" "$1"
  else
    die "curl or wget is needed to download dev"
  fi
}

unpack_to_work() {
  tar -xzf "$1" -C "$WORK" || die "could not unpack $1"
  found=$(find "$WORK" -type f -name dev | head -n 1)
  [ -n "$found" ] || die "no 'dev' program inside $1"
  BIN="$found"
}

build_from_source() {
  if ! has cargo && [ -x "$HOME/.cargo/bin/cargo" ]; then
    PATH="$HOME/.cargo/bin:$PATH"
  fi
  has cargo || die "Rust is needed to build dev from source: install it from https://rustup.rs, then run this again"
  say "building dev from source (this takes a minute)"
  (cd "$SCRIPT_DIR" && cargo build --release) || die "the build failed"
  BIN="$SCRIPT_DIR/target/release/dev"
}

# --- find or make the dev program -----------------------------------------
BIN=""
if [ "${DEV_FROM_SOURCE:-}" = "1" ] && [ -n "$SCRIPT_DIR" ] && [ -f "$SCRIPT_DIR/Cargo.toml" ]; then
  build_from_source
elif [ -n "${DEV_BINARY:-}" ]; then
  [ -f "$DEV_BINARY" ] || die "DEV_BINARY=$DEV_BINARY does not exist"
  BIN="$DEV_BINARY"
elif [ -n "$SCRIPT_DIR" ] && [ -f "$SCRIPT_DIR/dev" ] && [ ! -d "$SCRIPT_DIR/dev" ]; then
  BIN="$SCRIPT_DIR/dev"
elif [ -n "$SCRIPT_DIR" ] && [ -f "$SCRIPT_DIR/dist/dev-$OS-$ARCH" ]; then
  BIN="$SCRIPT_DIR/dist/dev-$OS-$ARCH"
elif [ -n "$SCRIPT_DIR" ] && [ -f "$SCRIPT_DIR/dist/dev-$OS-$ARCH.tar.gz" ]; then
  unpack_to_work "$SCRIPT_DIR/dist/dev-$OS-$ARCH.tar.gz"
elif [ -n "${DEV_DOWNLOAD_URL:-}" ]; then
  url="${DEV_DOWNLOAD_URL%/}/dev-$OS-$ARCH.tar.gz"
  say "downloading $url"
  download "$url" "$WORK/dev.tar.gz"
  unpack_to_work "$WORK/dev.tar.gz"
elif [ -n "$DEV_REPO" ]; then
  if [ -n "${DEV_VERSION:-}" ]; then
    base="https://github.com/$DEV_REPO/releases/download/v${DEV_VERSION#v}"
  else
    base="https://github.com/$DEV_REPO/releases/latest/download"
  fi
  say "downloading $base/dev-$OS-$ARCH.tar.gz"
  download "$base/dev-$OS-$ARCH.tar.gz" "$WORK/dev.tar.gz"
  if download "$base/SHA256SUMS.txt" "$WORK/SHA256SUMS.txt" 2>/dev/null; then
    want=$(grep " dev-$OS-$ARCH.tar.gz\$" "$WORK/SHA256SUMS.txt" | cut -d' ' -f1)
    if has sha256sum; then got=$(sha256sum "$WORK/dev.tar.gz" | cut -d' ' -f1); else got=$(shasum -a 256 "$WORK/dev.tar.gz" | cut -d' ' -f1); fi
    [ -z "$want" ] || [ "$want" = "$got" ] || die "checksum mismatch for dev-$OS-$ARCH.tar.gz"
  fi
  unpack_to_work "$WORK/dev.tar.gz"
elif [ -n "$SCRIPT_DIR" ] && [ -f "$SCRIPT_DIR/Cargo.toml" ]; then
  build_from_source
else
  die "no dev program found: put one next to this script, set DEV_BINARY, DEV_DOWNLOAD_URL or DEV_REPO, or run it from dev's source folder"
fi

chmod +x "$BIN" 2>/dev/null || true
"$BIN" --version >/dev/null 2>&1 || die "$BIN does not run on this computer ($OS-$ARCH)"

# --- install / uninstall ----------------------------------------------------
set -- --dir "$INSTALL_DIR"
[ "${DEV_NO_MODIFY_PATH:-}" = "1" ] && set -- "$@" --no-modify-path
[ "${DEV_NO_SHELLS:-}" = "1" ] && set -- "$@" --no-shells
[ "$UNINSTALL" = "1" ] && set -- "$@" --uninstall
[ "$PURGE" = "1" ] && set -- "$@" --purge

DEV_YES=1 "$BIN" setup "$@"

if [ "$UNINSTALL" != "1" ]; then
  echo
  say "done. Open a new terminal (or run: exec \$SHELL -l) to start using dev."
fi
