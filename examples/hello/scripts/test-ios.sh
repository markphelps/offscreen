#!/bin/sh
set -eu

: "${OFFSCREEN_CACHE:?OFFSCREEN_CACHE must be set by offscreen}"
: "${OFFSCREEN_OUT:?OFFSCREEN_OUT must be set by offscreen}"
mkdir -p "$OFFSCREEN_CACHE" "$OFFSCREEN_OUT"

# Device names change between Xcode releases; use the first available iPhone.
device=$(xcrun simctl list devices available | sed -n 's/^ *\(iPhone[^(]*[^ (]\) (.*/\1/p' | head -n 1)
if [ -z "$device" ]; then
    printf 'No available iPhone simulator in this image\n' >&2
    exit 1
fi
printf 'Simulator: %s\n' "$device"

# Same index-store workaround as test-ui.sh; see there.
xcodebuild \
    -project Hello.xcodeproj \
    -scheme Hello \
    -configuration Debug \
    -destination "platform=iOS Simulator,name=$device" \
    -derivedDataPath "$OFFSCREEN_CACHE/HelloDerivedDataIOS" \
    -resultBundlePath "$OFFSCREEN_OUT/HelloIOS.xcresult" \
    -parallel-testing-enabled NO \
    COMPILER_INDEX_STORE_ENABLE=NO \
    ONLY_ACTIVE_ARCH=YES \
    test
