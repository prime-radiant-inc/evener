# Hub Web UI Data-Path Corrections Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make local transcript reloads bounded and fast, report running in-process children accurately, and recover Hub-to-daemon relays without replacing the browser connection.

**Architecture:** Add an append-aware transcript index with bounded page projection and integrate it into local daemon and saved-session reads. Keep child lifecycle separate from routability. Turn the Hub relay goroutine into a single supervised logical relay that re-subscribes after an established upstream channel closes. Remove the two avoidable client/server hydration gates that delay first paint.

**Tech Stack:** Go 1.x, JSONL transcripts, AppWire WebSockets, `httptest`, injected clocks/channels, browser JavaScript, JSDOM.

## Global Constraints

- Read and follow `docs/testing.md` before changing tests.
- Default tests must not use provider credentials, external network access, quota, ambient daemons, or wall-clock races.
- Use structured channels and injected clocks instead of sleeps to prove concurrency behavior.
- Preserve AppWire methods, JSON fields, decimal cursor semantics, `turn_N` identities, page order, prelude behavior, failed `api_call` turns, `TurnLimit <= 0` full reads, `ReplaceSubscription`, and local image enrichment.
- Treat a transcript index as disposable acceleration data; the JSONL transcript remains authoritative.
- A parent-owned child may have lifecycle `active` and `Live: false`; lifecycle must not grant routing or action capabilities.
- Keep browser heartbeat and renderer self-heal as defense in depth.
- Do not touch or stage the five pre-existing untracked journal/plan files.

---

## File Structure

- Modify `internal/apptranscript/apptranscript.go`: let cache-owned projection seed tool names for random-access pages.
- Create `internal/apptranscript/turn_index.go`: sidecar schema, validation, suffix advancement, bounded record selection, atomic persistence.
- Create `internal/apptranscript/turn_index_test.go`: reference equivalence, append/truncate/corruption, incomplete-line, work-bound, and benchmark coverage.
- Modify `internal/apptranscript/turn_cache.go`: expose full, latest, and cursor-page cache operations backed by the index.
- Modify `internal/apptranscript/turn_cache_test.go`: cache identity, eviction, and index reuse assertions.
- Modify `server/appwire_turns.go`: stateless indexed transcript page projector and incremental notification reducer.
- Modify `server/appwire_runtime.go`: call bounded snapshot methods; update notification reducer when events are recorded.
- Modify `server/appwire_turns_paging_test.go`: daemon response equivalence and bounded-work tests.
- Modify `server/server.go`: own the notification reducer if a server field is required.
- Modify `cmd/serf-hub/app_threadread.go`: use indexed bounded operations for saved local transcripts.
- Modify `cmd/serf-hub/app_threadread_test.go`: saved-session window/page equivalence and work-bound regression.
- Modify `cmd/serf-hub/web_workspace.go`: metadata-only workspace read and parent-owned child lifecycle projection.
- Modify `cmd/serf-hub/web_test.go`: workspace read-parameter and child state/capability regressions.
- Modify `cmd/serf-hub/assets/renderer.js`: let transcript replay proceed before task descriptions resolve.
- Create `cmd/serf-hub/jstest/test-renderer-hydration-order.js`: unresolved-task deterministic first-paint regression.
- Modify `cmd/serf-hub/jstest/run-all.sh`: include the new focused renderer test.
- Create `cmd/serf-hub/app_relay.go`: supervised relay loop and retry-clock abstraction, extracted from `app_rpc.go`.
- Modify `cmd/serf-hub/app_rpc.go`: construct/start/stop supervised relay handles through the existing registry.
- Modify `cmd/serf-hub/app_rpc_test.go`: scripted upstream replacement, retry, cancellation, and deduplication tests.

---

### Task 1: Append-Aware Bounded Transcript Index

**Files:**
- Modify: `internal/apptranscript/apptranscript.go`
- Create: `internal/apptranscript/turn_index.go`
- Create: `internal/apptranscript/turn_index_test.go`
- Modify: `internal/apptranscript/turn_cache.go`
- Modify: `internal/apptranscript/turn_cache_test.go`

**Interfaces:**
- Consume: existing `EntryProjector`, `TurnsFromFile`, `PreludeTurn`, `appwire.WindowTurns`, and `appwire.PageTurns` semantics.
- Produce:

