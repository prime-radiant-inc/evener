# Upgrading

Breaking changes and migration notes for evener.

## Before shutting down an older session

**Saved session trees with legacy delegate lifecycle state or watches addressed
through legacy delegate jobs cannot resume on the current runtime.** Preserve
their history and workspace before shutting down the old daemon. A hub restart
does not replace that daemon. `evener migrate` moves supported layouts and
provider configuration; it does not convert this rejected runtime state.

The refusal identifies `legacy_delegate_state` or
`legacy_delegate_watch_state`. Keep the original state intact. Do not remove
job or watch journals to bypass the check. Current-format saved sessions can
restore, but readable history alone does not establish compatibility.

1. Record the old build and effective state paths. Finish essential work on the
   old runtime before the [maintenance shutdown](docs/evener-hub.md#upgrading-and-migrating-safely).
2. Make a consistent private backup, then run the provider-free
   [history restore rehearsal](docs/security.md#rehearse-history-restoration).
   Inspect a copy, never the only surviving state.
3. If restoration rejects legacy state, keep that copy for history. Review the
   workspace and external job outcomes, then start a new task with a written
   handoff: completed work, remaining work, relevant paths, and uncertain effects.
   Do not use `--resume`, `--resume-last`, or `--resume-with` for this path.

For CLI continuation, create a new private state directory outside the backup.
Run this in Bash after setting `workspace` to the existing working directory and
`model` to a configured model reference:

```bash
(
  set -euo pipefail
  umask 077
  fresh_state=$(mktemp -d "${TMPDIR:-/tmp}/evener-continuation.XXXXXX")
  printf 'Keep this new session state: %s\n' "$fresh_state"
  evener --state-dir "$fresh_state" --dir "$workspace" --model "$model" \
    'Review the preserved workspace and my handoff before continuing.'
)
```

This starts a new session and can call the provider and run tools. It does not
import the rejected session's history, jobs, delegates, watches, or queue. Keep
the new state directory as well as the backup; move it into your backup policy
before temporary-directory cleanup. Check the task's actual files and saved
transcript before treating the continuation as successful.

## Unreleased — the `evener tui` subcommand is removed

The interactive terminal dashboard (`evener tui`, `cmd/evener-tui`) is gone.
The non-interactive CLI and the web and native clients are unchanged. `evener
tui` is now an ordinary unknown first word: it is not a subcommand, so it runs
as a prompt (or, with no prompt on an interactive terminal, prints usage and
exits 1). Use the web UI served by `evener hub`, the native app, or the
non-interactive CLI (`evener "<prompt>"`, `evener serve`, `evener doctor`).

## Unreleased — `evener-dev` is no longer installed

`evener-dev` is repository dev and test tooling, not part of an install.
`install.sh`, `make install` and `evener upgrade` now install `evener`
alone. Build `evener-dev` from a checkout with `make build-dev` when you
need it.

Release archives no longer carry `evener-dev`: they hold `evener` alone.
No tagged evener release ever required `evener-dev` in the archive — `v0.2.0`,
the first evener-named release, already installs `evener` alone
(`installBinaries`) — so the only `evener upgrade` that still needs it is a
development/snapshot build from the transition window between the binary
consolidation and the evener-only `installBinaries`; those have aged out, and
such a build must reinstall with `install.sh` to move past them. An
`evener-dev` that an earlier install left in `~/.local/share/evener/bin` (and
its `~/.local/bin` link) is no longer updated. Remove it if you don't use it:

```sh
rm -f ~/.local/bin/evener-dev ~/.local/share/evener/bin/evener-dev
```

## Unreleased — Tool `purpose` param renamed to `intent`

The shared tool parameter that carries the agent's stated reason for a call
was renamed from `purpose` to `intent`. New tool calls carry `intent` only.
Readers fall back to `purpose` when `intent` is absent, so transcripts
recorded before 2026-08-29 still render their tool-intent line in the hub
and `evener doctor`.

## Unreleased — Binary consolidation

### Summary

All standalone `evener-<x>` binaries are consolidated into two binaries:

| Binary | Subcommands |
|--------|-------------|
| `evener` | `serve`, `hub`, `doctor`, `migrate`, `openai`, `upgrade`, `plugin`, `launch-check` |
| `evener-dev` | `dev`, `fuzz-harvest`, `fuzzcov`, `fuzzregistry`, `internalcheck`, `tomlcheck`, `transcript-v2-upgrade` |

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
evener doctor locate <session-id>
evener migrate
```

(The `evener-tui` row of this historical migration became `evener tui`; that
subcommand was later removed — see the top of this file.)

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

`make install` now installs `evener` alone instead of five binaries (see
"`evener-dev` is no longer installed" above). If you have old binaries on
your `PATH`, remove them:

```sh
rm ~/.local/bin/evener-hub ~/.local/bin/evener-tui \
   ~/.local/bin/evener-doctor ~/.local/bin/evener-migrate
```

The self-update (`evener upgrade`) path installs the new `evener`
automatically.

### Release archives

Release archives now contain one binary (`evener`) instead of five. The
archive name (`evener_<os>_<arch>.tar.gz`) and directory layout are unchanged.
`install.sh` installs `evener` from them.

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
