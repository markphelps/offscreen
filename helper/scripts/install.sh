#!/bin/bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
app="$HOME/Applications/OffscreenHelper.app"
plist="$HOME/Library/LaunchAgents/dev.offscreen.helper.plist"
label=dev.offscreen.helper
domain="gui/$(id -u)"
mode=${1:-install}
if [[ "$mode" != install && "$mode" != uninstall ]]; then
  printf 'Usage: %s [install|uninstall]\n' "$0" >&2
  exit 2
fi
if [[ -f "$plist" ]] && ! /usr/libexec/PlistBuddy -c 'Print :Label' "$plist" 2>/dev/null | /usr/bin/grep -qx "$label"; then
  printf 'Refusing to replace a LaunchAgent not owned by Offscreen: %s\n' "$plist" >&2
  exit 1
fi
if [[ -d "$app" ]] && [[ "$(/usr/libexec/PlistBuddy -c 'Print :CFBundleIdentifier' "$app/Contents/Info.plist" 2>/dev/null || true)" != "$label" ]]; then
  printf 'Refusing to replace an app not owned by Offscreen: %s\n' "$app" >&2
  exit 1
fi
if [[ "$mode" == install ]]; then "$root/scripts/bundle.sh" >/dev/null; fi
if [[ -f "$plist" ]]; then
  launchctl bootout "$domain" "$plist" || true
  rm "$plist"
fi
if [[ "$mode" == uninstall ]]; then
  if [[ -d "$app" ]]; then rm -rf "$app"; fi
  printf 'Removed Offscreen helper (daemon and other agents unchanged).\n'
  exit 0
fi
mkdir -p "$HOME/Applications" "$HOME/Library/LaunchAgents"
if [[ -d "$app" ]]; then rm -rf "$app"; fi
/usr/bin/ditto "$root/.build/OffscreenHelper.app" "$app"
codesign --verify --strict "$app"
/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister -f "$app"
# Escape the only interpolated XML string. A default install uses the user's
# ordinary state directory; isolated integration tests can export a short path.
escape_xml() {
  local value=$1
  value=${value//&/\&amp;}
  value=${value//</\&lt;}
  value=${value//>/\&gt;}
  value=${value//\"/\&quot;}
  printf '%s' "$value"
}
state_env=''
if [[ -n "${OFFSCREEN_STATE_DIR:-}" ]]; then
  state_env="<key>EnvironmentVariables</key><dict><key>OFFSCREEN_STATE_DIR</key><string>$(escape_xml "$OFFSCREEN_STATE_DIR")</string></dict>"
fi
cat > "$plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>$label</string>
<key>ProgramArguments</key><array><string>$(escape_xml "$app/Contents/MacOS/OffscreenHelper")</string></array>
<key>RunAtLoad</key><true/><key>KeepAlive</key><true/>
$state_env
</dict></plist>
PLIST
chmod 600 "$plist"
launchctl bootstrap "$domain" "$plist"
printf 'Installed %s. Allow Input Monitoring and notifications for Offscreen Helper in System Settings.\n' "$app"
