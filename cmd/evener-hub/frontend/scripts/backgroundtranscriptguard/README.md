# backgroundtranscriptguard

The whole-journey proof for background Live transcripts. It drives the **real**
hub web app in Vite development mode in real Chrome through a **real** hub
against **two real `evener serve` daemons**, with the only scripted piece at the
external LLM provider adapter. It replaces no Evener internal with a mock.

The guard is registered in `cmd/evener-dev/webbrowser.go` and runs through the
browser gate (`make test-web-browser`) as `web-backgroundtranscriptguard`. The
Gate's launch contract is:

```
go test -tags browserguard ./cmd/evener-hub -run '^TestBackgroundTranscriptsBrowser$' -count=1
```

The narrow selected-session entry-storage proof is an additional command, not a
replacement for the full qualification:

```
go test -tags browserguard ./cmd/evener-hub -run '^TestBackgroundTranscriptsEntrySliceBrowser$' -count=1
```

## What it proves

`TestBackgroundTranscriptsBrowser` (`cmd/evener-hub/background_transcripts_browser_test.go`)
starts the fixture hub (real roster prober + past index + `newWebServer` +
`appserver.NewWebSocketTrace`) and two compiled `evener serve` helper daemons,
then runs `run.mjs`. The driver owns the browser gestures and records
milestones; the Go test owns the daemons, hub and every assertion against what
the daemons actually received (provider request logs, phase acknowledgments,
durable transcripts) and what the real hub AppWire trace recorded.

The journey, in order:

1. The dev entry installs Task 5's tiny budgets (`residentBytes: 64 KiB`,
   `pendingBytes: 32 KiB`) **before** the real app imports; the target session
   produces the before-eviction text as a background subscription, and its
   checkpoint is committed to real IndexedDB.
2. The `after-eviction` phase is released only after that observation;
   `BACKGROUND_AFTER_EVICTION` and the real `shell` tool output are produced
   while the target is unopened, and the target is evicted from the resident map.
3. The target is opened through the rail: literal before/after text and tool
   output render, the trace position is recorded for zero-activation-read
   checking, the turn finishes and each produced item appears exactly once.
4. History paging, disk handoff, reopen-without-read and further paging.
5. A real WebSocket interruption via Chrome network controls, a completed turn
   in the gap, reconnect with an authoritative latest window, and paging back.
6. Narrow-viewport preservation and a bounded sustained two-session workload
   with deterministic batching phases.

Note on step 4: this base has **no dedicated transcript Find surface** (no Find
component, testid or keybinding), so the open reader is exercised as a
`history-holder` — its loaded pages, scroll anchor and resident model are kept
under pressure. A Find-specific holder is not tested because no such surface
exists.

## Narrow entry-slice journey

`TestBackgroundTranscriptsEntrySliceBrowser` uses the same two real daemons,
hub, Vite/Chrome driver, and AppWire trace. Its opt-in `--entry-slice true` mode
selects only session B for the isolated version-3 entry database; the full test
keeps its existing workload and assertions. The browser captures a trace byte
offset after the background baseline, opens the selected pane through the rail,
and proves that the activation interval contains no target `thread/read` or
`thread/turns/list` request. It retains raw renderer items and native IndexedDB
item, turn, and coverage rows in `entry-audit.json`, then checks their keyed
identities and producer-item positions against the recorded coverage boundaries.
It reads output from the rendered shell output `<pre>`, not the command label.
The Go owner independently reads and retains the daemon's
`target-producer-transcript.jsonl`, compares the provider turn and tool-call
identity/content with those rows, and checks the expanded shell output region
contains the exact tool output once. The journey also verifies a live
streaming sentinel is absent from entry rows until completion, and that the
selected ref has no legacy record. The Go owner independently parses the real
hub trace window and rejects a missing initial subscribed read.

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