```go
type FilePage struct {
    Turns      []appwire.Turn
    NextCursor string
}

type ReadStats struct {
    IndexedBytes   int64
    ProjectedTurns int
}

func (c *TurnCache) LatestFromFile(
    path string,
    maxLineBytes int,
    limit int,
    project func(raw json.RawMessage, turnID string, turnIndex int, toolNames map[string]string) []appwire.ThreadItem,
) (turns []appwire.Turn, olderCursor string)

func (c *TurnCache) PageFromFile(
    path string,
    maxLineBytes int,
    cursor string,
    limit int,
    project func(raw json.RawMessage, turnID string, turnIndex int, toolNames map[string]string) []appwire.ThreadItem,
) FilePage
```

`ReadStats` stays test-only through an unexported observer hook in `turn_index.go`; production callers do not depend on it.

- [ ] **Step 1: Write failing reference-equivalence tests**

In `turn_index_test.go`, create one JSONL fixture with a header/system prompt, a first successful `api_call`, ordinary entries, a failed `api_call`, a malformed line, and an incomplete final line. Use a projector that calls `ProjectTurn` and assert:

```go
func TestTurnCacheBoundedPagesMatchFullProjection(t *testing.T) {
    full := TurnsFromFile(path, testMaxLineBytes, project)
    got, cursor := cache.LatestFromFile(path, testMaxLineBytes, 3, project)
    want, wantCursor := appwire.WindowTurns(full, 3)
    if diff := cmp.Diff(want, got); diff != "" { t.Fatal(diff) }
    if cursor != wantCursor { t.Fatalf("cursor=%q want=%q", cursor, wantCursor) }

    page := cache.PageFromFile(path, testMaxLineBytes, cursor, 2, project)
    wantPage := appwire.PageTurns(full, cursor, 2)
    if diff := cmp.Diff(wantPage.Data, page.Turns); diff != "" { t.Fatal(diff) }
    if page.NextCursor != wantPage.NextCursor { t.Fatalf(...) }
}
```

Also assert exact `turn_system`, `turn_N`, timestamps, usage, and the failed-turn error payload. The incomplete final line must not appear until a later append completes it.

- [ ] **Step 2: Run the focused tests and record the expected red failure**

Run:

```bash
GOCACHE=/tmp/serf-gocache go test ./internal/apptranscript \
  -run 'TestTurnCacheBoundedPagesMatchFullProjection|TestTurnCacheBoundedReadCompletesAppendedPartialLine' \
  -count=1 -v
```

Expected: compile failure because `LatestFromFile`, `PageFromFile`, and `FilePage` do not exist.

- [ ] **Step 3: Implement index scanning and bounded selection**

In `turn_index.go`, define a versioned sidecar at `path + ".appwire-index.json"`:

```go
type turnIndexDisk struct {
    Version    int                 `json:"version"`
    TranscriptSize int64           `json:"transcript_size"`
    CompleteSize   int64           `json:"complete_size"`
    Header     transcript.Header   `json:"header"`
    FirstCall  *transcript.APICall `json:"first_call,omitempty"`
    Records    []indexedTurn       `json:"records"`
    ToolNames  map[string]string   `json:"tool_names,omitempty"`
}

type indexedTurn struct {
    Offset int64  `json:"offset"`
    Length int64  `json:"length"`
    Index  int    `json:"index"`
    Kind   string `json:"kind"`
}
```

Scan with `bufio.Reader.ReadBytes('\n')` so offsets and incomplete tails are explicit. Increment `Index` only for transcript `entry` records and failed `api_call` records, matching `TurnsFromFile`. Capture header/first API call for the synthetic prelude. Capture tool call ID/name pairs while scanning so a selected tool-result record can resolve a legacy missing result name without projecting the prefix.

Load the sidecar only when version and transcript prefix metadata match. On growth, seek to `CompleteSize` and scan the suffix. On shrink, replacement, malformed sidecar JSON, or prefix mismatch, rebuild. Persist with `os.CreateTemp` in the transcript directory, `Sync`, `Close`, and `os.Rename`; index-write failure must not fail the read.

