#!/bin/sh
# Optional [image] setup script: boot an iPhone simulator once while building
# the golden image, so each job's first boot skips device setup and the
# simulator's dyld cache build. Pair with test-ios.sh, which picks the same device.
set -eu

device=$(xcrun simctl list devices available | sed -n 's/^ *\(iPhone[^(]*[^ (]\) (.*/\1/p' | head -n 1)
if [ -z "$device" ]; then
    printf 'No available iPhone simulator in this image\n' >&2
    exit 1
fi
printf 'Warming simulator: %s\n' "$device"
xcrun simctl bootstatus "$device" -b
xcrun simctl shutdown "$device"
