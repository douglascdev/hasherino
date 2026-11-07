#!/usr/bin/env bash
#
# Build an Android APK for Hasherino's Gio UI (main.go).
#
# Run from the repository checkout with the Android dev shell active:
#
#   nix develop .#android -c ./scripts/build-android.sh
#
# Set ANDROID_ARCHS to override the default ABIs (arm64,arm).
# An optional first argument sets the output path of the .apk file.

set -euo pipefail

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
DIST_DIR="$ROOT_DIR/dist"
CACHE_DIR="${XDG_CACHE_HOME:-$HOME/.cache}/hasherino-tools"
APP_ID="com.hashysoftware.hasherino"
GOGIO_VERSION="v0.10.3"
ARCHS="${ANDROID_ARCHS:-arm64,arm}"
OUTPUT="${1:-$DIST_DIR/hasherino-android.apk}"
if [[ "$OUTPUT" != /* ]]; then
	OUTPUT="$ROOT_DIR/$OUTPUT"
fi
ICON="$DIST_DIR/hasherino.png"

missing=()
for tool in go javac keytool; do
	if ! command -v "$tool" >/dev/null 2>&1; then
		missing+=("$tool")
	fi
done
if (( ${#missing[@]} )); then
	echo "error: missing ${missing[*]}; run from 'nix develop .#android'" >&2
	exit 1
fi

if [[ -z "${ANDROID_HOME:-}" || ! -d "${ANDROID_HOME}" ]]; then
	echo "error: ANDROID_HOME is not set; run from 'nix develop .#android'" >&2
	exit 1
fi

mkdir -p "$DIST_DIR" "$CACHE_DIR/bin" "$(dirname "$OUTPUT")"

# Build gogio on the host first, then use it for the Android build.
GOBIN="$CACHE_DIR/bin" go install "gioui.org/cmd/gogio@$GOGIO_VERSION"
GOGIO="$CACHE_DIR/bin/gogio"

if command -v rsvg-convert >/dev/null 2>&1; then
	rsvg-convert -w 512 -h 512 \
		"$ROOT_DIR/flatpak/com.hashysoftware.hasherino.svg" \
		-o "$ICON"
fi

# gogio needs a package directory rather than a single file. Stage a clean
# source tree with only the Gio entry point so the Fyne main is not included.
STAGE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/hasherino-android-XXXXXX")"
trap 'rm -rf "$STAGE_DIR"' EXIT
cp "$ROOT_DIR/main.go" "$ROOT_DIR/go.mod" "$ROOT_DIR/go.sum" "$STAGE_DIR/"
cp -R "$ROOT_DIR/hasherino" "$STAGE_DIR/hasherino"

cd "$STAGE_DIR"
args=(
	-target android
	-appid "$APP_ID"
	-name Hasherino
	-arch "$ARCHS"
	-minsdk 23
	-o "$OUTPUT"
)
if [[ -f "$ICON" ]]; then
	args+=(-icon "$ICON")
fi
"$GOGIO" "${args[@]}" .

echo "Built $OUTPUT"
