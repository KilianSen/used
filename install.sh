#!/bin/sh
# Install used on macOS or Linux and start it at login (no root needed):
#
#   curl -fsSL https://raw.githubusercontent.com/KilianSen/used/main/install.sh | sh
#
# Env: USED_VERSION (default: latest), USED_BIN_DIR (default: ~/.local/bin),
#      USED_REPO (default: KilianSen/used), USED_NO_SERVICE=1 to skip `used install`.
set -eu

repo="${USED_REPO:-KilianSen/used}"
version="${USED_VERSION:-latest}"
bin_dir="${USED_BIN_DIR:-$HOME/.local/bin}"

say() { printf '\033[1m%s\033[0m\n' "$*"; }
die() { printf 'used install: %s\n' "$*" >&2; exit 1; }

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in darwin | linux) ;; *) die "unsupported OS: $os (use install.ps1 on Windows)" ;; esac
case "$(uname -m)" in
	x86_64 | amd64) arch=amd64 ;;
	arm64 | aarch64) arch=arm64 ;;
	*) die "unsupported architecture: $(uname -m)" ;;
esac

if [ "$version" = latest ]; then
	base="https://github.com/$repo/releases/latest/download"
else
	base="https://github.com/$repo/releases/download/$version"
fi
asset="used_${os}_${arch}.tar.gz"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

say "downloading $asset ($version)"
curl -fsSL "$base/$asset" -o "$tmp/$asset" || die "download failed: $base/$asset"
curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt" || die "checksums.txt missing"

say "verifying checksum"
(
	cd "$tmp"
	grep " $asset\$" checksums.txt >expected || die "$asset not listed in checksums.txt"
	if command -v sha256sum >/dev/null; then sha256sum -c expected >/dev/null; else shasum -a 256 -c expected >/dev/null; fi
) || die "checksum mismatch"

tar -xzf "$tmp/$asset" -C "$tmp" used
mkdir -p "$bin_dir"
install -m 0755 "$tmp/used" "$bin_dir/used"
say "installed $bin_dir/used"

if [ "${USED_NO_SERVICE:-}" != 1 ]; then
	"$bin_dir/used" install
fi

case ":$PATH:" in
	*":$bin_dir:"*) ;;
	*) printf '\nAdd %s to your PATH to use the "used" command.\n' "$bin_dir" ;;
esac
