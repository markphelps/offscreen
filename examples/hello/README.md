# Hello: macOS GUI example

A one-window SwiftUI app for trying offscreen without a developer account, XcodeGen, mise, or Homebrew. The window is titled **Hello**. Its **Change label** button changes **Ready** to **Changed**. The shared Xcode scheme includes an XCUITest that clicks the button and checks both labels.

## Build without taking over the host screen

From `examples/hello`, with Xcode selected:

```sh
xcodebuild -project Hello.xcodeproj -scheme Hello -configuration Debug \
  -destination 'platform=macOS' -derivedDataPath .build build
xcodebuild -project Hello.xcodeproj -scheme Hello -configuration Debug \
  -destination 'platform=macOS' -derivedDataPath .build build-for-testing
```

Both commands compile only. Do **not** run `xcodebuild test` or launch `Hello.app` on the host if you want your desktop free. The app and test runner use ad-hoc signing (`-`) and no development team.

## Offscreen tasks

After installing offscreen and building its image, run from this directory:

```sh
offscreen run hello launch
offscreen run hello test-ui
offscreen run hello test-ios
```

`launch` builds `Hello.app` on the host into `.build/`, copies that app into a disposable VM, waits for its **Hello** window via System Events, and saves `hello.png` under `$OFFSCREEN_OUT`. `test-ui` compiles and runs the XCUITest **in the guest**, keeping its derived data in the per-project `$OFFSCREEN_CACHE` and its `Hello.xcresult` in `$OFFSCREEN_OUT`. `test-ios` runs the same XCUITest in an iOS Simulator inside the VM, using the first available iPhone in the image, and saves `HelloIOS.xcresult`. The simulator boots from scratch in each fresh VM, so expect a few minutes per run. To boot it once while building the golden image instead, copy `scripts/image-setup-ios.sh` somewhere stable, add that path to `[image] setup` in your offscreen config, and run `offscreen image build`; on a test machine this cut a warm-cache `test-ios` run from about 3.5 minutes to under 2. Offscreen prints the job's artifact directory at the end of each run. Neither task needs a package manager or a developer account.

The app and test are in `Hello.xcodeproj`, which is checked in; opening this example never generates a project. `.build/` is ignored by Git.

## If your own repo needs guest tools

Add a `[guest]` section to **your repo's** `.offscreen.toml`; `setup` runs in the guest before each task and can use `$OFFSCREEN_CACHE`. This example does not need one. Pick whichever setup matches your project:

```toml
[guest]
setup = "sh scripts/setup-guest.sh"  # plain shell: download/install your tools
```

Or, for a repo that already uses mise: `setup = "mise install"`. For a repo with a Brewfile and Homebrew in its image: `setup = "brew bundle --file Brewfile"`. Those are optional choices for other projects, not prerequisites for Hello or offscreen.
