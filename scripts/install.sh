#!/bin/bash
# Install or uninstall all three Offscreen components: the offscreen CLI (plus
# an `ofs` shortcut symlink), the offscreend daemon and its LaunchAgent, and the
# Offscreen menu bar app with its login LaunchAgent. Builds everything before
# stopping anything, so a failed build leaves the current install running.
# Builds to a fixed directory so GOBIN/GOPATH never matter.
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
bin=${OFFSCREEN_BIN_DIR:-$HOME/.local/bin}
mode=${1:-install}
app="$HOME/Applications/Offscreen.app"
label=io.latentsignal.offscreen
plist="$HOME/Library/LaunchAgents/$label.plist"
# Installs from before the rename used these names. Remove them when they are
# ours. `offscreen daemon install` migrates the old daemon LaunchAgent.
legacy_app="$HOME/Applications/OffscreenHelper.app"
legacy_label=dev.offscreen.helper
legacy_plist="$HOME/Library/LaunchAgents/$legacy_label.plist"
domain="gui/$(id -u)"
# Diagnostics only (offscreen version); compatibility uses the protocol number.
version=$(git -C "$root" describe --tags --always --dirty 2>/dev/null || echo dev)
ldflags="-X github.com/markphelps/offscreen/internal/version.Build=$version"

if [[ "$mode" != install && "$mode" != uninstall ]]; then
  printf 'Usage: %s [install|uninstall]\n' "$0" >&2
  exit 2
fi

# Only touch an `ofs` that is missing or is our own symlink.
owns_alias() {
  [[ ! -e "$bin/ofs" && ! -L "$bin/ofs" ]] && return 0
  [[ -L "$bin/ofs" && "$(readlink "$bin/ofs")" == offscreen ]]
}

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

# stop_app stops the menu bar app, old and new. Booting out the LaunchAgent
# stops the instance launchd started. Booting out the label also stops a copy
# started by a Homebrew app's Start at Login, which shares it and whose argv
# is the relative Contents/MacOS/Offscreen, so the pkill below misses it. An
# instance opened another way (open -a, Finder) is not launchd's, so signal
# it too. SIGTERM skips the app's quit
# prompt and leaves the daemon running; the daemon install that follows
# restarts it, and uninstall stops it.
stop_app() {
  local agent exe
  for agent in "$plist" "$legacy_plist"; do
    if [[ -f "$agent" ]]; then
      launchctl bootout "$domain" "$agent" 2>/dev/null || true
      rm "$agent"
    fi
  done
  launchctl bootout "$domain/$label" 2>/dev/null || true
  for exe in "$app/Contents/MacOS/Offscreen" "$legacy_app/Contents/MacOS/OffscreenHelper"; do
    pkill -TERM -x -f "$exe" 2>/dev/null || true
    for _ in 1 2 3 4 5 6 7 8 9 10; do
      pgrep -x -f "$exe" >/dev/null || break
      sleep 0.5
    done
  done
}

# Escape the only interpolated XML strings.
escape_xml() {
  local value=$1
  value=${value//&/\&amp;}
  value=${value//</\&lt;}
  value=${value//>/\&gt;}
  value=${value//\"/\&quot;}
  printf '%s' "$value"
}

owned_agent "$plist" "$label"
owned_app "$app" "$label"
owned_agent "$legacy_plist" "$legacy_label"
owned_app "$legacy_app" "$legacy_label"

if [[ "$mode" == uninstall ]]; then
  stop_app
  rm -rf "$app" "$legacy_app"
  if [[ -x "$bin/offscreen" ]]; then "$bin/offscreen" daemon uninstall; fi
  if [[ -L "$bin/ofs" ]] && owns_alias; then rm -f "$bin/ofs"; fi
  rm -f "$bin/offscreen" "$bin/offscreend"
  printf 'Removed Offscreen.app, the daemon LaunchAgent, and offscreen, ofs and offscreend from %s.\n' "$bin"
  exit 0
fi

if ! command -v swift >/dev/null; then
  printf 'The Offscreen menu bar app needs Swift 6.2+. Install Xcode and its command-line tools, then rerun.\n' >&2
  exit 1
fi
mkdir -p "$bin"
(cd "$root" && go build -ldflags "$ldflags" -o "$bin/offscreen" ./cmd/offscreen && go build -ldflags "$ldflags" -o "$bin/offscreend" ./cmd/offscreend)
swift build --package-path "$root/app"
"$root/app/scripts/bundle.sh" >/dev/null

stop_app
# Stops any running daemon, including one under the old label, and starts the
# new one.
"$bin/offscreen" daemon install
if owns_alias; then
  ln -sf offscreen "$bin/ofs"
else
  printf 'Skipped the ofs shortcut: %s/ofs already exists.\n' "$bin" >&2
fi

rm -rf "$legacy_app" "$app"
mkdir -p "$HOME/Applications" "$HOME/Library/LaunchAgents"
/usr/bin/ditto "$root/app/.build/Offscreen.app" "$app"
codesign --verify --strict "$app"
/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister -f "$app"
# The app starts at login and launchd restarts it after a crash, but not after
# Quit Offscreen, which exits 0 once it has stopped the daemon. A default
# install uses the user's ordinary state directory; isolated integration tests
# can export a short path.
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
<key>RunAtLoad</key><true/>
<key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
$state_env
</dict></plist>
PLIST
chmod 600 "$plist"
launchctl bootstrap "$domain" "$plist"

printf 'Installed offscreen (alias ofs) and offscreend in %s, and %s.\n' "$bin" "$app"
printf 'Offscreen asks for Notifications when it first shows one, and for Input Monitoring once a host job exists.\n'
case ":$PATH:" in
*":$bin:"*) ;;
*)
  # shellcheck disable=SC2016 # print $PATH literally for the user to copy
  printf 'Add %s to your PATH, e.g. export PATH="%s:$PATH"\n' "$bin" "$bin"
  ;;
esac
