# Installing offscreen

## Requirements

- An Apple silicon Mac.
- Full Xcode selected as the developer directory. Check that `xcode-select -p` points to Xcode's `Contents/Developer` directory and that `xcodebuild -version` reports Xcode. Xcode also provides the Swift 6.2+ that builds the Offscreen menu bar app; check with `swift --version`.
- [Tart](https://tart.run/) on `PATH`.
- Go 1.27 or newer.

The default guest image includes macOS and Xcode and may take time and disk space to download. You do **not** need mise or Homebrew to use offscreen.

## Build and install

```sh
git clone https://github.com/markphelps/offscreen.git
cd offscreen
mise run install   # or without mise: scripts/install.sh
```

One command installs all three parts of Offscreen:

| Part | Installed as |
| --- | --- |
| `offscreen` CLI | `~/.local/bin/offscreen`, plus an `ofs` symlink as a shorter name |
| `offscreend` daemon | `~/.local/bin/offscreend`, run from a signed `OffscreenDaemon.app` in the state directory by the `io.latentsignal.offscreend` LaunchAgent |
| **Offscreen** menu bar app | `~/Applications/Offscreen.app`, started at login by the `io.latentsignal.offscreen` LaunchAgent |

The script builds everything first, so a failed build leaves your current install running. It then stops any running Offscreen (including one you opened by hand), installs the daemon, and starts Offscreen. An existing `ofs` that isn't offscreen's own symlink is left alone. Set `OFFSCREEN_BIN_DIR` to use another directory for the binaries. If that directory isn't on your `PATH`, the script says so; add it to your shell profile.

`mise run uninstall` (or `scripts/install.sh uninstall`) stops Offscreen and the daemon and removes the app, both LaunchAgents, both binaries and the `ofs` symlink. Your job history, config and images stay.

Rerun the install after pulling changes. `offscreen version` shows whether the running daemon matches the CLI, and the Offscreen menu says when they differ.

### Running Offscreen

Offscreen owns the daemon, the way Docker Desktop does. Opening Offscreen (at login, or with `open -a Offscreen`) starts the daemon; **Quit Offscreen** in its menu stops it. If jobs are queued or running, Quit asks "Quit and cancel N jobs?" first and cancels them cleanly. **Restart Daemon** in the menu restarts it the same way, for example after you change the [user config](config.md#user-config).

The CLI never starts Offscreen for you. When it isn't running, commands that need the daemon fail with:

```text
Offscreen isn't running. Open it with: open -a Offscreen
```

### Permissions

Offscreen asks for each permission only when it needs it:

| Permission | Granted to | When macOS asks |
| --- | --- | --- |
| Local Network | **Offscreen Daemon** | The first VM job, when the daemon connects to the Tart guest |
| Notifications | **Offscreen** | The first time Offscreen has a notification to show (a failed job, or **Take back now** on the host lane) |
| Input Monitoring | **Offscreen** | Once a host-lane job is queued. A user who only runs VM jobs is never asked. |

No Accessibility permission is needed. When macOS asks, allow **Offscreen Daemon** in System Settings > Privacy & Security > Local Network; the install command alone does not establish that grant. If guest SSH repeatedly reports "no route to host," check that setting and retry; the message can also indicate other routing problems.

### Upgrading from an older install

Older installs called the menu bar app **Offscreen Helper** (`~/Applications/OffscreenHelper.app`, identifier `dev.offscreen.helper`) and the daemon `dev.offscreen.offscreend`. `mise run install` removes the old app and both old LaunchAgents when they are offscreen's own. Because the identifiers changed, macOS treats Offscreen as a new app:

- Grant **Local Network** to Offscreen Daemon and **Input Monitoring** and **Notifications** to Offscreen again when asked.
- Stale **Offscreen Helper** rows (Input Monitoring, Notifications) can stay in System Settings > Privacy & Security and System Settings > Notifications. Offscreen never edits the permission database, so remove them yourself with the **-** button or turn them off.
- An old **Offscreen Daemon** row can also stay under Local Network. That pane has no remove button, and the old row is harmless. If VM jobs fail at guest SSH, turn every **Offscreen Daemon** switch off and on again.
- The host-lane settings `VerifiedInputFilter` and `ManualReturn` were stored under the old identifier. If you had set either, set it again under the new one after rerunning the [input probe](../app/README.md#input-discrimination-probe), for example `defaults write io.latentsignal.offscreen ManualReturn -bool YES`, then remove the old ones with `defaults delete dev.offscreen.helper`.

## Build the golden image

```sh
offscreen image build
```

This pulls the base image if needed, checks guest auto-login and GUI automation, turns off app relaunch and window restoration so every job starts on an empty desktop, and records the resulting image in your user config. `offscreen image refresh` pulls the base image again before rebuilding. See [the config reference](config.md#user-config) to change the base image or add image setup scripts. Each build or refresh leaves the previous image in Tart; `offscreen image prune` lists them and asks before deleting.

## Try the example

```sh
cd examples/hello
offscreen run hello test-ui
```

The Hello example builds its small macOS app on the host, then runs XCUITest in the guest. You can also run `offscreen run hello launch` to save a screenshot. A blocking run streams command output and ends with a job ID and artifact directory on stderr; the guest clone is normally deleted. See [the example](../examples/hello/README.md) for the tasks.

## Homebrew (not yet published)

A Homebrew cask is configured for `markphelps/homebrew-tap`, but **has not been published**. Once a release and the tap exist, `brew install --cask markphelps/tap/offscreen` will be an alternative to building from source. The cask is for the two Go binaries only; it does not include the Offscreen menu bar app, so build from source for now.

The release binaries are unsigned and unnotarized; the locally installed daemon app uses ad-hoc signing only. The cask removes quarantine from the two staged binaries, which bypasses Gatekeeper's verification. Install only if you trust the source; signing and notarization are preferable.

## Host lane

The VM lane needs no extra setup. Before you queue host-lane jobs, read [lanes and safety](lanes.md) and the [Offscreen app guide](../app/README.md), which covers the input probe and manual return.
