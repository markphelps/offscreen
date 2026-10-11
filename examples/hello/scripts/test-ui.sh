#!/bin/sh
set -eu

: "${OFFSCREEN_CACHE:?OFFSCREEN_CACHE must be set by offscreen}"
: "${OFFSCREEN_OUT:?OFFSCREEN_OUT must be set by offscreen}"
mkdir -p "$OFFSCREEN_CACHE" "$OFFSCREEN_OUT"

# Disable index-store writes: VirtioFS does not support their atomic renames.
# Derived data stays in the per-project cache across VM jobs.
xcodebuild \
    -project Hello.xcodeproj \
    -scheme Hello \
    -configuration Debug \
    -destination 'platform=macOS' \
    -derivedDataPath "$OFFSCREEN_CACHE/HelloDerivedData" \
    -resultBundlePath "$OFFSCREEN_OUT/Hello.xcresult" \
    -parallel-testing-enabled NO \
    COMPILER_INDEX_STORE_ENABLE=NO \
    test
