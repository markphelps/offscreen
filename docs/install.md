# Installing offscreen

## Requirements

- An Apple silicon Mac.
- Full Xcode selected as the developer directory. Check that `xcode-select -p` points to Xcode's `Contents/Developer` directory and that `xcodebuild -version` reports Xcode.
- [Tart](https://tart.run/) on `PATH`.
- Go 1.27 or newer.

The default guest image includes macOS and Xcode and may take time and disk space to download. You do **not** need mise or Homebrew to use offscreen.

## Build and install

```sh
git clone https://github.com/markphelps/offscreen.git
cd offscreen
mise run install:go   # or without mise: scripts/install.sh
```

This builds `offscreen` and `offscreend` into `~/.local/bin` and runs `offscreen daemon install` (below). Set `OFFSCREEN_BIN_DIR` to use another directory. If that directory isn't on your `PATH`, the script says so; add it to your shell profile.

| Command | What it does |
| --- | --- |
| `mise run install` | `install:go`, then builds and installs the [host lane helper](../helper/README.md) (needs Swift 6.2+) |
| `mise run install:go` | CLI and daemon only. Enough for VM jobs. |
| `mise run install:helper` | Helper only |
| `mise run uninstall` | Removes the daemon LaunchAgent, the helper, and both binaries |

Rerun the install after pulling changes. The daemon must be reinstalled whenever `offscreend` is rebuilt. Both binaries must sit in the same directory when you run `daemon install`.

## Build the golden image

```sh
offscreen image build
```

This pulls the base image if needed, checks guest auto-login and GUI automation, and records the resulting image in your user config. `offscreen image refresh` pulls the base image again before rebuilding. See [the config reference](config.md#user-config) to change the base image or add image setup scripts.

## Install the daemon

The install script above already runs this. Run it yourself after rebuilding `offscreend` by hand:

```sh
offscreen daemon install
```

`daemon install` finds `offscreend` beside the `offscreen` executable, or on `PATH`, installs it in the signed user-owned `OffscreenDaemon.app` under the state directory, registers it with Launch Services, and starts a launchd agent. `offscreen daemon uninstall` removes its LaunchAgent.

### Local Network permission

VM jobs need the daemon's Local Network permission to reach the Tart guest. If prompted, allow **Offscreen Daemon** in System Settings > Privacy & Security > Local Network before running a VM job. A grant is not established by the install command alone, and the daemon does not auto-start if you skip that step. If SSH repeatedly reports “no route to host,” check that setting and retry; the message can also indicate other routing problems.

## Try the example

```sh
cd examples/hello
offscreen run hello test-ui
```

The Hello example builds its small macOS app on the host, then runs XCUITest in the guest. You can also run `offscreen run hello launch` to save a screenshot. A blocking run streams command output and ends with a job ID and artifact directory on stderr; the guest clone is normally deleted. See [the example](../examples/hello/README.md) for the tasks.

## Homebrew (not yet published)

A Homebrew cask is configured for `markphelps/homebrew-tap`, but **has not been published**. Once a release and the tap exist, `brew install --cask markphelps/tap/offscreen` will be an alternative to building from source. The cask is for the two Go binaries only; the native host helper is built and installed separately.

The release binaries are unsigned and unnotarized; the locally installed daemon app uses ad-hoc signing only. The cask removes quarantine from the two staged binaries, which bypasses Gatekeeper's verification. Install only if you trust the source; signing and notarization are preferable.

## Host lane helper

The VM lane needs nothing else. The optional host lane needs the [menu bar helper](../helper/README.md), installed with `mise run install:helper` (or `mise run install`). See [lanes and safety](lanes.md) before enabling it.