Select logical ranges with the same bounds as `WindowTurns` and `PageTurns`. Read only selected records with `ReadAt`, clone the index's prefix `ToolNames` map, and pass that map to the bounded projector so tool results whose persisted `Name` is empty still resolve exactly as in full sequential projection. Project records oldest-first and prepend the synthetic prelude only when its logical position lies in the selected range. For `limit <= 0`, call the existing full projector so legacy behavior remains exact.

- [ ] **Step 4: Add append, replacement, and work-bound tests**

Add tests that:

- warm an index, append one complete entry, and observe only suffix bytes indexed;
- assert latest-40 projects exactly 40 selected turns, not the historical prefix;
- request a 30-turn older page and observe exactly that page projected;
- replace/truncate the transcript and assert a rebuild returns the replacement content;
- corrupt/delete the sidecar and assert transparent rebuild;
- exceed `bufio.Scanner`'s default token size but stay below `maxLineBytes`;
- fill the TurnCache beyond 32 transcript paths and assert LRU eviction still works.

Use an unexported hook:

```go
var observeTurnIndexRead func(ReadStats)
```

Restore it with `t.Cleanup`. Do not assert elapsed time.

- [ ] **Step 5: Add benchmarks**

Add:

```go
func BenchmarkTurnCacheLatest40(b *testing.B)
func BenchmarkTurnCacheLatest40AfterAppend(b *testing.B)
func BenchmarkTurnCacheOlder30(b *testing.B)
```

Build transcripts of 100, 1,000, and 10,000 entries before `b.ResetTimer()`. Report allocations and indexed/projected record counts. Benchmark cold index build separately from warm bounded reads.

- [ ] **Step 6: Run package tests and benchmarks**

```bash
GOCACHE=/tmp/serf-gocache go test ./internal/apptranscript -count=1
GOCACHE=/tmp/serf-gocache go test ./internal/apptranscript \
  -run '^$' -bench 'BenchmarkTurnCache' -benchmem -benchtime=100x
```

Expected: tests pass; warm latest/page projected-turn counts stay fixed as history grows.

- [ ] **Step 7: Commit the index unit**

```bash
git add internal/apptranscript/turn_index.go \
  internal/apptranscript/turn_index_test.go \
  internal/apptranscript/turn_cache.go \
  internal/apptranscript/turn_cache_test.go
git commit -m "perf(appwire): index bounded transcript pages"
```

---

### Task 2: Integrate Bounded Transcript and Notification Snapshots

**Files:**
- Modify: `server/appwire_turns.go`
- Modify: `server/appwire_runtime.go`
- Modify: `server/server.go`
- Modify: `server/appwire_turns_paging_test.go`
- Modify: `cmd/serf-hub/app_threadread.go`
- Modify: `cmd/serf-hub/app_threadread_test.go`

**Interfaces:**
- Consume: `TurnCache.LatestFromFile` and `TurnCache.PageFromFile` from Task 1.
- Produce:

```go
type appTurnSnapshot struct {
    mu     sync.Mutex
    cursor uint64
    turns  []appwire.Turn
}

func (s *Server) appLatestTurns(threadID string, limit int) ([]appwire.Turn, string)
func (s *Server) appPageTurns(threadID, cursor string, limit int) appwire.ThreadTurnsListResponse
```

- [ ] **Step 1: Write failing daemon bounded-read tests**

Extend `server/appwire_turns_paging_test.go` with `TestServerAppWireBoundedReadsDoNotProjectFullTranscript`. Create 200 transcript turns, install the Task 1 observer, call `thread/read` with `TurnLimit: 40`, then `thread/turns/list` with `Limit: 30`. Assert exact response equality with the existing full reference and observed projections of 40 and 30, not 200.

Add `TestServerAppWireNotificationSnapshotAdvancesFromLastSequence`: record a complete turn, read twice, append one delta, and assert the second read applies only records after the saved notifier cursor.

- [ ] **Step 2: Run focused server tests and verify red**

```bash
GOCACHE=/tmp/serf-gocache go test ./server \
  -run 'TestServerAppWireBoundedReadsDoNotProjectFullTranscript|TestServerAppWireNotificationSnapshotAdvancesFromLastSequence' \
  -count=1 -v
```

Expected: bounded-work assertion fails because `appAllTurns` projects the complete transcript and replays notifier history from zero.

- [ ] **Step 3: Refactor notification projection into an incremental reducer**

