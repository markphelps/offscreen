#!/bin/bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
bindir=$(cd "$root" && swift build --show-bin-path)
app="$root/.build/Offscreen.app"
label=io.latentsignal.offscreen
# The menu compares this with the daemon's build (diagnostics only, as in
# scripts/install.sh) to say when the daemon needs a restart.
build=$(git -C "$root" describe --tags --always --dirty 2>/dev/null || echo dev)
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
cp "$bindir/Offscreen" "$app/Contents/MacOS/Offscreen"
cp "$root/Resources/AppIcon.icns" "$app/Contents/Resources/AppIcon.icns"
cat > "$app/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>$label</string>
<key>CFBundleName</key><string>Offscreen</string>
<key>CFBundleDisplayName</key><string>Offscreen</string>
<key>CFBundleExecutable</key><string>Offscreen</string>
<key>CFBundleIconFile</key><string>AppIcon</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>CFBundleVersion</key><string>1</string>
<key>CFBundleShortVersionString</key><string>1.0</string>
<key>LSUIElement</key><true/>
<key>NSHumanReadableCopyright</key><string>Offscreen</string>
<key>OffscreenBuild</key><string>$build</string>
</dict></plist>
PLIST
codesign --force --sign - --identifier "$label" \
  -r "=designated => identifier \"$label\"" "$app"
codesign --verify --strict "$app"
printf '%s\n' "$app"
