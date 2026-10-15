#!/bin/bash
# Usage: bundle.sh [VERSION]
# Without VERSION, bundles the debug build that `swift build` left behind, for
# local use. With VERSION (GoReleaser passes {{ .Version }}), makes a release
# build and stamps VERSION into the bundle.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
app="$root/.build/Offscreen.app"
label=io.latentsignal.offscreen
if [[ $# -gt 0 ]]; then
  # The menu compares OffscreenBuild with the daemon's build (diagnostics only,
  # as in scripts/install.sh) to say when the daemon needs a restart, so a
  # release passes the same version GoReleaser gives the Go binaries.
  build=$1
  # Finder and brew show this; keep the numeric x.y.z part of the version.
  short=${build#v}
  short=${short%%[-+]*}
  bundle_version=$short
  swift build --package-path "$root" -c release
  bindir=$(swift build --package-path "$root" -c release --show-bin-path)
else
  build=$(git -C "$root" describe --tags --always --dirty 2>/dev/null || echo dev)
  short=1.0
  bundle_version=1
  bindir=$(cd "$root" && swift build --show-bin-path)
fi
rm -rf "$app"
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
<key>CFBundleVersion</key><string>$bundle_version</string>
<key>CFBundleShortVersionString</key><string>$short</string>
<key>LSUIElement</key><true/>
<key>NSHumanReadableCopyright</key><string>Offscreen</string>
<key>OffscreenBuild</key><string>$build</string>
</dict></plist>
PLIST
# Start at Login (the menu's checkbox) registers this agent with
# SMAppService.agent, which reads it only from inside the signed bundle, so
# write it before codesign. Same rules as scripts/install.sh's agent: launchd
# relaunches the app after a crash, not after Quit Offscreen (exit 0).
mkdir -p "$app/Contents/Library/LaunchAgents"
cat > "$app/Contents/Library/LaunchAgents/$label.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>$label</string>
<key>BundleProgram</key><string>Contents/MacOS/Offscreen</string>
<key>RunAtLoad</key><true/>
<key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
</dict></plist>
PLIST
plutil -lint -s "$app/Contents/Library/LaunchAgents/$label.plist"
codesign --force --sign - --identifier "$label" \
  -r "=designated => identifier \"$label\"" "$app"
codesign --verify --strict "$app"
printf '%s\n' "$app"