Extract the local closures in `appTurnsFromNotifications` into methods on `appTurnSnapshot`. Add:

```go
func (s *appTurnSnapshot) Apply(records []appserver.SequencedNotification)
func (s *appTurnSnapshot) Snapshot() []appwire.Turn
```

`Apply` ignores `record.Seq <= cursor`, applies records in sequence order, and updates `cursor`. `Snapshot` returns a read-only snapshot or a defensive slice copy. Keep existing merge semantics for started/completed items and deltas. Initialize lazily from `ReplayAfter(0)`; subsequent reads call `ReplayAfter(snapshot.cursor)`. Add projected notifications directly after `RecordAppEvent` records them so the reducer remains current without rescanning retained history.

- [ ] **Step 4: Replace eager daemon reads with bounded snapshots**

Change `handleAppThreadRead` to call `appLatestTurns` and `handleAppThreadTurnsList` to call `appPageTurns`. The methods compare transcript and notification authority under the current `useTranscriptTurns` rules without constructing a full transcript candidate. For the transcript candidate, use Task 1 bounded operations. For notification data, use the incremental reducer and then existing `WindowTurns`/`PageTurns` over its bounded retained state.

Keep `appAllTurns` only for legacy full reads and reference tests.

- [ ] **Step 5: Write failing saved-session bounded-read tests**

In `app_threadread_test.go`, seed a persisted 200-turn local transcript and assert `pastThreadForRead`/Hub paging returns the same latest/page data as full projection while the Task 1 observer reports only the selected page projected.

Run:

```bash
GOCACHE=/tmp/serf-gocache go test ./cmd/serf-hub \
  -run 'TestPastThread(Read|TurnsList).*Bounded' -count=1 -v
```

Expected: red work-bound assertion because `pastThreadForRead` still calls `TurnsFromFile` for all turns.

- [ ] **Step 6: Integrate indexed reads into saved-session paths**

In `app_threadread.go`, route `ThreadReadParams.TurnLimit` through `LatestFromFile`. Route saved-session `thread/turns/list` through `PageFromFile`. Preserve `projectPastTranscriptTurn`, output-image enrichment, stale-processing status sanitation, and legacy full reads.

- [ ] **Step 7: Run focused and package tests**

```bash
GOCACHE=/tmp/serf-gocache go test ./server ./cmd/serf-hub \
  -run 'AppWire|Thread(Read|Turns)|PastThread' -count=1
GOCACHE=/tmp/serf-gocache go test ./server ./internal/apptranscript ./cmd/serf-hub -count=1
```

Expected: all pass; no response-shape or cursor changes.

- [ ] **Step 8: Commit bounded integrations**

```bash
git add server/appwire_turns.go server/appwire_runtime.go server/server.go \
  server/appwire_turns_paging_test.go \
  cmd/serf-hub/app_threadread.go cmd/serf-hub/app_threadread_test.go
git commit -m "perf(hub): bound local transcript snapshots"
```

---

### Task 3: Remove First-Paint Hydration Gates

**Files:**
- Modify: `cmd/serf-hub/web_workspace.go`
- Modify: `cmd/serf-hub/web_test.go`
- Modify: `cmd/serf-hub/assets/renderer.js`
- Create: `cmd/serf-hub/jstest/test-renderer-hydration-order.js`
- Modify: `cmd/serf-hub/jstest/run-all.sh`

**Interfaces:**
- Consume: existing `Thread.Serf.Capabilities`, `Thread.Serf.ActiveTurnID`, `hydrateDescriptions`, `readThread`, and buffered renderer event machinery.
- Produce no wire changes.

- [ ] **Step 1: Write a failing metadata-only workspace test**

Add `TestLiveWorkspaceSnapshotSkipsTurns` in `web_test.go`. Use `scriptedAppSource` to capture `ThreadReadParams`, return capabilities plus `Serf.ActiveTurnID`, and call `liveWorkspaceSnapshot`. Assert:

```go
if got.IncludeTurns { t.Fatal("workspace metadata requested transcript turns") }
if !caps.Send || activeTurnID != "turn_active" { t.Fatalf(...) }
```

Run:

```bash
GOCACHE=/tmp/serf-gocache go test ./cmd/serf-hub \
  -run '^TestLiveWorkspaceSnapshotSkipsTurns$' -count=1 -v
```

