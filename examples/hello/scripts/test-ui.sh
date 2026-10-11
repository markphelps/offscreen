#!/bin/sh
set -eu

: "${OFFSCREEN_CACHE:?OFFSCREEN_CACHE must be set by offscreen}"
: "${OFFSCREEN_OUT:?OFFSCREEN_OUT must be set by offscreen}"
mkdir -p "$OFFSCREEN_CACHE" "$OFFSCREEN_OUT"

xcodebuild \
    -project Hello.xcodeproj \
    -scheme Hello \
    -configuration Debug \
    -destination 'platform=macOS' \
    -derivedDataPath "$OFFSCREEN_CACHE/HelloDerivedData" \
    -resultBundlePath "$OFFSCREEN_OUT/Hello.xcresult" \
    -parallel-testing-enabled NO \
    test
