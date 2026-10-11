#!/bin/bash
set -euo pipefail
user=$(defaults read /Library/Preferences/com.apple.loginwindow autoLoginUser)
if [[ "$user" != "$1" ]]; then
  printf 'auto-login user is %s, expected %s\n' "$user" "$1" >&2
  exit 1
fi
osascript -e 'tell application "System Events" to get name of every process' >/dev/null
