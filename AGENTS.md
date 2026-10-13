# Offscreen contributor guidance

Go 1.27 module `github.com/markphelps/offscreen`: Kong commands in `cmd/offscreen`, daemon in `cmd/offscreend`, shared code in `internal/`. Each CLI command is a struct with `Run` method. Use `log/slog` text output on stderr; stdout belongs to the wrapped command. Use Google Go style, inline error checks, three import groups (stdlib, external, local), and co-located behavioral tests.

Contributor tools are pinned in `mise.toml`; mise is not required for users building from source. Keep personal settings in ignored `mise.local.toml`. Private design notes under `docs/` must not be copied into tracked examples or documentation. Run `mise run fmt` then `mise run check` before committing. Task names are `<verb>` or `<verb>:<part>` (`go`, `helper`, `sh`); keep new tasks consistent. Shell scripts must pass shellcheck.
