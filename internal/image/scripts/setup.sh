#!/bin/bash
set -euo pipefail
script=$(mktemp /tmp/offscreen-image-setup.XXXXXX)
trap 'rm -f "$script"' EXIT
cat > "$script"
chmod 700 "$script"
"$script"
