# Hub and AppWire CPU-efficiency audit

Audited commit: `377ac0867f580bc65981dee425bf83b3f89af72e` (Go 1.27.4, darwin/arm64, Apple M5 Max). Findings are designs only; no source was changed.

## Verdict

Filtered event fanout repeats JSON work once per filtered subscriber while `Server.Broadcast` holds its delivery gate; this is the clearest event-rate cost that multiplies with audience size. Local relay status frames also pass through several JSON capability stampers and recompute hub-owned capabilities for every route. Live thread projections, navigation snapshots, and AppWire request bookkeeping are mostly reused or bounded; the cold past-thread path and activity roster reads are the remaining measurable or linear costs.

## Findings, ranked

### 1. High: Filtered event fanout decodes and re-encodes per subscriber

**Evidence: read.** AppWire notifications enter `appserver.Server.Broadcast` (`internal/appserver/server.go:288-304`), which creates one notification record, routes it, and runs `deliverNotifications` while holding `deliveryMu` (`:336-345`). Each selected connection reaches `Connection.enqueueHistory` and `Filter.Notification` (`internal/appserver/history_content.go:112-119`). For `history/updated` and `overlay/upserted`, `Filter.Notification` unmarshals the payload, applies that reader's selection, then `replaceFields` unmarshals the payload again, marshals replacement values, and marshals the full object (`internal/apphistory/notifications.go:58-85,120-137`). Overlay delta/reset/end frames also decode per filtered subscriber (`:86-117`). The filter holds its own mutex over that work (`:62-63`).

Unfiltered subscribers take the nil-filter pass-through and share the recorded `Params` bytes; filtered subscribers receive a new projected byte slice when fields change. `appwire.NotificationMessage` encodes params once when the server records/broadcasts the event (`appwire/jsonrpc.go:226-247`). The per-subscriber work is therefore filtering and outer-frame writing, not re-projecting every unfiltered payload.

**Scale.** Cost grows with the number of filtered subscribers per delivered event and the size of the history/overlay payload. History updates and overlay upserts pay at least two JSON decodes and two or more encodes per filtered reader; the decode/encode path runs under the server delivery gate, so slow large projections also delay later deliveries.

**Fix design.** Keep selection-specific state in `apphistory.Filter`, but avoid the second full payload decode and per-field re-marshalling. Add a projection helper that decodes the event once into the typed fields plus preserved raw fields, updates the selected fields, and encodes one final frame. Preserve unknown fields and the current behavior for malformed frames, identity-only updates, and overlay deltas. Add tests that compare projected bytes/decoded values for each notification type and prove one subscriber's filter cannot mutate another subscriber's frame.

**How to verify the win.** Add a `BenchmarkFilteredHistoryFanout` in `internal/appserver` with 1, 8, and 32 filtered readers and a representative large history update; compare `ns/op`, `B/op`, and `allocs/op` before and after. Run `go test ./internal/appserver -run 'Test.*(History|Filter|Broadcast)'` plus the benchmark. The acceptance condition is lower per-event allocations and reduced time growth as reader count rises, with identical per-reader projections.

### 2. High: A local relay status event makes repeated JSON capability passes

**Evidence: read.** A source delivery reaches `startAcknowledgedFanout` and `publishTarget` (`cmd/evener-hub/app_relay_fanout.go:110-140`). For every local `thread/status/changed` route, that function computes the fork capability and applies closed-thread, fork, page-before, and voice-arm capability stampers. The three `stampForkCapability`, `stampPageBeforeCapability`, and `stampVoiceArmCapability` functions each call `patchStatusCapabilities` (`cmd/evener-hub/app_relay_types.go:298-331`). Each call unmarshals params twice and marshals capabilities and the containing object (`:336-362`); the closed-thread stamper adds another status pass. The route also runs `applyHubForkCapability` before stamping; the caller's own comment says it scans the roster, resolves the target session, and reads the deletion store (`app_relay_fanout.go:124-139`).

**Scale.** Per local status event, this is at least six JSON unmarshals and six marshals across the three common stampers, plus the closed-thread pass, for each route that receives the status. Capability resolution also repeats per route. Other notification methods skip these status-only branches.

**Fix design.** Replace the chained status stampers at `publishTarget` with one `patchStatusCapabilities` call that applies all four hub-owned capability values to one decoded status object and emits one encoded params object. Reuse a single target capability snapshot for that notification; do not cache it across status events unless ownership and deletion-store invalidation are explicit. Preserve daemon-owned capability keys and pass malformed or capability-free frames through unchanged. Add focused tests for all four hub flags, daemon-key preservation, malformed input, and closed-thread behavior.

**How to verify the win.** Add `BenchmarkRelayStatusCapabilityProjection` in `cmd/evener-hub` using a representative `NotifyThreadStatusChanged` payload and report `ns/op`, `B/op`, and `allocs/op` before and after. Run `go test ./cmd/evener-hub -run 'Test.*(Capability|Status)'` and that benchmark; confirm the output equals the existing chained stamper behavior for live, closed, and malformed frames.

### 3. Medium: Relay fanout wakes four times a second to scan idle routes

