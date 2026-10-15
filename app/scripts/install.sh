#!/bin/bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
app="$HOME/Applications/Offscreen.app"
plist="$HOME/Library/LaunchAgents/io.latentsignal.offscreen.plist"
label=io.latentsignal.offscreen
# Installs from before the rename used these names. Remove them when they are ours.
legacy_app="$HOME/Applications/OffscreenHelper.app"
legacy_plist="$HOME/Library/LaunchAgents/dev.offscreen.helper.plist"
legacy_label=dev.offscreen.helper
domain="gui/$(id -u)"
mode=${1:-install}
if [[ "$mode" != install && "$mode" != uninstall ]]; then
  printf 'Usage: %s [install|uninstall]\n' "$0" >&2
  exit 2
fi
# owned_agent PLIST LABEL and owned_app APP IDENTIFIER succeed when the path
# is missing or carries our identity, and refuse anything else.
owned_agent() {
  if [[ -f "$1" ]] && ! /usr/libexec/PlistBuddy -c 'Print :Label' "$1" 2>/dev/null | /usr/bin/grep -qx "$2"; then
    printf 'Refusing to replace a LaunchAgent not owned by Offscreen: %s\n' "$1" >&2
    return 1
  fi
}
owned_app() {
  if [[ -d "$1" ]] && [[ "$(/usr/libexec/PlistBuddy -c 'Print :CFBundleIdentifier' "$1/Contents/Info.plist" 2>/dev/null || true)" != "$2" ]]; then
    printf 'Refusing to replace an app not owned by Offscreen: %s\n' "$1" >&2
    return 1
  fi
}
owned_agent "$plist" "$label"
owned_app "$app" "$label"
owned_agent "$legacy_plist" "$legacy_label"
owned_app "$legacy_app" "$legacy_label"
if [[ "$mode" == install ]]; then "$root/scripts/bundle.sh" >/dev/null; fi
for agent in "$plist" "$legacy_plist"; do
  if [[ -f "$agent" ]]; then
    launchctl bootout "$domain" "$agent" || true
    rm "$agent"
  fi
done
if [[ -d "$legacy_app" ]]; then rm -rf "$legacy_app"; fi
if [[ "$mode" == uninstall ]]; then
  if [[ -d "$app" ]]; then rm -rf "$app"; fi
  printf 'Removed the Offscreen menu bar app (daemon and other agents unchanged).\n'
  exit 0
fi
mkdir -p "$HOME/Applications" "$HOME/Library/LaunchAgents"
if [[ -d "$app" ]]; then rm -rf "$app"; fi
/usr/bin/ditto "$root/.build/Offscreen.app" "$app"
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
<key>ProgramArguments</key><array><string>$(escape_xml "$app/Contents/MacOS/Offscreen")</string></array>
<key>RunAtLoad</key><true/><key>KeepAlive</key><true/>
$state_env
</dict></plist>
PLIST
chmod 600 "$plist"
launchctl bootstrap "$domain" "$plist"
printf 'Installed %s. Allow Input Monitoring and notifications for Offscreen in System Settings.\n' "$app"
