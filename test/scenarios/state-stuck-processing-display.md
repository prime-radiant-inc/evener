# state-stuck-processing-display: past session with failed turns shows "ended" not "processing"

**What this covers**: kata `r6y9`, commit `c10b2fd`. Before the fix, a
session whose last turn errored with `stream ended without finish event`
(or any retryable-LLM-error path that bailed before flipping
SessionIdle) would persist as state=processing forever. The hub's
`MethodThreadRead`/`hubThreadList` would surface it, the workspace UI
would disable steer/send ("no active turn is available for steer"),
and the user had no recovery path short of killing the daemon.

The fix is two-sided: agent-side flips state to SessionIdle on the
error exit (so /status reports IDLE going forward); reader-side
`sanitizeStaleProcessingStatus` in `cmd/serf-hub/app_rpc.go` overrides
processing → error when the transcript tail is an api_call with a
non-empty `Error` field.

## Pre-state

- A session exists whose transcript ends with an api_call error and
  no completed assistant turn (`session.go`'s recoverable-LLM-error
  exit). On this dev box, session `01KRSCTTEY3176G22YNWQW847Z` under
  `~/.local/state/serf/projects/a8758bc1dce01e5e/sessions/` fits.
- The backing daemon for that session is NOT running (kill the PID
  if needed; the test wants to exercise the reader-side override).
- Hub rebuilt with `c10b2fd` (or later) and running.

## Steps

1. Hit the hub `/credentials` page in a browser to set the auth
   cookie if not already.
2. Navigate to `/s/01KRSCTTEY3176G22YNWQW847Z` (substitute your
   stuck-session ID).
3. Read the status ribbon at the top of the workspace.
4. Read the transcript area for the diagnostics.
5. (Optional) Hit the JSON endpoint
   `/api/threads/<id>` (or whatever the appwire-method projection is)
   to confirm `state` is no longer `processing`.

## Expected

- Status ribbon shows `ended` (or `error` depending on hub UI label
  mapping). Falsification: ribbon shows `processing` or `busy` — the
  reader-side sanitization didn't fire.
- The page is not stuck waiting for a turn; the form is interactive
  and the send button is enabled.
- Diagnostics from the historical failed turns are visible.

## Cleanup

- None — read-only.

## Sharp edges

- **Legacy diagnostic classification (kata `96pr`)**: even after the
  state ribbon flips to ended/error, the historical "Stream ended
  without finish event" diagnostics were classified at emission time
  with `source: "serf"`, so the e465 Reconnect & retry button does
  NOT appear for them. This is a separate sharp edge — see
  `state-reconnect-button.md` for the test that exercises the NEW
  diagnostic path (where source classifies correctly).
- The agent-side fix (A) only affects daemons spawned AFTER the
  commit. Daemons running pre-fix would persist `processing` in
  /status until killed; the reader-side override (B) is what saves
  the user-visible display.
- If you have multiple stuck sessions, this scenario validates the
  list-page projection too: `/` should show them with the corrected
  state in their cards.