Expected: fail because `IncludeTurns` is currently true.

- [ ] **Step 2: Make the server-rendered workspace metadata-only**

Change only this call:

```go
source.ReadThread(ctx, appwire.ThreadReadParams{Ref: ref, IncludeTurns: false})
```

Retain capability and active-turn extraction from `Thread.Serf`.

- [ ] **Step 3: Write the failing JSDOM hydration-order test**

Create `test-renderer-hydration-order.js` from the minimal workspace fixture used by `test-renderer-liveness-selfheal.js`. Provide deferred promises for `SerfAppwire.tasks` and `SerfAppwire.readThread`. Resolve `readThread` with one user turn while leaving tasks pending; deliver one live notification through the registered callback. After microtasks drain, assert both snapshot and live text are present before resolving tasks. Then resolve tasks and assert the task description replaces the numeric label without duplicating transcript items.

The key test shape is:

```js
let resolveTasks;
const tasksPromise = new Promise(r => { resolveTasks = r; });
window.SerfAppwire.tasks = () => tasksPromise;
window.SerfAppwire.readThread = () => Promise.resolve(snapshot);
// init renderer; emit buffered notification; await microtasks
assert(conversation.textContent.includes("snapshot text"));
assert(conversation.textContent.includes("live text"));
resolveTasks([{id: 1, description: "Indexed task", status: "in_progress"}]);
```

- [ ] **Step 4: Run the JS test and verify red**

```bash
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules \
  node cmd/serf-hub/jstest/test-renderer-hydration-order.js
```

If JSDOM is absent, install it outside the repository once:

```bash
npm install --prefix /tmp/serf-jstest-jsdom jsdom
```

Expected before the fix: snapshot/live transcript assertions fail while the tasks promise remains unresolved.

- [ ] **Step 5: Decouple transcript replay from task descriptions**

Start `hydrateDescriptions()` without using it to hold `descriptionsReady`. Set `descriptionsReady = true` and drain `eventBuffer` during initialization immediately. Let `applyTasks` update task labels when the task promise resolves. Do not move AppWire subscription establishment after hydration; keep `connectAppwire()` first so the snapshot-plus-buffer no-gap behavior remains intact.

- [ ] **Step 6: Run focused UI tests**

```bash
GOCACHE=/tmp/serf-gocache go test ./cmd/serf-hub \
  -run '^TestLiveWorkspaceSnapshotSkipsTurns$' -count=1
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules \
  node cmd/serf-hub/jstest/test-renderer-hydration-order.js
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules \
  node cmd/serf-hub/jstest/test-renderer-liveness-selfheal.js
```

Expected: all pass.

- [ ] **Step 7: Register and commit the hydration regression**

Add the new test to `run-all.sh`, then:

```bash
git add cmd/serf-hub/web_workspace.go cmd/serf-hub/web_test.go \
  cmd/serf-hub/assets/renderer.js \
  cmd/serf-hub/jstest/test-renderer-hydration-order.js \
  cmd/serf-hub/jstest/run-all.sh
git commit -m "perf(web): unblock initial transcript paint"
```

---

### Task 4: Report Parent-Owned Child Lifecycle Correctly

**Files:**
- Modify: `cmd/serf-hub/web_workspace.go`
- Modify: `cmd/serf-hub/web_test.go`

**Interfaces:**
- Consume: `Roster.IsSubagentActive(sessionID string) bool`.
- Produce: consistent workspace and `/state` lifecycle; no routing or capability changes.

- [ ] **Step 1: Write failing active/terminal child tests**

Add a helper that seeds a persisted child meta and a parent roster entry whose `RunningSubagentIDs` contains the child. Add:

```go
func TestWorkspaceDataProjectsRunningInProcessSubagentActive(t *testing.T)
func TestSessionStateProjectsRunningInProcessSubagentActiveButNotLive(t *testing.T)
func TestWorkspaceDataProjectsStoppedInProcessSubagentEnded(t *testing.T)
```

For the running case assert:

```go
if data.State != "active" { t.Fatalf(...) }
if detail.Live { t.Fatal("in-process child became independently routable") }
if detail.Capabilities.Send || detail.Capabilities.Steer || detail.Capabilities.Interrupt {
    t.Fatal("lifecycle state granted live actions")
}
```

