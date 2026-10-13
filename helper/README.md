# Host helper

The host lane uses a native macOS menu bar app. A nonactivating panel appears automatically for the ten-second host-job countdown, with a visible **Not now** button; the menu offers the same action. It does not activate the app or request keyboard focus. It sends a fresh desktop signal to the daemon each second over the owner-only Unix socket. The VM lane works without this app. The helper uses Input Monitoring to listen for input and notifications to offer **Take back now**. It does not request Accessibility.

## Build from source

Install Xcode. From the repository root, run:

```sh
cd helper
swift build
swift test
./scripts/bundle.sh
codesign --verify --strict .build/OffscreenHelper.app
codesign -d -r - .build/OffscreenHelper.app
```

The bundle has the fixed identifier `dev.offscreen.helper`. Its ad-hoc signature uses an identifier-based designated requirement so local rebuilds keep the same permission identity. It is not notarized. The Info.plist sets `LSUIElement`, so the app stays in the menu bar without a Dock icon. The bundle step creates only `helper/.build/OffscreenHelper.app`.

If you want the helper to run at login, run `mise run install:helper` from the repository root (without mise: `swift build --package-path helper && helper/scripts/install.sh`). This copies the bundle to `~/Applications/OffscreenHelper.app`, registers it with Launch Services, and installs only `~/Library/LaunchAgents/dev.offscreen.helper.plist`. Run `helper/scripts/install.sh uninstall` to stop and remove those owned files. Reinstall after a rebuild. The script refuses to replace an app or agent with another identifier. Install the CLI and daemon separately with `mise run install:go` (or `scripts/install.sh`). You can install and use the daemon for VM work without the helper. If macOS asks for Local Network access while running VM jobs, grant it to **Offscreen Daemon**, not Offscreen Helper; this is independent of the helper's permissions below.

When macOS asks, allow **Input Monitoring** and **Notifications** for Offscreen Helper in System Settings. If you grant Input Monitoring after the helper starts, it retries the event tap. You may need to quit and reopen the helper if macOS does not apply the grant at once. No helper Accessibility grant is needed. Missing permission or a stale helper signal keeps host jobs queued with an explicit reason in `offscreen status`.

## Input discrimination probe

Each installation must check physical versus injected input for itself before turning on automatic return detection. A successful build, a PID guess, or a permission denial does not settle that question for your Mac. Run the installed app in probe mode:

```sh
launchctl bootout "gui/$(id -u)" "$HOME/Library/LaunchAgents/dev.offscreen.helper.plist"
"$HOME/Applications/OffscreenHelper.app/Contents/MacOS/OffscreenHelper" --probe-input
```

Grant Input Monitoring if asked. Move a physical mouse and press a real key. Then send a synthetic mouse move from a separately trusted input-injection tool. The injector may need its own Accessibility permission; the helper does not. Record the printed `pid` and `sourceState` for both events, including the injector's actual PID. Do not count a denied tap or an event-free run as a negative discrimination result. Stop the foreground probe with Ctrl-C. Restart only this helper with `launchctl bootstrap "gui/$(id -u)" "$HOME/Library/LaunchAgents/dev.offscreen.helper.plist"`. The standalone `probe.swift` is the small source version of this experiment; use the installed bundle for a stable Input Monitoring identity.

The current event-tap filter treats `pid == 0` as a candidate for physical input. Automatic return detection stays off until you enable it after your own probe. For reference, one tested Apple silicon Mac reported physical mouse, key, click, and scroll events with `pid` 0 and `sourceState` 1. A synthetic mouse move from a separate injector reported that injector's nonzero PID and `sourceState` 0. That result is from one machine and one injector; it does not show what your Mac reports. If the fields distinguish physical events on your Mac, enable the filter and restart the helper:

```sh
defaults write dev.offscreen.helper VerifiedInputFilter -bool YES
```

If the observed streams cannot be distinguished, leave that setting off and enable the documented manual fallback:

```sh
defaults write dev.offscreen.helper ManualReturn -bool YES
```

Restart the helper after changing that setting. In fallback mode, the helper does not detect a mid-run return automatically. Select **I'm back (start grace period)** in its menu to get a notification and a 60-second grace period by default. Select **Take back now** to cancel an active host job (including one still preparing) and put it at the front of the queue after cleanup. Failed controls remain visible as errors in the menu until a successful action or dismissal. **I'm back** and **Take back now** are also available in the menu when automatic detection is on. Reset with `defaults delete dev.offscreen.helper ManualReturn` only after a successful physical-input test. A return while a job is still preparing cancels preparation before the task launches and requeues it after cleanup. A return during a running task does not stop it immediately. At grace expiry, the daemon requeues it if the desktop is still in use or the helper signal is stale. It lets the current task continue if the desktop is idle again. Later host jobs still wait for a fresh idle gate.

Use a short owner-only `OFFSCREEN_STATE_DIR` for isolated testing. Never point the helper at another user's socket. Do not use a hardware-sensitive host task before you have checked lock, display, microphone, camera, countdown, and notification behavior on your own Mac. A harmless `sleep` or `echo` task is sufficient for the first test.