**Evidence: read.** `startAcknowledgedFanout` creates a ticker at `r.idleInterval` and selects on it even when there are no deliveries (`cmd/evener-hub/app_relay_fanout.go:12-18,435-460`). The configured interval is 250 ms (`cmd/evener-hub/app_relay_types.go:120-125`). Each tick calls `retireIdle`, which allocates a candidate slice and scans `handle.relayKeys` under `r.mu`; it then calls `SubscriberCount` for candidates, and rechecks those counts before removal (`app_relay_fanout.go:364-433`).

**Scale.** Four wakeups per second per acknowledged relay handle. Each costs O(routes on that handle) under the relay mutex, plus subscriber-count checks for idle command-free routes. The pending delivery queue is bounded at 64, so this is not an unbounded-buffer issue.

**Fix design.** Make the retirement grace period timer-driven only after a route becomes command-free and unsubscribed, and wake the handle when route/subscriber state changes. Reuse the existing route-change signaling where it gives the required wakeup; keep the final `SubscriberCount` and route identity revalidation immediately before deletion to avoid retiring a newly subscribed route. Test both sides of the race: a subscription arriving at the retirement boundary must preserve the route, and a genuinely idle route must still retire within the chosen grace period.

**How to verify the win.** Add an idle-relay benchmark or test counter for `retireIdle` scans at 1, 100, and 1,000 routes over a fixed idle interval. Compare wakeups, allocations, and mutex-protected route visits before and after; run the existing relay idle-retirement and subscriber-race tests. The target is no periodic route scan while no route is eligible for retirement.

### 4. Medium: The first past-thread read scans and allocates in proportion to transcript size

**Evidence: executed.** `BenchmarkPastThreadReadResponseCold` rebuilds the transcript cache per iteration and measures the cold `thread/read` path; `stampDerivedTotalsWith` falls back to `pastTranscriptCache.DerivedTotalsFromFile` when no usable derived answer exists (`cmd/evener-hub/app_threadread.go:1638-1713`; benchmark setup at `cmd/evener-hub/app_threadread_bench_test.go:136-165`). It scans the session span to recover missing cumulative usage and the full failure count, respecting `DivergenceTurn` (`:1644-1678`). Command:

```sh
GOCACHE="$EVENER_SCRATCH_DIR/go-cache" go test ./cmd/evener-hub -run '^$' -bench '^(BenchmarkPastThreadReadResponseCold|BenchmarkPastThreadTurnsListWarm|BenchmarkPastThreadTurnsListColdHandle)$' -benchmem -benchtime=1x
```

The cold read benchmark reported 4,000 rounds / 6.7 MB: **4,396,041 ns/op, 7,605,576 B/op, 4,875 allocs/op**; 25,000 rounds / 42.1 MB: **4,346,000 ns/op, 39,737,824 B/op, 3,426 allocs/op**. Times are single-iteration and noisy; allocations show the transcript-size scaling. The existing transcript cache avoids repeated cold parses within its lifetime, so this is a first-read/cache-miss cost, not every read.

**Scale.** O(transcript bytes and entries) per cache miss, with allocations rising from 7.6 MB to 39.7 MB in these samples. Larger sessions and fork children without cumulative usage pay more; sessions with an existing derived index or a warm cache avoid much of the scan.

**Fix design.** Extend the existing past-derived-totals index used by `stampDerivedTotalsWith` to provide usage and failure totals before the first read, rather than adding another cache subsystem. Keep `DivergenceTurn` as the child-span boundary and keep metadata usage precedence. A missing, stale, or unreadable index must retain the current scan fallback so the UI never reports partial totals as complete. Test fork divergence, usage precedence, failures outside the loaded window, stale/missing index fallback, and cold-read allocation on long transcripts.

**How to verify the win.** Run the command above with `BenchmarkPastThreadReadResponseCold` before and after, and add a matching indexed-cold case. Verify the indexed case avoids `DerivedTotalsFromFile` and sharply lowers `B/op` and `ns/op`; retain the scan-fallback tests.

### 5. Low: Activity reads clone/scan the local roster and sort a fresh result each time

**Evidence: read.** Each `evener/activity/read` request enters `hubActivityRead` (`cmd/evener-hub/app_activity.go:20-30`), calls `cfg.Roster.List()` and filters every local entry (`:48-55`), asks each eligible attached remote host in parallel (`:118-151`), then sorts all returned sessions (`:57-72`). `Roster.List` returns cloned live-entry copies, as the function comment notes (`:75-79`). The request caps explicit refs at 500 (`:16-18`), but an unfiltered request visits all local sessions and all attached hosts.

**Scale.** Per activity request: O(local roster entries) local traversal and clone work, O(attached hosts) remote calls in parallel (each with a 3-second timeout), then O(N log N) sorting for N returned sessions. This is request-driven, not a background poll.

**Fix design.** Add an activity-only snapshot accessor to the existing roster type that returns only the immutable fields `localSessionActivity` needs, avoiding full `LiveEntry` clones while retaining request-time computation of `QuietForMS`. Continue to fetch remotes per request until the contract permits cached remote freshness. Test concurrent roster updates, quiet-time freshness, ref filtering, and identical ordering.

