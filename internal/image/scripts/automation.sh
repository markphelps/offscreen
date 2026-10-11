#!/bin/bash
set -euo pipefail
# Xcode enables Automation Mode for UI tests. Persist the separate setting
# that allows it to do so without a password prompt.
status=$(/usr/bin/automationmodetool || true)
grep -q 'DOES NOT REQUIRE user authentication' <<< "$status"
