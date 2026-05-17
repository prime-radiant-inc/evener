# cli-sibling-binary: serf-tui finds serf-hub next to itself

**What this covers**: kata `a4w6`, helper landed in commit `07fdd02`,
tests in `bc00781`. Before the fix, running `./serf-tui` when
`serf-hub` was in the same directory but not on `$PATH` failed with
`exec: "serf-hub": executable file not found in $PATH`. The fix
resolves `filepath.Dir(os.Executable())` (via `EvalSymlinks`) and
tries the sibling before falling back to `$PATH` lookup, returning an
absolute path so Go's `exec.ErrDot` restriction doesn't trip.

## Pre-state

- Repo built: `go build -o serf-tui ./cmd/serf-tui && go build -o serf-hub ./cmd/serf-hub`.
- A fresh `mktemp -d` directory the test will copy binaries into.
- `SERF_HUB_BIN` unset in the environment.

## Steps

1. `tmpdir=$(mktemp -d); cp serf-tui serf-hub "$tmpdir/"`.
2. `cd "$tmpdir" && unset SERF_HUB_BIN`.
3. Confirm `$PATH` does NOT include the tmpdir (it shouldn't — fresh
   shell). `which serf-hub` should fail.
4. Run `./serf-tui --hub-addr 127.0.0.1:<random-unused-port>` with a
   3-5 second timeout. The hub will try to start but the address is
   unused so the TUI's health check eventually gives up.
5. Capture stderr.

## Expected

- The error message names a real exec attempt (`serf-hub exited
  during startup: ...`), not "executable file not found in $PATH".
- Specifically: if another serf-hub is already running, you'll see
  `flock: resource temporarily unavailable (another serf-hub may
  already be running)` — that proves the sibling binary was exec'd.
  If no other hub is running, you'll see a different startup error
  (port conflict, missing perms) — still proves the exec happened.
- Falsification: stderr contains `executable file not found in $PATH`
  or `cannot run executable found relative to current directory` —
  the kata regression is back.

## Cleanup

- `rm -rf "$tmpdir"`.

## Sharp edges

- This test is implicitly racy with whatever serf-hub is running on
  the system. The flock-failure signal works because the hub uses a
  shared run-dir; on a clean machine the test still passes via a
  different startup error path.
- A symlink variant is worth adding: `ln -s real/path/serf-tui
  $tmpdir/serf-tui` and confirm the EvalSymlinks branch resolves to
  the symlink target's directory and finds serf-hub there. Covered
  in the unit test `TestResolveHubBinaryFollowsSymlinks` but not
  exercised end-to-end here.
- `unset SERF_HUB_BIN` matters; if a developer has it set (e.g. to
  point at a wip build), the explicit override pre-empts sibling
  resolution and the test would pass trivially.
