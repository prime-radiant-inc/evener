# backgroundtranscriptguard

The whole-journey proof for background Live transcripts on the production entry
route. It drives the **real** hub web app in Vite development mode in real Chrome
through a **real** hub against **two real `evener serve` daemons**, with the only
scripted piece at the external LLM provider adapter. It replaces no Evener
internal with a mock.

The guard is registered in `cmd/evener-dev/webbrowser.go` and runs through the
browser gate (`make test-web-browser`) as `web-backgroundtranscriptguard`. The
Gate's launch contract is:

```
go test -tags browserguard ./cmd/evener-hub -run '^TestBackgroundTranscriptsEntrySliceBrowser$' -count=1
```

## What it proves

`TestBackgroundTranscriptsEntrySliceBrowser`
(`cmd/evener-hub/background_transcripts_browser_test.go`) starts the fixture hub
(real roster prober + past index + `newWebServer` + `appserver.NewWebSocketTrace`)
and two compiled `evener serve` helper daemons, then runs `run.mjs` with
`--entry-production true`. The driver owns the browser gestures and records
milestones; the Go test owns the daemons, hub and every assertion against what
the daemons actually received (provider request logs, phase acknowledgments,
durable transcripts) and what the real hub AppWire trace recorded.

The journey, in order:

1. The dev entry installs the entry harness budgets (`residentBytes: 64 KiB`,
   `pendingBytes: 101 KiB`) **before** the real app imports; the selected target
   session's durable entry rows are committed to real IndexedDB, and the Go
   owner asserts the selected ref carries no legacy cache record.
2. The target produces its before-eviction background output while it is never
   opened, and the pane then opens through the rail rendering from local entry
   history. The trace byte window around that activation must contain no target
   `thread/read` or `thread/turns/list` request, and a live streaming sentinel
   must be absent from entry rows until its turn completes.
3. A fresh second tab opens the same session through the rail while that turn
   is running. Both panes show the initial output, then receive the remaining
   streamed output and real tool result. The `two-tab-live` milestone retains
   both tabs' rendered rows, ordered item identities and tool output. The Go
   owner compares them with each other and the daemon's durable turn/call data.
4. The turn finishes; `BACKGROUND_AFTER_EVICTION` and the real `shell` tool
   output are recorded in native entry storage. Each produced item renders
   exactly once in its real tool output region, the renderer's recorded items
   match the native IndexedDB item/turn/coverage rows one-to-one by keyed
   identity and position, and the durable producer turn/call identity matches
   independently.
5. The raw renderer items, rendered rows, output region, native items, turns and
   coverage boundaries are retained in `entry-audit.json`, and the Go owner
   re-reads the daemon's own durable `target-producer-transcript.jsonl` for the
   producer assertions.

This browser provider does not produce delegate lifecycles. The retained-child
relay deadlock and the otherwise-identical schedules with and without an extra
background subscriber are exercised through real hub/daemon components in
`cmd/evener-hub/app_relay_read_progress_test.go`. A passing browser journey
alone does not attribute a live incident to that deadlock.

### Retired legacy journey

The legacy client-side session-cache route and its stores were removed (6d-2b).
The journey that drove that route (`TestBackgroundTranscriptsBrowser` and the
legacy branch of `run.mjs`) was retired with it; the entry route is the only
journey. The entry guard restored the legacy journey's page-back
(`paged-back-sentinel`), reopen (`reopen-without-read`), reconnect
(`reconnected-latest-window`) and narrow-viewport (`narrow-viewport`)
assertions (`cmd/evener-hub/background_transcripts_browser_test.go`).
`assertBackgroundEntryPagingAndReconnect` re-derives the paging, reopen and
reconnect *transport* claims itself from the raw appwire trace (target
`thread/read` and `thread/turns/list` frames at each milestone's own byte
window), and correlates the gap turn with the daemon's durable producer
transcript. It does not recompute every content claim: `paged-back-sentinel`
still checks the driver's own `pages`, `noDuplicates`, `sentinelPresent` and
`historyOrdered` values, and `reopen-without-read` takes the restored-content
booleans `retainedItemsPresent` and `restoredHasSentinel` from the driver.
`assertBackgroundEntryNarrowViewport` re-activates the pane at 420px through the
mobile shell and compares the real before/after item bodies (text, tool output,
arguments, status), not just item identity; it re-derives that comparison from
the recorded payloads and confirms the produced gap content against them rather
than trusting the driver's `contentPreserved`/`sentinelAfter` booleans. The only
part of the legacy journey this guard does not cover is its sustained
two-session batching apparatus.

