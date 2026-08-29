# web-thread-clear-replaces-session: clear context keeps the session ref and replaces its live instance

**What this covers**: issue `#139`. The web command-palette **Clear context**
action must reach typed AppWire `thread/clear`, replace the live daemon
instance, preserve the stable workspace ref, and leave the same browser pane
usable for a new turn. This is the user-visible counterpart to
`server/appwire_runtime.go#handleAppThreadClear` and the store's
`cmd/evener-hub/frontend/src/stores/threads.ts#clearThread` response path.

The run also checks the failure mode that motivated the change: a clear that
looks successful but leaves the browser attached to the retired instance. A
post-clear prompt is the falsification test; it must execute in the replacement
session rather than disappearing or creating a second rail row.

## Pre-state

- A disposable fake-provider hub, isolated HOME, and fresh SPA built from this
  checkout. Start it with the repo's harness so it uses a kernel-assigned port:
  ```bash
  scripts/e2e/e2e-webui-turn-controls.sh --hold 0 --rounds 1
  ```
  Keep the printed run directory, auth URL, and workspace path. Never use
  Jesse's real hub or port `9180`.
- A browser viewport at least 900 px wide, authenticated with the printed URL.

## Steps

1. Open the printed auth URL, navigate to `/new`, and use the printed workspace
   and fake model. Start a session with a prompt that yields one short, unique
   reply, for example: `Reply with exactly INITIAL-CLEAR-MARKER.`
2. Open the new session from the Live rail and wait for the first turn to
   settle. Confirm the pane shows the marker and the rail has one row for its
   `local:<SID>` ref. Record the stable ref and the live `instanceId` from an
   authenticated AppWire `thread/read` with `includeTurns:false`, following
   `docs/developing-evener/agentic-testing.md` "Driving AppWire directly".
3. With the session idle and still open, open the command palette with
   `⌘K`/`Ctrl-K`, choose **Clear context**, and wait for the action to settle.
   Do not reload or navigate away.
4. Re-read the pane and rail in the browser:
   ```javascript
   ({
     port: location.port,
     path: decodeURIComponent(location.pathname),
     rowCount: document.querySelectorAll('[data-session-ref="local:<SID>"]').length,
     turnCount: document.querySelectorAll('[data-testid="turn-block"]').length,
     initialMarker: document.body.textContent?.includes("INITIAL-CLEAR-MARKER") ?? false,
     composer: !!document.querySelector('[data-testid="composer-input-card"]'),
     toast: document.querySelector('[aria-label="Notifications"]')?.textContent ?? "",
   })
   ```
5. Read the same stable ref again over AppWire. Compare it with step 2, then
   type `Reply with exactly AFTER-CLEAR-MARKER.` into the visible composer and
   click **Send**. Wait for the reply.
6. Re-read the browser and AppWire state. Confirm the post-clear marker is
   visible, the initial marker is absent, the pane is still on the same stable
   ref, and the rail still has one row. Shut down the disposable session and
   hub using the captured run directory.

## Expected

- **Step 2**: the first marker is visible, the session is idle, and AppWire
  reports a non-empty live instance for the stable `local:<SID>` ref.
- **Step 4**: the browser remains on the same decoded `/s/local:<SID>` path;
  `rowCount` is `1`, `initialMarker` is `false`, the composer is present, and
  there is no error toast. A transient clear notification is acceptable while
  the response is arriving, but it must be gone before step 5.
- **Step 5**: AppWire still reports the same stable ref but a different,
  non-empty `instanceId`. The replacement is therefore real, not merely a
  client-side transcript wipe.
- **Step 6**: `AFTER-CLEAR-MARKER` appears in the pane, `INITIAL-CLEAR-MARKER`
  does not, and no duplicate rail row appears. Falsify the scenario if the
  pane is blank but cannot send, the old marker returns after hydration, the
  ref changes, or the post-clear message is lost.

## Cleanup

```bash
scripts/e2e/e2e-webui-turn-controls.sh --stop "$run"
```

The stop command owns the disposable hub, fake provider, daemon children, and
run directory. Do not use `pkill`.

## Sharp edges

- **Clear context is a session-scoped command.** It is available only when the
  focused session advertises the clear capability; invoking it while a turn is
  active should be treated as a precondition failure, not as a successful run.
- **The stable ref is not the live instance id.** The ref must stay
  `local:<SID>` across clear while `instanceId` changes. Comparing only the
  visible URL misses the replacement half of the contract.
- **This is a browser scenario, not a REST substitute.** The clear action must
  be performed from the command palette, and the post-clear prompt must use the
  same browser composer. Use AppWire `thread/read` only for the authoritative
  identity cross-check.
