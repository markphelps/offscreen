#!/bin/bash
set -euo pipefail
shopt -s nullglob
# Every job VM boots from this image. Start each login on an empty desktop:
# stop macOS from relaunching apps or restoring their windows, quit apps left
# open by the base image or setup scripts, and drop their saved windows.
defaults write NSGlobalDomain NSQuitAlwaysKeepsWindows -bool false
defaults write com.apple.loginwindow TALLogoutSavesState -bool false
defaults write com.apple.loginwindow LoginwindowLaunchesRelaunchApps -bool false
defaults -currentHost delete com.apple.loginwindow TALAppsToRelaunchAtLogin 2>/dev/null || true

apps=$(osascript -e 'set AppleScript'"'"'s text item delimiters to linefeed' \
  -e 'tell application "System Events" to get name of every application process whose background only is false' \
  -e 'result as text')
while IFS= read -r app; do
  [[ -z "$app" || "$app" == Finder ]] && continue
  # A normal quit discards saved windows now that NSQuitAlwaysKeepsWindows is off.
  osascript -e "with timeout of 10 seconds" -e "tell application \"$app\" to quit" -e "end timeout" >/dev/null 2>&1 ||
    killall "$app" 2>/dev/null || true
done <<< "$apps"
sleep 2

# macOS 26 keeps saved windows and the app relaunch map (ApplicationMapping.plist)
# in the talagent daemon container; older releases use
# ~/Library/Saved Application State. Stop talagent so it cannot rewrite them
# from memory; launchd restarts it on demand.
states=("$HOME/Library/Saved Application State")
for container in "$HOME/Library/Daemon Containers"/*; do
  metadata="$container/.com.apple.containermanagerd.metadata.plist"
  if [[ "$(/usr/libexec/PlistBuddy -c 'Print :MCMMetadataIdentifier' "$metadata" 2>/dev/null)" == com.apple.talagent ]]; then
    states+=("$container/Data/Library/Saved Application State")
  fi
done
killall talagent 2>/dev/null || true
for state in "${states[@]}"; do
  rm -rf "${state:?}"/*
done