For the stopped case remove the child from `RunningSubagentIDs` and assert `ended`. Include an unrelated running child to prove exact ID matching.

- [ ] **Step 2: Run focused tests and verify red**

```bash
GOCACHE=/tmp/serf-gocache go test ./cmd/serf-hub \
  -run 'Test(WorkspaceData|SessionState)Projects.*InProcessSubagent' \
  -count=1 -v
```

Expected: running child is `ended` because the persisted fallback hardcodes it.

- [ ] **Step 3: Implement lifecycle-only projection**

In the persisted local branch of `workspaceData`, derive:

```go
state := "ended"
if s.cfg.Roster != nil && s.cfg.Roster.IsSubagentActive(id) {
    state = "active"
}
```

Use `state` for `State` and `StateLabel`. Leave `apiSessionCapabilities(id, false)` unchanged. Do not add the child to roster routing and do not change `hubIsSessionLive`.

- [ ] **Step 4: Run lifecycle, tree, and thread projection tests**

```bash
GOCACHE=/tmp/serf-gocache go test ./cmd/serf-hub ./cmd/serf-hub/internal/hubcore \
  -run 'Subagent|SessionState|WorkspaceData|BuildTree' -count=1
```

Expected: new workspace tests and existing tree/past-thread active-child tests pass.

- [ ] **Step 5: Commit lifecycle correction**

```bash
git add cmd/serf-hub/web_workspace.go cmd/serf-hub/web_test.go
git commit -m "fix(hub): preserve running subagent lifecycle"
```

---

### Task 5: Supervise Established Hub Relays

**Files:**
- Create: `cmd/serf-hub/app_relay.go`
- Modify: `cmd/serf-hub/app_rpc.go`
- Modify: `cmd/serf-hub/app_rpc_test.go`
- Modify: `cmd/serf-hub/internal/hubcore/config.go` only if the retry clock belongs in `WebConfig` rather than a package-private test seam.

**Interfaces:**
- Consume: `appsource.Source.SubscribeThread`, `appserver.Server.Broadcast`, `SubscriberCount`, and existing `hubRelayHandle` registry semantics.
- Produce:

```go
type relayRetryClock interface {
    Wait(context.Context, time.Duration) error
}

type hubRelayHandle struct {
    ready  chan struct{}
    err    error
    cancel context.CancelFunc
}
```

Keep the public `startRelay`, `startTurn`, `startRelayForThread`, and `stopRelay` closure signatures unchanged.

- [ ] **Step 1: Write a failing post-close recovery test**

Extend the scripted source helper so each `SubscribeThread` call receives the next result from a channel:

```go
type relaySubscribeResult struct {
    notifications <-chan appwire.Notification
    err           error
}
```

Add `TestHubRPCThreadReadRecoversEstablishedRelayAfterSourceClose`:

1. return upstream channel A;
2. read/subscribe from one Hub client;
3. send event A and assert receipt;
4. close upstream A;
5. wait for the scripted source's second subscribe-call signal;
6. return upstream channel B;
7. send event B; and
8. assert the same client receives B without rereading the thread.

Use channels for every transition; timeouts serve only as deadlock guards.

- [ ] **Step 2: Run the recovery test and verify red**

```bash
GOCACHE=/tmp/serf-gocache go test ./cmd/serf-hub \
  -run '^TestHubRPCThreadReadRecoversEstablishedRelayAfterSourceClose$' \
  -count=1 -v
```

Expected: timeout waiting for the second subscribe call because the relay currently returns on channel close.

- [ ] **Step 3: Extract and implement the supervised relay loop**

Move relay-specific constants/types/helpers into `app_relay.go`. Preserve initial synchronous subscribe: it either succeeds and closes `ready`, or stores the error, removes the handle, closes `ready`, and returns the error to all waiters.

After readiness, run:

```go
for {
    if notifications == nil {
        next, err := source.SubscribeThread(relayCtx, subscribeParams)
        if err != nil {
            if retryClock.Wait(relayCtx, backoff.Next()) != nil { return }
            continue
        }
        notifications = next
        backoff.Reset()
    }
    select {
    case <-relayCtx.Done(): return
    case <-idleTick: /* existing double-check retirement */
    case notification, ok := <-notifications:
        if !ok {
            notifications = nil
            continue
        }
        backoff.Reset()
        // existing image enrichment and Broadcast
    }
}
```

