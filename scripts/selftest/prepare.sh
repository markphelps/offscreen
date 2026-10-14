#!/bin/sh
# Compile every package's test binary on the host, because the golden image
# has no Go toolchain. Binaries mirror the source tree under bin/tests.
set -eu

out=bin/tests
rm -rf "$out"
go list -f '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}} {{.Dir}}{{end}}' ./... |
	while read -r pkg dir; do
		rel=${dir#"$PWD"/}
		mkdir -p "$out/$rel"
		go test -c -o "$out/$rel/pkg.test" "$pkg"
	done
