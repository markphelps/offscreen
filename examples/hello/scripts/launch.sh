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

# Login may restore Terminal over the app. Raise Hello before capturing it.
if ! osascript -e 'tell application "System Events" to set frontmost of process "Hello" to true'; then
    printf 'Could not bring Hello to the front\n' >&2
    exit 1
fi
attempt=0
while [ "$attempt" -lt 10 ]; do
    if [ "$(osascript -e 'tell application "System Events" to get frontmost of process "Hello"' 2>/dev/null)" = "true" ]; then
        break
    fi
    attempt=$((attempt + 1))
    sleep 1
done
if [ "$attempt" -eq 10 ]; then
    printf 'Hello did not become frontmost\n' >&2
    exit 1
fi
sleep 1
mkdir -p "$OFFSCREEN_OUT"
screencapture -x "$OFFSCREEN_OUT/hello.png"
printf 'Screenshot: %s/hello.png\n' "$OFFSCREEN_OUT"