**How to verify the win.** Add `BenchmarkHubActivityRead` with increasing local roster sizes and no remotes; compare allocations and time per request before and after. Run the existing activity handler tests and a remote-host freshness test. Do not use a response cache until its staleness window is explicitly accepted.

## Clean

- Live `thread/list` and `thread/read` use materialized in-memory projections. `server/appwire_read_handlers.go:61-97` lists the root and projected descendants and sorts IDs; `:212-234` captures the read state and keeps transcript work outside the subscription cut. Hub thread listing fans out to selected sources with at most four workers, not one daemon connection per listed session (`cmd/evener-hub/app_threadlist.go:66-123`). Past-history search is limited and past turns use bounded pages.
- Navigation snapshots are reused when source revision and epoch match; `ensureSnapshot` joins or starts one build when stale (`cmd/evener-hub/navigation_service.go:747-797`). `Invalidate` advances the epoch and wakes the builder (`:295-314`); the projection is not rebuilt for every request.
- AppWire client request IDs are monotonic integers, not UUIDs (`appwire/client.go:349-350`). `appserver/dispatch.go:65-125` dispatches only designated slow operations concurrently; ordinary requests do not create a goroutine each. `appwire/jsonrpc.go:129-179` retains result bytes as `json.RawMessage` rather than creating a generic `any` tree; `appwire/client.go:404-429` decodes the typed response directly from those bytes.
- Fanout does not re-encode unfiltered notification params per reader: `NotificationMessage` marshals them once and deliveries copy the record with the raw byte slice (`appwire/jsonrpc.go:226-247`; `internal/appserver/server.go:288-345`). Filter-specific copies are covered in Finding 1. The relay pending queue is capped at 64 (`cmd/evener-hub/app_relay_types.go:122-125`); the notifier's retained history is bounded (`internal/appserver/notifier.go:15-48`).
- Spawn/restart/process polling is user-action lifecycle work, not an idle loop. `waitForRendezvousOrExit` checks the spawn rendezvous every 50 ms until the child exits, matches, or context ends (`cmd/evener-hub/spawn.go:1230-1258`). Restart checks endpoint identity and `/api/health` every 250 ms until the new hub reports a different start time or context ends (`cmd/evener-hub/restart.go:218-250`). `daemonprocess.Wait` checks process exit every 20 ms until exit or cancellation (`internal/daemonprocess/process.go:250-275`). I found no fixed maximum wait duration in these loops; their work ends with the awaited lifecycle event or cancellation.
- Coordinator-provided idle measurements (executed, isolated state roots): `evener hub --addr 127.0.0.1:19744 --no-deploy` with no sessions used **1.07 CPU-s over 120 s**, or **0.54 CPU-s/min and about 0.9% of one core**, with **176 MB RSS**. Idle `evener serve` used **0.07 CPU-s/min**. These are whole-process baselines, not attribution to the relay loop; per-handle scan cost still needs an isolated counter or profile.
- The requested server benchmark passed with one iteration: `GOCACHE="$EVENER_SCRATCH_DIR/server-cache" go test ./server -run '^$' -bench . -benchmem -benchtime=1x` reported `BenchmarkContextFacetPublication`: **46,500 ns/op, 3,744 B/op, 2 allocs/op**. Its fixture describes one scripted request with zero usage and continuation off (`server/context_facet_bench_test.go:11`); it does not demonstrate multi-request behavior.
- The requested AppWire benchmark passed: `GOCACHE="$EVENER_SCRATCH_DIR/server-cache" go test ./appwire -run '^$' -bench . -benchmem` reported `BenchmarkMessageDecodeLargeResponse`: **4,706,657 ns/op, 4,953,863 B/op, 1,020 allocs/op**. Its synthetic payload contains 200 delegate records with repeated task text (`appwire/client_wire_result_test.go:88-113`); treat it as a large-response stress case, not typical traffic.
- The hub benchmark command above passed with `-benchtime=1x`. Warm turns-list pages stayed near 0.55–0.57 MB/op and 1,351–1,418 allocs/op from 4,000 to 25,000 rounds; cold handle reads rose from 4.9 MB/op at 3.8 MiB to 27.5 MB/op at 23.9 MiB. This supports bounded warm paging and size-sensitive cold reads.

## Open questions

- Can past-derived usage and failure totals be made available from the existing index before a first read without increasing transcript write latency or risking stale totals? This determines the priority of Finding 4.
- What activity freshness does the client contract require for remote hosts? A cache could reduce per-request work but would change freshness; the report does not assume a staleness window.
- What relay-idle retirement delay is acceptable if retirement becomes event-driven? Preserve the current 250 ms discovery bound unless the owner explicitly accepts a different delay.
- The documentation reader test and independent copyedit could not be run because no delegate/agent tool is available in this session. The report is new, dated audit evidence, not an evergreen doc; no audit stamp was added.
- Server measurement had one failed attempt with a missing file in its scratch Go cache. A fresh isolated cache retry passed; no benchmark result is reported from the failed attempt.
