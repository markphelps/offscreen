#!/bin/bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
bindir=$(cd "$root" && swift build --show-bin-path)
app="$root/.build/Offscreen.app"
label=io.latentsignal.offscreen
mkdir -p "$app/Contents/MacOS"
cp "$bindir/Offscreen" "$app/Contents/MacOS/Offscreen"
cat > "$app/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>$label</string>
<key>CFBundleName</key><string>Offscreen</string>
<key>CFBundleDisplayName</key><string>Offscreen</string>
<key>CFBundleExecutable</key><string>Offscreen</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>CFBundleVersion</key><string>1</string>
<key>CFBundleShortVersionString</key><string>1.0</string>
<key>LSUIElement</key><true/>
<key>NSHumanReadableCopyright</key><string>Offscreen</string>
</dict></plist>
PLIST
codesign --force --sign - --identifier "$label" \
  -r "=designated => identifier \"$label\"" "$app"
codesign --verify --strict "$app"
printf '%s\n' "$app"
