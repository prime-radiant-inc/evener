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
- `python3` on PATH (step 4 uses it to take a port from the kernel).

## Steps

1. `tmpdir=$(mktemp -d); cp serf-tui serf-hub "$tmpdir/"`.
2. `cd "$tmpdir" && unset SERF_HUB_BIN`.
3. Confirm `$PATH` does NOT include the tmpdir (it shouldn't — fresh
   shell). `which serf-hub` should fail.
4. Take an address nothing answers on, without choosing a number: bind
   `127.0.0.1:0`, read back the port the kernel handed out, and close
   the listener so that port is free again.
   ```bash
   PORT=$(python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); port = s.getsockname()[1]; s.close(); print(port)')
   ```
5. Run `./serf-tui --hub-addr 127.0.0.1:$PORT` with a 3-5 second
   timeout. Nothing is listening there, so the TUI's health check fails
   immediately and it resolves `serf-hub` and execs it — the sibling
   lookup this card is about.
6. Capture stderr.
7. Record the hub this run caused, if it started one. The TUI execs
   `serf-hub --addr <the address you passed>` and deliberately releases
   it (`cmd/serf-tui/internal/hubstart/hub_start.go`, `StartLocalHub`),
   so on a machine where the flock was free a detached hub is now
   holding the REAL `~/.serf/hub.lock` — this card runs on the real
   `$HOME` on purpose. `$PORT` came from the kernel in step 4, so that
   argv names this run's hub and nobody else's:
   ```bash
   pgrep -f "serf-hub --addr 127.0.0.1:$PORT" | head -1 > "$tmpdir/hub.pid" || true
   ```
   An empty `hub.pid` is the other legal outcome (another serf-hub held
   the lock, so the sibling exec failed before listening) and Cleanup
   handles it.

## Expected

- The error message names a real exec attempt (`serf-hub exited
  during startup: ...`), not "executable file not found in $PATH".
- Specifically: if another serf-hub is already running, stderr
  contains the substring `resource temporarily unavailable (another
  serf-hub may already be running` — that proves the sibling binary
  was exec'd. Match on the substring, not the whole sentence: the
  real message names the lock path and continues past that point
  (`flock <path>: resource temporarily unavailable (another serf-hub
  may already be running; a disposable hub needs its own HOME)`,
  `cmd/serf-hub/internal/hostlock/hostlock.go:32`).
  If no other serf-hub holds that flock, the sibling hub starts
  instead of failing and the TUI attaches to it — no `serf-hub exited
  during startup` line at all, which is still a pass, since the
  falsification below is the only failure this card reads. A port
  conflict is not one of the outcomes any more: step 4's port was free
  when the kernel handed it back. The hub started this way is
  deliberately detached and outlives the TUI
  (`cmd/serf-tui/internal/hubstart/hub_start.go:441`); step 7 records
  its pid and Cleanup kills it, so nothing this card started is left
  holding the real `~/.serf/hub.lock` (kata `zw9j`).
- Falsification: stderr contains `executable file not found in $PATH`
  or `cannot run executable found relative to current directory` —
  the kata regression is back.

## Cleanup

Kill the hub step 7 recorded BEFORE removing `$tmpdir` — that file is
the only record of it, and a survivor holds the real `~/.serf/hub.lock`
against every later hub on this machine. The `ps` re-check is the
safety catch: this only ever signals a process whose own argv still
names the port step 4 took from the kernel, so it can never reach
another agent's hub or a hub this card did not start.

```bash
HUBPID=$(cat "$tmpdir/hub.pid" 2>/dev/null || true)
if [ -n "$HUBPID" ] && ps -o command= -p "$HUBPID" 2>/dev/null | grep -q -- "--addr 127.0.0.1:$PORT"; then
  kill "$HUBPID"
  for i in $(seq 1 50); do kill -0 "$HUBPID" 2>/dev/null || break; sleep 0.1; done
  if kill -0 "$HUBPID" 2>/dev/null; then kill -9 "$HUBPID"; fi
fi
rm -rf "$tmpdir"
```

Confirm nothing survives: `pgrep -f "serf-hub --addr 127.0.0.1:$PORT"`
prints nothing. If it still does, say so in the run record rather than
walking away — a held `hub.lock` is the failure this cleanup exists to
prevent, and the next card to start a hub is the one that pays for it.

## Sharp edges

- Step 4's bind-read-close is how this card gets an address nothing
  answers on without anybody choosing a number (kata `nv03`). Two
  agents running the card at once get different ports by construction,
  and the port is known-free rather than assumed-free. Plain `-addr
  127.0.0.1:0` is not a substitute: the TUI hands its bind address
  straight to the hub it starts
  (`cmd/serf-tui/internal/hubstart/hub_start.go:275` and `:444`), so
  the hub would come up on a kernel-assigned port while the TUI kept
  health-checking port 0 — a different failure than the one this card
  reads. Between the close and the TUI's connect there is a window in
  which something else could take the port; on loopback that costs a
  rerun of a card that only inspects an error message.
- This test is implicitly racy with whatever serf-hub is running on
  the system. The flock-failure signal works because the hub uses a
  shared run-dir; on a clean machine the test still passes via a
  different startup error path.
- The real `$HOME` is deliberate and is why Cleanup matters. Isolating
  `$HOME` would remove the very signal the Expected section reads: the
  flock contention that proves the sibling binary was exec'd only
  happens against the one host-wide lock (`~/.serf/hub.lock`,
  `cmd/serf-hub/main.go:157`). So the card takes the opposite trade —
  keep the real lock in play, and account for the hub it may start.
  Step 7 plus Cleanup are that accounting; do not "fix" this card by
  isolating `$HOME` without replacing the assertion first.
- A symlink variant is worth adding: `ln -s real/path/serf-tui
  $tmpdir/serf-tui` and confirm the EvalSymlinks branch resolves to
  the symlink target's directory and finds serf-hub there. Covered
  in the unit test `TestResolveFollowsSymlinkedExecutable`
  (`internal/binresolve/sibling_test.go#TestResolveFollowsSymlinkedExecutable`) but not exercised
  end-to-end here.
- `unset SERF_HUB_BIN` matters; if a developer has it set (e.g. to
  point at a wip build), the explicit override pre-empts sibling
  resolution and the test would pass trivially.
