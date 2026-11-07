#!/usr/bin/env bash
#
# Cross-compile Hasherino's Gio UI (main.go) for Windows.
#
# Run from the repository checkout with the Windows dev shell active:
#
#   nix develop .#windows -c ./scripts/build-windows.sh
#
# An optional first argument sets the output path of the .exe file.

set -euo pipefail

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
DIST_DIR="$ROOT_DIR/dist"
CACHE_DIR="${XDG_CACHE_HOME:-$HOME/.cache}/hasherino-tools"
APP_ID="com.hashysoftware.hasherino"
GOGIO_VERSION="v0.10.3"
OUTPUT="${1:-$DIST_DIR/hasherino-windows-amd64.exe}"
if [[ "$OUTPUT" != /* ]]; then
	OUTPUT="$ROOT_DIR/$OUTPUT"
fi
ICON="$DIST_DIR/hasherino.png"

missing=()
for tool in go x86_64-w64-mingw32-gcc x86_64-w64-mingw32-g++; do
	if ! command -v "$tool" >/dev/null 2>&1; then
		missing+=("$tool")
	fi
done
if (( ${#missing[@]} )); then
	echo "error: missing ${missing[*]}; run from 'nix develop .#windows'" >&2
	exit 1
fi

mkdir -p "$DIST_DIR" "$CACHE_DIR/bin" "$(dirname "$OUTPUT")"

# Build gogio on the host first, then use it with the Windows cross compiler.
GOBIN="$CACHE_DIR/bin" go install "gioui.org/cmd/gogio@$GOGIO_VERSION"
GOGIO="$CACHE_DIR/bin/gogio"

if command -v rsvg-convert >/dev/null 2>&1; then
	rsvg-convert -w 512 -h 512 \
		"$ROOT_DIR/flatpak/com.hashysoftware.hasherino.svg" \
		-o "$ICON"
fi

export CGO_ENABLED=1
export GOARCH=amd64
export CC=x86_64-w64-mingw32-gcc
export CXX=x86_64-w64-mingw32-g++

# gogio needs a package directory rather than a single file. Stage a clean
# source tree with only the Gio entry point so the Fyne main is not included.
STAGE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/hasherino-windows-XXXXXX")"
trap 'rm -rf "$STAGE_DIR"' EXIT
cp "$ROOT_DIR/main.go" "$ROOT_DIR/go.mod" "$ROOT_DIR/go.sum" "$STAGE_DIR/"
cp -R "$ROOT_DIR/hasherino" "$STAGE_DIR/hasherino"

cd "$STAGE_DIR"
args=(-target windows -appid "$APP_ID" -name Hasherino -arch amd64 -o "$OUTPUT")
if [[ -f "$ICON" ]]; then
	args+=(-icon "$ICON")
fi
"$GOGIO" "${args[@]}" .

echo "Built $OUTPUT"
