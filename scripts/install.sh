#!/bin/sh
# Build offscreen and offscreend into a stable bin directory and (re)install
# the daemon. With "uninstall", remove the daemon's LaunchAgent and both
# binaries. Builds to a fixed directory so GOBIN/GOPATH never matter.
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
bin=${OFFSCREEN_BIN_DIR:-$HOME/.local/bin}
mode=${1:-install}

case "$mode" in
install)
  mkdir -p "$bin"
  (cd "$root" && go build -o "$bin/offscreen" ./cmd/offscreen && go build -o "$bin/offscreend" ./cmd/offscreend)
  "$bin/offscreen" daemon install
  printf 'Installed offscreen and offscreend in %s.\n' "$bin"
  case ":$PATH:" in
  *":$bin:"*) ;;
  *)
    # shellcheck disable=SC2016 # print $PATH literally for the user to copy
    printf 'Add %s to your PATH, e.g. export PATH="%s:$PATH"\n' "$bin" "$bin"
    ;;
  esac
  ;;
uninstall)
  if [ -x "$bin/offscreen" ]; then "$bin/offscreen" daemon uninstall; fi
  rm -f "$bin/offscreen" "$bin/offscreend"
  printf 'Removed offscreen and offscreend from %s.\n' "$bin"
  ;;
*)
  printf 'Usage: %s [install|uninstall]\n' "$0" >&2
  exit 2
  ;;
esac
