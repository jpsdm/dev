#!/bin/sh
# Builds dev for this Linux or macOS computer and packs it for install.sh:
#   dist/dev-<os>-<arch>.tar.gz   (plus install.sh beside it)
# Give the folder to someone (or upload it and set DEV_DOWNLOAD_URL) and they run: sh install.sh
set -eu
cd "$(dirname "$0")/.."
case "$(uname -s)" in Linux) OS=linux ;; Darwin) OS=macos ;; *) echo "run on Linux or macOS" >&2; exit 1 ;; esac
case "$(uname -m)" in x86_64 | amd64) ARCH=x86_64 ;; arm64 | aarch64) ARCH=aarch64 ;; *) ARCH=$(uname -m) ;; esac
command -v cargo >/dev/null 2>&1 || PATH="$HOME/.cargo/bin:$PATH"
cargo build --release
mkdir -p dist
tar -czf "dist/dev-$OS-$ARCH.tar.gz" -C target/release dev
cp install.sh dist/install.sh
echo "Built dist/dev-$OS-$ARCH.tar.gz"
