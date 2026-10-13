#!/bin/sh
# Build offscreen and offscreend into a stable bin directory and (re)install
# the daemon, plus an `ofs` shortcut symlink. With "uninstall", remove the
# daemon's LaunchAgent, both binaries and the symlink. Builds to a fixed
# directory so GOBIN/GOPATH never matter.
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
bin=${OFFSCREEN_BIN_DIR:-$HOME/.local/bin}
mode=${1:-install}
# Diagnostics only (offscreen version); compatibility uses the protocol number.
version=$(git -C "$root" describe --tags --always --dirty 2>/dev/null || echo dev)
ldflags="-X github.com/markphelps/offscreen/internal/version.Build=$version"

# Only touch an `ofs` that is missing or is our own symlink.
owns_alias() {
  [ ! -e "$bin/ofs" ] && [ ! -L "$bin/ofs" ] && return 0
  [ -L "$bin/ofs" ] && [ "$(readlink "$bin/ofs")" = offscreen ]
}

case "$mode" in
install)
  mkdir -p "$bin"
  (cd "$root" && go build -ldflags "$ldflags" -o "$bin/offscreen" ./cmd/offscreen && go build -ldflags "$ldflags" -o "$bin/offscreend" ./cmd/offscreend)
  "$bin/offscreen" daemon install
  if owns_alias; then
    ln -sf offscreen "$bin/ofs"
  else
    printf 'Skipped the ofs shortcut: %s/ofs already exists.\n' "$bin" >&2
  fi
  printf 'Installed offscreen (alias ofs) and offscreend in %s.\n' "$bin"
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
  if [ -L "$bin/ofs" ] && owns_alias; then rm -f "$bin/ofs"; fi
  rm -f "$bin/offscreen" "$bin/offscreend"
  printf 'Removed offscreen, ofs and offscreend from %s.\n' "$bin"
  ;;
*)
  printf 'Usage: %s [install|uninstall]\n' "$0" >&2
  exit 2
  ;;
esac
