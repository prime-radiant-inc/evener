# Upgrading

Breaking changes and migration notes for evener.

## Unreleased — Binary consolidation

### Summary

All standalone `evener-<x>` binaries are consolidated into two binaries:

| Binary | Subcommands |
|--------|-------------|
| `evener` | `serve`, `hub`, `tui`, `doctor`, `migrate`, `openai`, `upgrade`, `plugin`, `launch-check` |
| `evener-dev` | `dev`, `fuzz-harvest`, `fuzzcov`, `fuzzregistry`, `internalcheck`, `test-dev-tooling`, `tomlcheck`, `transcript-v2-upgrade` |

The five old binaries (`evener-hub`, `evener-tui`, `evener-doctor`, `evener-migrate`,
and the dev/test tooling CLIs) are gone. Their functionality lives on as
subcommands.

### Migration

**Replace every old binary invocation with the subcommand form:**

```sh
# Before
evener-hub
evener-tui
evener-doctor locate <session-id>
evener-migrate

# After
evener hub
evener tui
evener doctor locate <session-id>
evener migrate
```

Dev/test tooling:

```sh
# Before
evener-fuzz-harvest
evener-fuzzcov
evener-internalcheck

# After
evener-dev fuzz-harvest
evener-dev fuzzcov
evener-dev internalcheck
```

### Install

`make install` now installs two binaries (`evener`, `evener-dev`) instead of
five. If you have old binaries on your `PATH`, remove them:

```sh
rm ~/.local/bin/evener-hub ~/.local/bin/evener-tui \
   ~/.local/bin/evener-doctor ~/.local/bin/evener-migrate
```

The self-update (`evener upgrade`) path installs both new binaries
automatically.

### Release archives

Release archives now contain two binaries (`evener`, `evener-dev`) instead of
five. The archive name (`evener_<os>_<arch>.tar.gz`) and directory layout are
unchanged. `install.sh` reads the new binary list.

### Go version

The Go toolchain requirement is bumped from **1.25.x** to **1.27.0**.
`go.work` and all `go.mod` files declare `go 1.27.0`. CI uses Go 1.27.0.

### golangci-lint

golangci-lint is bumped from **v2.12.2** to **v2.13.1** (built with Go 1.27).
Run `make tools` to install the new version. The `govet` `inline` analyzer is
disabled in `.golangci.yml` — it produces false positives on test helper
functions that are deliberately not inlined.

### Makefile targets

Removed targets (functionality now in `make build` / `make build-dev`):
- `make build-tui`
- `make build-doctor`
- `make build-migrate`

(`make build-hub` is retained as an alias for `build-runtime`.)

New targets:
- `make build-dev` — builds the `evener-dev` dev/test infrastructure binary
- `make build-all` — builds both `evener` and `evener-dev`

### Scripts

All scripts under `scripts/` that invoked `evener-<x>` binaries now use
`evener <subcommand>` or `evener-dev <subcommand>`.

### Library packages

The `cmd/evener-<x>` directories are now library packages (not `package main`)
with a `Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int`
entry point. The dispatch binaries (`cmd/evener`, `cmd/evener-dev/bin`) call
these `Run` functions.

### Self-invocation

The TUI previously spawned `evener-hub` as a separate process. It now spawns
`evener hub` (the `evener` binary with the `hub` subcommand). Binary resolution
via `binresolve.Resolve` uses the `evener` binary name.
