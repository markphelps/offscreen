#!/bin/sh
# Run each prebuilt test binary from its package directory and keep its full
# log in $OFFSCREEN_OUT. Exits nonzero if any package fails.
set -u

: "${OFFSCREEN_OUT:?OFFSCREEN_OUT must be set by offscreen}"
root=$(pwd)
status=0
for test in $(find bin/tests -name pkg.test | sort); do
	dir=$(dirname "${test#bin/tests/}")
	log="$OFFSCREEN_OUT/$(echo "$dir" | tr / -).log"
	if (cd "$root/$dir" && "$root/$test" -test.count=1 >"$log" 2>&1); then
		echo "ok   $dir"
	else
		echo "FAIL $dir (see $(basename "$log"))"
		grep -A8 -e '--- FAIL' "$log" | head -40
		status=1
	fi
done
exit "$status"
