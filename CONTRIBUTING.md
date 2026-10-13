# Contributing

Offscreen's users can build with Go alone. Contributors use the versions pinned in `mise.toml` (Go, golangci-lint, shellcheck, GoReleaser). Install Xcode and its command-line tools for the macOS example and Swift helper; install Tart for local VM tests.

```sh
mise install       # install the pinned tools
mise run fmt       # format Go and apply modern idioms (edits files)
mise run check     # lint, test, build: run before every commit
mise tasks         # list everything
```

Tasks are named `<verb>` or `<verb>:<part>`, where the part is `go`, `helper` or `sh`. A bare verb runs every part: `test` runs `test:go` and `test:helper`, `lint` runs `lint:go` and `lint:sh`, `build` runs `build:go` and `build:helper`. `fmt` may change source files, so check the diff afterward. `mise run build:go` writes `bin/offscreen` and `bin/offscreend`. `mise run install` installs your build for real use (see the [install guide](docs/install.md)). Plain `go build ./...` and `go test ./...` also work. Put local environment settings in the ignored `mise.local.toml`, never in the committed `mise.toml`. Include behavioral tests for code changes and run the relevant checks before sending a PR.

`lint:sh` runs the pinned ShellCheck on every **tracked** `*.sh` file, so `git add` a new script before checking it. `test:helper` needs Swift 6.2+. It only compiles the helper and runs its XCTest unit tests. It does not bundle, sign, install or launch the helper, and it requests no permissions.

The Swift helper is a separate SwiftPM package. `mise run build:helper` makes the ad-hoc signed app bundle; the [helper guide](helper/README.md) explains local installation and permissions. The helper is **not** part of the GoReleaser archive or Homebrew cask. Distributing it as a trusted app needs Developer ID signing and notarization; the local ad-hoc signature is for development only. Don't install it over somebody else's local test setup without coordinating first.

The Go installer builds an ad-hoc signed `OffscreenDaemon.app` at install time and registers it with Launch Services; Go builds and GoReleaser still produce only the two binaries. The identifier-based designated requirement is for source installs, not an Apple-issued identity: macOS may require a fresh Local Network grant after rebuilding or reinstalling. Production distribution should sign the daemon bundle with a stable Apple-issued identity and verify actual permission persistence on target macOS versions. Do not script a global Local Network bypass or modify the TCC database. The VM daemon's Local Network grant is separate from the helper's Input Monitoring and Notifications grants.

Run end-to-end Tart and XCUITest checks **locally** with [Hello](examples/hello/README.md) after building an image. To smoke-test an existing image without the daemon, run `OFFSCREEN_IMAGE_SMOKE=<image-name> OFFSCREEN_CONFIG=<config-path> go test ./internal/image -run TestImageSmoke -count=1`. GitHub-hosted macOS runners do not support running Tart VMs. CI (`.github/workflows/ci.yml`) runs three jobs: `mise run lint:go`, `mise run test:go` and a coverage-instrumented `go test ./internal/run -cover -count=1` for Go on `macos-15`, `mise run test:helper` on `macos-26` (its default Xcode provides the Swift 6.2 that `Package.swift` needs), and `mise run lint:sh` on `macos-15`. Swift unit tests cover pure helper logic only; they do not prove sensor, event-tap, notification or permission behavior. Host-lane behavior also needs a human physical-input and permission check on the target Mac: do not interpret a passing unit test or a denied event tap as proof of physical-versus-injected input detection. Use the helper's [probe instructions](helper/README.md), and avoid hardware-sensitive host jobs until verified. Never run a GUI test on the host by accident when checking the VM path.

Release packaging can be inspected locally with `goreleaser release --snapshot --clean`. This makes an archive, checksum file and generated cask in ignored `dist/`; it does not publish. Actual releases, the tap repository, helper signing and notarization are separate maintainer work.
