#!/bin/bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
bindir=$(cd "$root" && swift build --show-bin-path)
app="$root/.build/OffscreenHelper.app"
mkdir -p "$app/Contents/MacOS"
cp "$bindir/OffscreenHelper" "$app/Contents/MacOS/OffscreenHelper"
cat > "$app/Contents/Info.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>dev.offscreen.helper</string>
<key>CFBundleName</key><string>Offscreen Helper</string>
<key>CFBundleExecutable</key><string>OffscreenHelper</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>CFBundleVersion</key><string>1</string>
<key>CFBundleShortVersionString</key><string>1.0</string>
<key>LSUIElement</key><true/>
<key>NSHumanReadableCopyright</key><string>Offscreen</string>
</dict></plist>
PLIST
codesign --force --sign - --identifier dev.offscreen.helper \
  -r '=designated => identifier "dev.offscreen.helper"' "$app"
codesign --verify --strict "$app"
printf '%s\n' "$app"
