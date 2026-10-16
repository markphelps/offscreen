# Contributing

Offscreen's users can build with Go alone. Contributors use the versions pinned in `mise.toml` (Go, golangci-lint, shellcheck, GoReleaser, swift-format). Install Xcode and its command-line tools for the macOS example and the Offscreen menu bar app (Swift 6.2+); install Tart for local VM tests.

```sh
mise install       # install the pinned tools
mise run fmt       # format Go and Swift, apply modern Go idioms (edits files)
mise run check     # lint, test, build: run before every commit
mise tasks         # list everything
```

Tasks are named `<verb>` or `<verb>:<part>`, where the part is `go`, `app` or `sh`. A bare verb runs every part: `test` runs `test:go` and `test:app`, `lint` runs `lint:go`, `lint:sh` and `lint:app`, `fmt` runs `fmt:go` and `fmt:app`, `build` runs `build:go` and `build:app`. `fmt` may change source files, so check the diff afterward. `mise run build:go` writes `bin/offscreen` and `bin/offscreend`. `mise run install` installs your build for real use (see the [install guide](docs/install.md)). Plain `go build ./...` and `go test ./...` also work. Put local environment settings in the ignored `mise.local.toml`, never in the committed `mise.toml`. Include behavioral tests for code changes and run the relevant checks before sending a PR.

`lint:sh` runs the pinned ShellCheck on every **tracked** `*.sh` file, so `git add` a new script before checking it. `lint:app` and `fmt:app` run the pinned [swift-format](https://github.com/swiftlang/swift-format) on the menu bar app with `app/.swift-format` (4-space indent, 120 columns); `lint:app` fails on any finding. swift-format publishes no release binaries, so on macOS `mise install` builds it from source with your Swift toolchain, which takes a few minutes the first time. `test:app` needs Swift 6.2+. It only compiles the app and runs its XCTest unit tests. It does not bundle, sign, install or launch the app, and it requests no permissions.

The Offscreen menu bar app is a separate SwiftPM package in `app/`. `mise run build:app` makes the ad-hoc signed bundle in `app/.build` without installing it; `mise run install` builds and installs it with the CLI and daemon, and the [app guide](app/README.md) explains its permissions. Releases ship it too: the GoReleaser archive contains Offscreen.app, and the Homebrew cask installs it in `/Applications`. Both are ad-hoc signed; the cask removes quarantine instead of relying on Developer ID signing and notarization, which remain out of scope. Don't install it over somebody else's local test setup without coordinating first.

`offscreen daemon install` builds an ad-hoc signed `OffscreenDaemon.app` at install time and registers it with Launch Services, for both source installs and the cask (which runs it after every install and upgrade); Go builds and GoReleaser produce only the bare `offscreend` binary. The identifier-based designated requirement is not an Apple-issued identity: macOS may require a fresh Local Network grant after rebuilding or reinstalling. Production distribution should sign the daemon bundle with a stable Apple-issued identity and verify actual permission persistence on target macOS versions. Do not script a global Local Network bypass or modify the TCC database. The VM daemon's Local Network grant is separate from the app's Input Monitoring and Notifications grants.

Run end-to-end Tart and XCUITest checks **locally** with [Hello](examples/hello/README.md) after building an image. To smoke-test an existing image without the daemon, run `OFFSCREEN_IMAGE_SMOKE=<image-name> OFFSCREEN_CONFIG=<config-path> go test ./internal/image -run TestImageSmoke -count=1`. GitHub-hosted macOS runners do not support running Tart VMs. CI (`.github/workflows/ci.yml`) runs two jobs. `lint` runs on `ubuntu-latest`: `mise run lint:go` with `GOOS=darwin` (the Go code has no cgo, so it type-checks for darwin from Linux) and `mise run lint:sh`. `test` runs on `macos-26` (its default Xcode provides the Swift 6.2 that `Package.swift` needs): `mise run test:go`, a coverage-instrumented `go test ./internal/run -cover -count=1`, `mise run test:app` and `mise run lint:app`. CI has no secrets and publishes nothing. The release workflow (`.github/workflows/release.yml`) runs when a `v*` tag is pushed, on `macos-26`: it fails at once if the `HOMEBREW_TAP_GITHUB_TOKEN` secret is unset, checks out the full history (GoReleaser needs the tags for the version and changelog), installs the pinned Go and GoReleaser, runs `mise run test` (with swift-format disabled, so the release never builds it), then runs `goreleaser release --clean` with `HOMEBREW_TAP_GITHUB_TOKEN` set from that secret. That publishes the GitHub release and pushes `Casks/offscreen.rb` to `markphelps/homebrew-tap`. Swift unit tests cover pure app logic only; they do not prove sensor, event-tap, notification or permission behavior. Host-lane behavior also needs a human physical-input and permission check on the target Mac: do not interpret a passing unit test or a denied event tap as proof of physical-versus-injected input detection. Use the app's [probe instructions](app/README.md#input-discrimination-probe), and avoid hardware-sensitive host jobs until verified. Never run a GUI test on the host by accident when checking the VM path.

Release packaging can be inspected locally with `goreleaser release --snapshot --clean`. It needs Swift 6.2+, because a before hook builds a release Offscreen.app stamped with the version. This makes an archive (`offscreen`, `offscreend` and `Offscreen.app` at its top level), checksum file and generated cask in ignored `dist/`; it does not publish. App signing and notarization are separate maintainer work.

## Cutting a release

The release workflow needs the public `markphelps/homebrew-tap` repository and a `HOMEBREW_TAP_GITHUB_TOKEN` Actions secret on this repository holding a token with contents write access to the tap.

1. Make sure `main` is green in CI and run `goreleaser release --snapshot --clean` locally if packaging changed.
2. Tag the commit and push the tag:

   ```sh
   git tag v0.1.0
   git push origin v0.1.0
   ```

3. Watch the release workflow (`gh run watch`). It publishes the GitHub release with the archive and `checksums.txt`, then pushes `Casks/offscreen.rb` to the tap.
4. Check the result with `brew update && brew upgrade --cask offscreen` (or a fresh `brew install --cask markphelps/tap/offscreen`) after removing any source install with `mise run uninstall`.

The tag sets the version: GoReleaser stamps it, without the leading `v`, into both binaries and the app's `OffscreenBuild`, so the menu can tell when the daemon is stale. Don't reuse or move a pushed tag; cut a new patch version instead.
