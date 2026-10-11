# Contributing

Offscreen's users can build with Go alone. Contributors use the versions pinned in `mise.toml` (Go, golangci-lint, shellcheck, GoReleaser). Install Xcode and its command-line tools for the macOS example and Swift helper; install Tart for local VM tests.

```sh
mise install
mise run build
mise run test
mise run lint
mise run fmt
mise run go:modernize
```

`fmt` and `go:modernize` may change source files; check the diff afterward. `mise run build` writes `bin/offscreen` and `bin/offscreend`. Plain `go build ./...` and `go test ./...` also work. Put local environment settings in the ignored `mise.local.toml`, never in the committed `mise.toml`. Shell scripts must pass shellcheck (e.g. `shellcheck helper/scripts/*.sh examples/hello/scripts/*.sh internal/image/scripts/*.sh`). Include behavioral tests for code changes and run the relevant checks before sending a PR.

The Swift helper is a separate SwiftPM package. From `helper/`, use `swift build`, `swift test`, and `./scripts/bundle.sh`; the [helper guide](helper/README.md) explains local installation and permissions. The helper is **not** part of the GoReleaser archive or Homebrew cask. Distributing it as a trusted app needs Developer ID signing and notarization; the local ad-hoc signature is for development only. Don't install it over somebody else's local test setup without coordinating first.

Run end-to-end Tart and XCUITest checks **locally** with [Hello](examples/hello/README.md) after building an image. GitHub-hosted macOS runners do not support running Tart VMs, so CI only runs Go lint and unit tests. Host-lane behavior also needs a human physical-input and permission check on the target Mac: do not interpret a passing unit test or a denied event tap as proof of physical-versus-injected input detection. Use the helper's [probe instructions](helper/README.md), and avoid hardware-sensitive host jobs until verified. Never run a GUI test on the host by accident when checking the VM path.

Release packaging can be inspected locally with `goreleaser release --snapshot --clean`. This makes an archive, checksum file and generated cask in ignored `dist/`; it does not publish. Actual releases, the tap repository, helper signing and notarization are separate maintainer work.
