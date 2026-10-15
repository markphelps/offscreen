# Configuration

Offscreen reads two TOML files: a **project contract** (`.offscreen.toml` in your repo) and an optional **user config** (`~/Library/Application Support/offscreen/config.toml`). The project contract describes commands; the user config describes your machine and golden image. Named tasks must declare `lane` and `run`. Unknown project keys are rejected.

## Project contract

```toml
project = "hello"
exclusive = false

[prepare]
run = "xcodebuild -project Hello.xcodeproj -scheme Hello -derivedDataPath .build build"
share = [".build/Build/Products/Debug/Hello.app"]

[guest]
setup = "sh scripts/setup-guest.sh"

[tasks.launch]
lane = "vm"
run = "sh scripts/launch.sh"

[tasks.test-ui]
lane = "vm"
run = "sh scripts/test-ui.sh"
```

| Key | Meaning |
| --- | --- |
| `project` | Required nonempty name; must match `offscreen run <project> ...`. Offscreen discovers the file by walking up from the current directory unless `--worktree` names the exact contract directory. |
| `exclusive` | Optional Boolean, default `false`. Serialize jobs for this project across lanes; a failed VM retained by `--keep` holds the lease until canceled. |
| `[prepare] run` | Optional shell command run in the project root **on the host** before a job's VM command. Its output goes to stderr. Runs with the submitting caller's environment and `PATH`. Also runs before a host task. |
| `[prepare] share` | Optional list of files/directories to copy into the VM after preparation (e.g. compiled apps). Paths relative to the project root or absolute; `$NAME` expands from the submitting caller's host environment, and unset variables fail. The source checkout is copied separately, so use this for generated products. Not used to copy files for host tasks. |
| `[guest] setup` | Optional shell command run from the copied project root **inside each VM**, before the job command. Can populate/use `$OFFSCREEN_CACHE`. Never runs for a host task. |
| `[tasks.<name>] lane` | Required for each named task: `vm` or `host`. The CLI's `--lane` can override it. A direct `-- <cmd>` uses VM unless overridden. |
| `[tasks.<name>] run` | Required nonempty shell command executed in that lane from the project root. Direct commands after `--` instead preserve argv without joining it into a shell string. |

`[prepare] run`, `[guest] setup`, and task `run` are shell text; choose tools your project already uses. For example, replace only the guest setup above with **one** of these options:

```toml
[guest]
setup = "sh scripts/setup-guest.sh"   # plain scripts, no package manager
```

```toml
[guest]
setup = "mise install"                 # if your project and guest already use mise
```

```toml
[guest]
setup = "brew bundle --file Brewfile"  # if Homebrew is installed in your image
```

The default golden image does not install mise, Homebrew, or your project's packages. To use a package manager in the guest, add it with an `[image] setup` script in the user config, or install it in your project setup script. The checked-in [Hello contract](../examples/hello/.offscreen.toml) uses none of them and needs no `[guest]` section.

Every task gets `OFFSCREEN_OUT` (write screenshots and test results here), `OFFSCREEN_JOB` (job ID), `OFFSCREEN_LANE` (`vm` or `host`), and `OFFSCREEN_CACHE` (per-project persistent cache). VM paths are guest paths; host paths refer to the real machine. A host task inherits the caller's environment with these four values overwritten. A VM task does **not** receive the host environment wholesale. Treat the daemon's owner-only job database and backups as sensitive: submissions store the caller's host environment for preparation and share expansion. The daemon removes it from a job's record when the job finishes, but older copies can remain in SQLite free pages, its journal and backups (see [DAEMON.md](../DAEMON.md)).

## User config

The file is optional; missing settings use the defaults below. `OFFSCREEN_CONFIG` overrides its path for the client and daemon (set it before `mise run install` to carry it into the daemon's LaunchAgent). The daemon reads its settings at startup; choose **Restart Daemon** in the Offscreen menu to pick up changes. The exception is `current_image`, which it rereads for each VM job, so a new `offscreen image build` takes effect without a restart. `OFFSCREEN_STATE_DIR` overrides state/socket and job/cache directories (use a short, owner-only path to fit Unix socket length limits).

```toml
# ~/Library/Application Support/offscreen/config.toml
current_image = ""               # set automatically by image build / refresh
idle_threshold = "5m"
grace = "1m"
vm_slots = 2
low_memory_threshold_gb = 8
job_retention = "168h"           # expire completed jobs after seven days

[image]
base = "ghcr.io/cirruslabs/macos-tahoe-xcode:latest"
display = "1440x900"           # points (2x framebuffer); or 2880x1800px
ssh_user = "admin"
ssh_password = "admin"
# ssh_key = "~/.ssh/guest_key" # use a key instead of a password if configured in the base VM
# setup = ["~/offscreen-image-setup.sh"]
```

| Key | Default | Meaning |
| --- | --- | --- |
| `current_image` | Empty | Golden image name consumed by VM jobs. `image build`/`image refresh` records it after a successful build. |
| `idle_threshold` | `5m` | Required inactivity before host-lane admission; Go duration string. |
| `grace` | `1m` | Time from detected/manual return before a host job is requeued if the desktop is still in use or the Offscreen app's signal is stale. |
| `vm_slots` | `2` | VM concurrency limit; Apple's license allows no more than two macOS guests on one host. |
| `low_memory_threshold_gb` | `8` | When available memory is below this threshold, admit only one VM job. |
| `job_retention` | `168h` (seven days) | How long the daemon keeps a completed job's record, stream log and artifacts after it finished. Must be a positive Go duration string such as `24h` or `720h`; `0`, negative values and non-string values are rejected, so automatic cleanup cannot be disabled by accident. The daemon expires older jobs at startup and then every hour; see [DAEMON.md](../DAEMON.md). Read at daemon startup; restart the daemon after changing it. |
| `[image] base` | `ghcr.io/cirruslabs/macos-tahoe-xcode:latest` | Tart image cloned for a new golden image. Build pulls it if absent; refresh pulls again. |
| `[image] display` | `1440x900` | Guest display dimensions: `WIDTHxHEIGHT`, optionally `pt` or `px`. No suffix means points. |
| `[image] ssh_user` | `admin` | Base-image SSH login. |
| `[image] ssh_password` | `admin` | Password for SSH (and automation setup via sudo). Avoid checking secrets into a repo. |
| `[image] ssh_key` | Empty | SSH private key path for a base image set up for key login; use instead of password if supported by your VM. |
| `[image] setup` | Empty list | Paths to **host-side script files** (supports `~/`) streamed to and executed in the guest once while building the golden image. Use for machine-wide tools, not a project's per-job setup. |

For example, if a guest needs Homebrew or mise, supply a local image setup script that installs it and add that script's path to `[image] setup`. It runs inside the guest during `offscreen image build`, not on your host. The initial image build checks guest auto-login and GUI automation, selects the display mode, then (after any setup scripts) quits open apps and turns off app relaunch and window restoration, and records the resulting stopped image. It does not alter a project's `.offscreen.toml`.