### Reading placement acceptance

After reconnect paging, the driver positions the real rendered
`BACKGROUND_HISTORY_27` turn and moves it with a native Chrome wheel gesture.
It records the intersecting DOM row and semantic entry, entry offset and height,
viewport dimensions, visible payloads and the exact workspace pane record. Local
hydration and the opening hold must settle before geometry is accepted.

`responsive-reading` requires the same pane record and older row at 1440px and
420px, including the same semantic entry. The fixture reads a short entry's tail:
the independently measured entry bottom stays at the same viewport reading line
even when its height and top offset change. Neither oracle calls the production
capture/reflow helper. Tolerance remains two CSS pixels for integer scroll
read-back rounding, not a different entry or reading point. `new-pane-latest`
starts again at the older row, switches the main session through the real rail,
requires that the old target record is absent, then reopens it through the rail.
The new record must land at the true bottom with the producer's gap turn visible.
Both cases are collected before the responsive assertion is evaluated, so a
responsive failure cannot make the close control vacuously start at latest.

The Go owner independently checks these raw records and the AppWire byte windows
for zero target reads/pages. Both resize payload records retain source keys,
versions and positions alongside every complete body. The `narrow-viewport`
assertion still compares every recorded before/after body. Owner negative controls reject moved
semantic entries, changed reading points (including fixed offsets after reflow),
replaced/reused owners, absent DOM payloads, damaged
bodies and missing evidence. The driver waits for the mobile host and an exposed
rail row before clicking, rather than clicking stale desktop geometry mid-resize.

Acceptance screenshots are `older-desktop.png`, `older-responsive.png`,
`older-before-close.png` and `new-pane-latest.png`. Inspect their visible payloads
as well as the raw records; a file's existence alone is not visual acceptance.

## Auth and proxy prerequisites

- Hub and Vite share the hostname (`127.0.0.1`); cookies are port-agnostic, so
  the hub's `/auth/<token>` capability cookie flows to the Vite origin.
- Before Vite starts, the driver sets `EVENER_HUB_ADDR` to the fixture hub's
  base URL (from `--hub-url`). The dev server (`vite.config.ts`) proxies `/rpc`
  (WebSocket), `/api`, `/auth`, `/doc` and `/s` to it.
- The driver authenticates by navigating to the hub auth URL, then loads the
  guard's own nested HTML through Vite. A failed auth, proxy connection, Chrome
  launch or provider phase fails the guard.

## Fixture phase protocol

The helper's control file (same JSONL format the skill guard uses) accepts:

| command | meaning |
| --- | --- |
| `{"command":"hold"}` / `{"command":"release"}` | skill-guard turn hold (unchanged) |
| `{"command":"shutdown"}` | stop the daemon through the real `thread/shutdown` path |
| `{"command":"background-mode"}` | arm the explicit fixture mode: channel-backed `llm.Stream` turns replace `Complete` |
| `{"command":"background-phase","phase":"<name>"}` | release one named phase channel; the helper logs a `background-phase-ack` line to the request log |

The skill guard never writes `background-mode`/`background-phase`, so its turns
keep taking the `Complete` path unchanged. The phase channels are cancelable
(a cancelled stream context makes `waitBackgroundPhase` return false) and the
control files live in the guard's private temporary directory.

## Evidence

- `appwire-trace.jsonl` — every real hub AppWire frame (the Go test reads it).
- `helper-<name>-requests.jsonl` — actual provider requests plus phase acks.
- the daemons' durable `*.transcript.jsonl` under the fixture state root.
- `entry-audit.json` — raw renderer, rendered-row, output-region, native item,
  turn, and coverage records for the selected entry-store journey.
- `target-producer-transcript.jsonl` — the selected daemon's raw durable
  transcript copied by the Go owner for independent producer assertions.
- `milestones.jsonl`, screenshots and `*.dump.json` in the artifact directory.

The artifact directory survives a failing run for triage and is removed on a
clean one; set `SKILLGUARD_KEEP_ARTIFACTS` (the shared skill-guard flag) to keep
a passing run's artifacts.

## Cleanup

Go fixtures are cleaned up by the test binary's own `t.Cleanup`
(`skillGuardStartHelper`'s daemon stop and the appwire trace close); Chrome and
Vite are cleaned up by `run.mjs`'s `try/finally`. The guard is registered as
never-signalled, so an interrupt waits for that cleanup instead of skipping it.
