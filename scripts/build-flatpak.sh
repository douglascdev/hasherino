#!/usr/bin/env bash
#
# Build a single-file Flatpak bundle for Hasherino's Gio UI (main.go).
#
# Run from the repository checkout with the Nix dev shell active:
#
#   nix develop -c ./scripts/build-flatpak.sh
#
# An optional first argument sets the output path of the .flatpak bundle.

set -euo pipefail

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
APP_ID="com.hashysoftware.hasherino"
MANIFEST="$ROOT_DIR/flatpak/$APP_ID.yml"
CACHE_DIR="${XDG_CACHE_HOME:-$HOME/.cache}/hasherino-flatpak"
BUILD_DIR="$CACHE_DIR/build"
STATE_DIR="$CACHE_DIR/state"
REPO_DIR="$CACHE_DIR/repo"
OUTPUT="${1:-$ROOT_DIR/$APP_ID.flatpak}"
FLATHUB_REPO="https://dl.flathub.org/repo/flathub.flatpakrepo"

if [[ ! -f "$MANIFEST" ]]; then
	echo "error: manifest not found at $MANIFEST" >&2
	exit 1
fi

missing=()
for tool in flatpak flatpak-builder; do
	if ! command -v "$tool" >/dev/null 2>&1; then
		missing+=("$tool")
	fi
done
if (( ${#missing[@]} )); then
	echo "error: missing ${missing[*]}; run './build-flatpak.sh' from 'nix develop'" >&2
	exit 1
fi

if ! flatpak remotes --columns=name 2>/dev/null | grep -qx flathub; then
	echo "Adding the Flathub remote for the current user..."
	flatpak remote-add --user --if-not-exists flathub "$FLATHUB_REPO"
fi

mkdir -p "$CACHE_DIR" "$REPO_DIR" "$(dirname "$OUTPUT")"

# Install the runtime, SDK, and Go SDK extension declared in the manifest.
flatpak-builder --user --assumeyes --install-deps-from=flathub \
	--install-deps-only --state-dir="$STATE_DIR" "$BUILD_DIR" "$MANIFEST"

# Build the Gio application (main.go only) and export it to the local repo.
flatpak-builder --user --assumeyes --force-clean --disable-rofiles-fuse \
	--state-dir="$STATE_DIR" --repo="$REPO_DIR" \
	"$BUILD_DIR" "$MANIFEST"

ARCH="$(flatpak --default-arch)"
rm -f "$OUTPUT"
flatpak build-bundle --arch="$ARCH" --runtime-repo="$FLATHUB_REPO" \
	"$REPO_DIR" "$OUTPUT" "$APP_ID" master

echo
echo "Flatpak bundle: $OUTPUT"
echo "Install with:   flatpak install --user \"$OUTPUT\""