Use bounded exponential delays such as 100 ms, 200 ms, 400 ms, up to 5 s. Inject a fake clock in tests; do not sleep.

- [ ] **Step 4: Add retry, cancellation, and deduplication tests**

Add tests for:

- initial subscribe failure still fails both concurrent initiating reads and makes only one subscribe call;
- two recovery failures request increasing bounded delays, then success resets the next delay to the minimum;
- closing the browser while retry waits cancels the wait, stops subscribe calls, and removes the handle;
- zero-subscriber idle retirement stops retries;
- a concurrent reread during recovery joins the existing handle and does not create another supervisor;
- old idle cleanup cannot delete a replacement relay;
- `ReplaceSubscription` still retires the old downstream subscription.

Retain and run all existing relay lifecycle tests rather than replacing them.

- [ ] **Step 5: Run relay tests with race detection**

```bash
GOCACHE=/tmp/serf-gocache go test ./cmd/serf-hub \
  -run 'Relay|Relays|SubscribeFailure|ReplaceSubscription' -count=1
GOCACHE=/tmp/serf-gocache go test -race ./cmd/serf-hub \
  -run 'Relay|Relays|SubscribeFailure|ReplaceSubscription' -count=1
```

Expected: all pass; no duplicate relay or leaked retry loop.

- [ ] **Step 6: Commit relay supervision**

```bash
git add cmd/serf-hub/app_relay.go cmd/serf-hub/app_rpc.go \
  cmd/serf-hub/app_rpc_test.go cmd/serf-hub/internal/hubcore/config.go
git commit -m "fix(hub): recover stalled daemon relays"
```

Omit `config.go` from the add command if no production config change was needed.

---

### Task 6: Cross-Path Verification and Performance Evidence

**Files:**
- Modify only files required to fix defects found by this verification.

**Interfaces:**
- Consume all prior task outputs.
- Produce final test/performance evidence and a clean repository state.

- [ ] **Step 1: Run focused regression suites**

```bash
GOCACHE=/tmp/serf-gocache go test ./internal/apptranscript ./server \
  ./cmd/serf-hub/internal/hubcore ./cmd/serf-hub -count=1
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules \
  timeout 180 sh cmd/serf-hub/jstest/run-all.sh
```

Read every failure and fix its root cause. Do not mute tests or reduce coverage.

- [ ] **Step 2: Run race and static checks**

```bash
GOCACHE=/tmp/serf-gocache go test -race ./internal/apptranscript ./server ./cmd/serf-hub -count=1
GOCACHE=/tmp/serf-gocache go vet ./internal/apptranscript ./server ./cmd/serf-hub
make lint
```

- [ ] **Step 3: Run the broader repository test target**

```bash
make test
```

If the full target exceeds the session runtime, run its documented module commands separately and report the exact incomplete command rather than claiming success.

- [ ] **Step 4: Capture before/after bounded-read evidence**

Run the Task 1 benchmarks and an in-process server benchmark over 100, 1,000, and 10,000 turns. Record:

- cold index build;
- warm latest-40;
- latest-40 after one append;
- older page-30;
- allocations; and
- observed indexed/projected counts.

Acceptance is structural: warm bounded reads project 40/30 turns and append reads index only the suffix. Absolute milliseconds are diagnostic, not a flaky pass threshold.

- [ ] **Step 5: Review the final diff and workspace**

```bash
git diff --check
git status --short
git diff --stat HEAD~5..HEAD
git log --oneline -6
```

Verify the five pre-existing untracked files remain untouched. Remove only scratch artifacts created during implementation.

- [ ] **Step 6: Request independent code review**

Invoke `superpowers:requesting-code-review`. The reviewer must check the approved spec's ten acceptance criteria, concurrency ownership, sidecar corruption/truncation behavior, exact turn/cursor equivalence, and lifecycle-versus-routability separation.

- [ ] **Step 7: Fix review findings test-first and rerun affected verification**

For each valid finding, add or tighten a failing test, implement the root fix, rerun the focused package plus race test when concurrency is involved, and commit the correction without amending earlier commits.
