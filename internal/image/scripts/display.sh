#!/bin/bash
set -euo pipefail

width=$1
height=$2
unit=$3
folder="$HOME/Library/Application Support/offscreen"
source="$folder/display.swift"
selector="$folder/display-selector"

# Xcode is required by the base image; compile for the guest, not the host.
/usr/bin/xcrun swiftc "$source" -o "$selector"
"$selector" --install "$width" "$height" "$unit"
"$selector" "$width" "$height" "$unit"

# CGDisplayCopyDisplayMode alone cannot prove the framebuffer seen by captures.
shot=$(mktemp /tmp/offscreen-display.XXXXXX.png)
trap 'rm -f "$shot"' EXIT
/usr/sbin/screencapture -x "$shot"
size=$(/usr/bin/sips -g pixelWidth -g pixelHeight "$shot")
expected_width=$width
expected_height=$height
if [[ "$unit" == pt ]]; then
  expected_width=$((width * 2))
  expected_height=$((height * 2))
fi
if ! grep -Eq "^[[:space:]]*pixelWidth: ${expected_width}$" <<< "$size" ||
   ! grep -Eq "^[[:space:]]*pixelHeight: ${expected_height}$" <<< "$size"; then
  printf 'offscreen display: screenshot does not match %sx%s pixels:\n%s\n' "$expected_width" "$expected_height" "$size" >&2
  exit 1
fi
printf 'offscreen display: screenshot verified %sx%s pixels\n' "$expected_width" "$expected_height"
