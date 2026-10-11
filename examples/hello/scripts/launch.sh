#!/bin/sh
set -eu

: "${OFFSCREEN_OUT:?OFFSCREEN_OUT must be set by offscreen}"
app=".build/Build/Products/Debug/Hello.app"
if [ ! -d "$app" ]; then
    printf 'Missing prepared app: %s\n' "$app" >&2
    exit 1
fi

open "$app"
ready=0
attempt=0
while [ "$attempt" -lt 30 ]; do
    if [ "$(osascript -e 'tell application "System Events" to exists window "Hello" of process "Hello"' 2>/dev/null)" = "true" ]; then
        ready=1
        break
    fi
    attempt=$((attempt + 1))
    sleep 1
done
if [ "$ready" -ne 1 ]; then
    printf 'Hello window did not appear within 30 seconds\n' >&2
    exit 1
fi

mkdir -p "$OFFSCREEN_OUT"
screencapture -x "$OFFSCREEN_OUT/hello.png"
printf 'Screenshot: %s/hello.png\n' "$OFFSCREEN_OUT"
