# Command reference

Run from a directory under a repo containing `.offscreen.toml`, or pass `--worktree` to point at one. The `<project>` argument must match the file's `project` key. Run `offscreen --help` or `offscreen <command> --help` for flag syntax.

| Command | What it does |
| --- | --- |
| `offscreen run <project> <task>` | Queue a named task, stream output, and wait for its result. |
| `offscreen run <project> -- <cmd> [args...]` | Run an argv command directly; defaults to the VM lane. |
| `offscreen status` | List queued, active, and completed jobs with lane, state, and waiting reason. |
| `offscreen logs <job>` | Follow stdout/stderr while active; return after a completed job. `-f` also follows; `--no-follow` prints a snapshot. |
| `offscreen artifacts <job>` | Print the job's artifact directory. |
| `offscreen cancel <job>` | Cancel an active/queued job and wait for cleanup; also delete a retained `--keep` clone without changing its recorded result. |
| `offscreen prune --older-than 720h --dry-run` / `--yes` | Preview, or delete, completed jobs (record, logs and artifacts) that finished longer ago than the given age. Queued, active and retained jobs are skipped. |
| `offscreen pause` / `offscreen resume` | Pause/release the host lane; the manual pause survives daemon restarts. Resume also clears a **Not now** delay. |
| `offscreen image build` / `offscreen image refresh` | Build a golden image; refresh pulls the base image again before rebuilding. |
| `offscreen daemon install` / `offscreen daemon uninstall` | Register/start or stop/remove the user's daemon LaunchAgent. |

## `run` flags

| Flag | Meaning |
| --- | --- |
| `--lane vm\|host\|auto` | Default `auto`: a named task uses its configured lane, a direct command uses VM. |
| `--timeout 1m` | Limits waiting and running; default none. |
| `--worktree /path/to/worktree` | Use the contract in this exact directory. |
| `--detach` | Print the job ID and return; stdin is closed. |
| `--keep` | Retain a failed VM clone for inspection. |

A kept clone **holds its VM slot and any exclusive-project lease** until `offscreen cancel <job>` deletes it.

`--log-level` or `OFFSCREEN_LOG_LEVEL` selects debug, info, warn, or error logs on stderr. Stdout belongs to the wrapped command.

## Exit status

| Exit status | Meaning |
| --- | --- |
| Wrapped command's code | The command ran, including a nonzero test failure. |
| `75` | Queue deadline expired before execution. |
| `70` | Runner, daemon, VM, preparation, or cleanup failed (also a lost attached stdin after daemon restart). |
| `1` | CLI usage, config, or other client-side error. |
