# GC and allocation pressure

Lane: garbage collection and allocation pressure. Audited at
`377ac0867f580bc65981dee425bf83b3f89af72e`, Go 1.27.4 darwin/arm64.

Evidence labels: **executed** (a command I ran, output quoted), **read**
(code traced, no run), **suspected** (shape only, with what would confirm).
Where a number came from another auditor's run it says whose.

## Verdict

The largest per-round allocator is not the event stream or the transcript: it
is `llm.estimateInputTokens` marshaling the entire tool-schema list to JSON
three to four times per round just to divide the byte count by four, at
`llm/token_count.go:83`. Second is the whole-history `schema.Turn` shallow copy,
taken three to four times per round in `agent/session_model_call.go:199`,
`:460` (twice, in `foldHistoryBeforeRequest`) and `agent/context_metrics.go:28`,
so a 200-turn session copies ~180 KiB of turn headers each time. What is already
clean: the event envelope (`events.SessionEvent` is 80 bytes and emitting one
allocates **zero** bytes and zero objects, executed), and the tool-definition
cache, `cachedToolDefs` is built once and read by pointer per round. The one
crack in the envelope is watch provenance, which costs 6 allocations per event
on watch-driven turns.

GC is untuned and the daemon pays for it: another auditor measured 157 GC
cycles in a 4.3 s, 200-round scripted run with 387.6 ms of total pause and
18.2% of CPU in `gcBgMarkWorker`. No `SetGCPercent`, `SetMemoryLimit`, `GOGC`
or `GOMEMLIMIT` appears in production code (**executed**, the coordinator's
`rg`), and there is not one `sync.Pool` in the tree (**executed**, see Clean).

## Findings, ranked

Ranked by (CPU saved × frequency) ÷ fix risk.

### 1. High: the token estimate re-marshals the whole tool schema 3–4× per round

**Sites.** `llm/token_count.go:83` (`json.Marshal(req.Tools)`), reached from
`llm/token_budget.go:24` (`ApplyTokenBudget`), reached from
`agent/session_model_call.go:1753` (`budgetModelDispatchRequestWithBudget`),
which runs once per dispatch attempt inside `llm.RetryStream`'s closure
(`agent/session_stream.go:189`). The same estimator also runs at
`agent/session_model_call.go:302` and `:360` (`attachFullHistoryInputEstimate`
→ `:760` `EstimateUsage` → `contextmgr/pressure.go:282`
`cm.estimateTokensFor` → `llm.EstimateMessagesInputTokensForResolved`), at
`:600` (`applyResponsesContinuationShadowEstimate` → `:710`), and at `:732`
(`responsesContinuationWithInputEstimate`).

**Mechanism.** `estimateInputTokens` computes the input-token figure as
"characters ÷ 4". For messages it walks the already-built `[]llm.Message`
(cheap pointer walk). For tools it takes the expensive route:

```go
// llm/token_count.go:80-89
if len(req.Tools) > 0 {
    b, _ := json.Marshal(req.Tools)   // whole schema list, every call
    tokens += len(b) / 4
}
if req.ResponseFormat != nil {
    b, _ := json.Marshal(req.ResponseFormat)
    tokens += len(b) / 4
}
```

`[]llm.ToolDefinition` (`llm/types.go:404`) carries `Parameters map[string]any`,
a generic JSON Schema. `encoding/json` must walk and allocate through nested
`map[string]any`/`[]any` trees, and the output is a fresh byte slice that is
immediately discarded except for its length. The result is not cached anywhere:
`ApplyTokenBudget` runs on every attempt, and the three shadow-estimate sites
run on every request build. That is 3–4 schema marshals per model round, at
~20–40 registered tools (the fixture in `agent/zz_audit_round_test.go:95` uses
20; `grep -c 'reg.Register(' agent/` finds 38 call sites), on a schema list of
tens of kilobytes.

**Evidence.** read. The coordinator's CPU profile independently attributes
70 ms of a 2310 ms, 200-round run to `llm.estimateInputTokens` (**executed**,
their run), the marshals show up under `encoding/json`, which is 16.9% of that
profile. My own attempt to get `-benchmem` bytes/op for one round is described
under Measurement below.

**Scale.** Per attempt: one full schema marshal. Per round: 3–4 (once per
`ApplyTokenBudget` per attempt, plus the shadow/attach sites). Linear in round
count, constant in history length, quadratic-in-nothing. At ~60 KiB of schema
bytes per marshal this is **0.2–0.5 MB per round**: for a 200-round turn that
is 40–100 MB of garbage, which is the same order as the live heap the GC is
cycling against (`3-6 MB` live, `8-13 MB` goal).

**Fix design.** Cache the schema byte length beside the definitions that change
so rarely. `Session.cachedToolDefs` (`agent/session.go:1351`) is already
rebuilt only in `rebuildToolDefinitions` (`agent/session_tool_definitions.go:362`);
add a sibling `cachedToolDefsSize int` computed once there by one
`json.Marshal`, and thread it through:

- add `ToolsSizeBytes int` to `llm.Request` (`llm/types.go`), set where
  `req.Tools` is set (a zero value means "unknown, marshal as today");
- in `estimateInputTokens`, replace the marshal with
  `if req.ToolsSizeBytes > 0 { tokens += req.ToolsSizeBytes/4 } else { b, _ := json.Marshal(req.Tools); ... }`;
- the same for `ResponseFormat` if it is static, else leave it.

Still count chars rather than bytes-of-UTF-8 differently: `len(b)/4` counts
serialized bytes, and `Parameters` is ASCII JSON, so a cached byte count is
exactly equal, not an approximation. That equality is what makes the fix safe,
the estimate value does not change, so no compaction threshold, admission
decision, or continuation plan moves.

**What could break.** Anything that mutates `req.Tools` between the cache and
the estimate. Grep shows `req.Tools` is assigned from `s.cachedToolDefs` at
`agent/session_model_call.go:136` and never appended to; a test that builds a
`llm.Request` by hand keeps `ToolsSizeBytes == 0` and takes the old path. The
regression to pin: `TestRoundTimings_SerializesToJSON`-adjacent golden tests
that assert estimated token counts.

**How to verify the win.** A `testing.B` around `llm.ApplyTokenBudget(req, res)`
with a realistic 20-tool schema list, `-benchmem`: bytes/op must drop by the
schema size (~60 KiB) and allocations/op by 1. Then the coordinator's own
200-round profile: `llm.estimateInputTokens` should leave the top ten.

### 2. High: four whole-history shallow copies per round

**Sites.**
- `agent/session_model_call.go:199`, `historyTurns := append([]schema.Turn{}, s.history...)` on every request build.
- `agent/session_model_call.go:460`, the same copy inside `foldHistoryBeforeRequest`, in a loop of up to `maxFoldAttempts = 2` (`:457`). This is not a compaction-only path: `selectStrategy` returns `contextmgr.NewCompactStrategy(cm)` for the default empty config (`agent/session_init.go:266-267`), so `s.strategy != nil` and the fold body runs every round.
- `agent/context_metrics.go:28`, `hist := append([]schema.Turn{}, s.history...)` on every `ContextStatus()`, which the hub calls per `TOOL_CALL_END` and per `ASSISTANT_TEXT_END` (`server/thread_envelope.go:289`, `:294` list `facetContext`), and which `agent/session_state.go:492` calls again.
- `agent/session_model_call.go:338`, one more re-snapshot inside `rebuildForAppendedSkillTurns`.

**Mechanism.** `schema.Turn` (`agent/schema/turn.go:439`) has 51 fields, 20 of
them pointers or slices, laid out at roughly 920 bytes (arithmetic below, not a
compiler measurement). `append([]schema.Turn{}, s.history...)` allocates a new
backing array and memmoves 920 bytes per turn behind it, then the GC must scan
every one of the ~20 pointers per turn in the copy. The copies share nested
values, so the deep data is not copied, but the 920-byte headers are, and every
copied pointer is a pointer the GC now visits twice.

**Evidence.** read. Struct size arithmetic, from the real field list:
20 pointer-ish fields × 24 B (Go's rounded word pair for
`*T`/`[]T` in this layout) + 24 strings × 16 B + 24 B `time`-ish scalars ≈
**921 B/turn**. At 200 turns one copy is **184,200 B ≈ 180 KiB**; four copies is
**~720 KiB per round**.

**Scale.** 4 copies per round (1 request snapshot + up to 2 fold snapshots + 1
context-status sample), plus 1 more per `TOOL_CALL_END` and per
`ASSISTANT_TEXT_END` on the hub side. Linear in history length. A 200-turn
session pays ~720 KiB of header copies per round and the GC scans each copy's
pointers once.

**Fix design.** Take one snapshot per round and pass it down, and give the
fold a cheap "has anything moved" check instead of a fresh copy:

- In `prepareModelRequestWithError`, `foldHistoryBeforeRequest` already receives
  nothing and copies internally (`:460`). Change its signature to take
  `historyTurns []schema.Turn` (the caller's `:199` snapshot) and only re-copy
  when its own iteration actually needs a private list, which, given
  `ManageContext` takes `*[]schema.Turn` and may replace the slice, means it can
  copy `only when p >= th.checkpoint` or when `len(*history) != len(pass-in)`.
  `MaybeCompact` already short-circuits on `cw <= 0` and on a pressure read
  below threshold; exposing that decision (`cm.WouldCompact(history, sysLen) bool`)
  lets the common no-compaction round skip the copy entirely.
- For `ContextStatus` (`agent/context_metrics.go:28`): the estimator only reads
  the slice (`contextmgr/pressure.go:279`, `:282`). Publish the request
  snapshot the round already took, a `s.lastHistorySnapshot` + revision pair,
  or, simpler, have `ContextStatus` read `s.history` under `s.mu` and call
  `EstimateStatusFor` on it while holding the lock's read, since
  `estimateUsedTokensFor` also takes `s.mu` nowhere. The existing comment at
  `agent/context_metrics.go:26-28` argues the copy is needed because
  `published turns are replaced, not mutated in place`; the same invariant
  makes reading the live slice under the lock safe.

**What could break.** The G4 comment at `agent/session_model_call.go:188-197`
and `:217-221` explains that the second snapshot exists to re-pair the
in-flight-turn boundary with the exact history it guards. Dropping a copy
without moving `turnHistoryBaseline` in the same critical section re-opens the
thinking/signature-loss bug it names. A test must assert that a fold landing
between round start and request build still pairs boundary and history.

**How to verify the win.** `BenchmarkAuditRoundAssemblyPerHistory/snapshot-copy`
in `agent/zz_audit_round_test.go` measures exactly this copy at 50/200/800
turns; it should disappear from the profile when the copies coalesce. Allocation
per round should fall by ~500 KiB at 200 turns, and the 200-round GC-cycle count
should fall proportionally.

### 3. High: every request body is built as a `map[string]any` tree, then marshaled

**Sites.** `llm/providers/chatcompletions/messages.go:65-210` (a
`map[string]any` per message and per content part),
`llm/providers/internal/protocolhttp/call.go:71` (`Prepare`, which prunes the
map by registry field list and then marshals the whole thing at `:112`:
`b, err := json.Marshal(c.Body)`).

**Mechanism.** The request is materialized twice: once as a generic
`map[string]any` tree (one map allocation per message, one per content part,
plus boxed `any` for every scalar and nested slice), then again as the wire
bytes. `Prepare` then mutates the map (registry `Prune`,
`ApplyBodyConstants`) before marshaling. For a history whose body is dominated
by tool-result text, the body is a substantial fraction of the context window,
hundreds of KiB, and both the tree and the bytes are garbage after the
request is sent. `Prepare`'s `httpReq.GetBody` closure at `:118` keeps the bytes
alive for the HTTP client's redirect/retry path.

**Evidence.** read. The coordinator's profile puts `protocolhttp.Prepare` at
80 ms of 2310 ms (**executed**, their run), inside the 16.9% that
`encoding/json` holds.

**Scale.** Once per provider attempt (so 2× on a retried round). Bytes scale
with the serialized body: at 20 turns × ~2 KiB tool results the body is ~40 KiB;
at 200 turns, ~400 KiB. Both the map tree and the byte slice are garbage per
attempt.

**Fix design.** The smallest change that keeps `Prune` working is to stop
re-marshaling per attempt and to shrink what the tree holds: tool-result content
is the bulk, and it is already a `string` in `llm.ContentPart.Text` /
`ToolResultData.Content`. Two options, in increasing risk:

- **Narrow:** keep the tree but pre-size it, `make(map[string]any, N)` at
  `messages.go:86` and friends, and reuse one `bytes.Buffer` per
  `llm.Client` via `json.Encoder` into a pooled buffer instead of
  `json.Marshal`'s fresh slice at `call.go:112`. Saves the tree's growth
  reallocations (map growth is ~2× churn) without changing the shape.
- **Broad:** give `Call` an optional `BuildBody func(w io.Writer) error` so a
  protocol can encode straight from `[]llm.Message` into the writer, and let
  `Prune` skip the map path when `caps.Fields` prunes nothing (the common case:
  `Prune` returns an empty `pruned` slice for a target that sends everything).

**What could break.** `Prune`, `ApplyBodyConstants`, and the `RequestPreparer`
auth hook all mutate the map in place, and their contract is documented at
`call.go:33-40` ("callers must not reuse a Body across calls"). Any caching of
the body across attempts must be invalidated by `ShapeRequest`
(`llm/registry_shape.go:77`) and by `budgetModelDispatchRequest`, both of which
rewrite the request before dispatch.

**How to verify the win.** A `testing.B` around `protocolhttp.Prepare` with a
200-message history, `-benchmem`: allocations/op should fall by the number of
boxed nodes (thousands), bytes/op by the tree size. Then confirm the wire body
is byte-identical for a fixture pair (before/after diff of `p.Body`).

### 4. High: every event takes the session's exclusive mutex; a watch-driven turn allocates 6 objects per delta

**Sites.** `agent/session_provenance.go:8-12`:

```go
func (s *Session) activeCausalProvenance() *provenance.Causal {
    s.mu.Lock()
    defer s.mu.Unlock()
    return provenance.Clone(provenance.NilIfEmpty(&s.activeProvenance))
}
```

reached from `agent/session_events.go:252` (`emit`), which every event goes
through, including every assistant-text delta (`agent/session_stream.go:393`)
and every tool-output delta (`agent/session_tools.go:456`).

**Mechanism.** On the hottest path in the program:

1. `s.mu` is a plain `sync.Mutex` (`agent/session.go:349`), taken
   **exclusively** for a read, once per event. The streaming goroutine and the
   session's own client-mutation handlers, queue handlers and status writers all
   contend for it. Per brief, 500 deltas per turn is 500 exclusive acquisitions
   that can only serialize against writers.
2. `provenance.Clone` (`agent/provenance/provenance.go:33`) builds a copy
   before checking whether there is anything to copy:

```go
out := &Causal{ChainTruncated: p.ChainTruncated}
out.WatchKeys = append(out.WatchKeys, p.WatchKeys...)
out.Chain = append(out.Chain, p.Chain...)
truncateChain(out)
return NilIfEmpty(out)     // returns nil when both are empty
```

For a session with no watch provenance, every ordinary user turn, `p` is nil
(`NilIfEmpty` returns nil at `session_provenance.go:11`), so `Clone` returns
early and the emit path allocates **nothing** (measured below). When the session
does carry watch provenance, every watch delivery, every notification wake,
`Clone` allocates the `Causal` (56 bytes), two slice headers, and one copy each
of `WatchKeys` and `Chain`; `provenance.Entry` is 96 bytes, so a two-entry chain
is most of the cost.

**Evidence.** executed, with a temporary `agent/zz_audit_gc_test.go` (since
deleted) benchmarked through a prebuilt `agent-gcaudit.test` binary,
`-benchmem -benchtime 200x`:

```
BenchmarkAuditEmitTextDelta-18          200  2542 ns/op     1 B/op   0 allocs/op
BenchmarkAuditEmitToolOutputDelta4k-18  200   309.6 ns/op  48 B/op   1 allocs/op
BenchmarkAuditEmitWithProvenance-18     200  1619 ns/op   577 B/op   6 allocs/op
BenchmarkAuditEventsNew-18              200    66.88 ns/op   0 B/op    0 allocs/op
```

Read those as allocation facts, not timings: 200 iterations on a host shared
with nine other auditors makes ns/op noisy (the two plain-emit cases differ 8×
for reasons that are scheduler noise, not code). B/op and allocs/op are
deterministic. The no-provenance case allocates **zero**, so the `&Causal` in
`Clone` is elided entirely when the pointer is nil, that corrects the guess in
my first draft. The watch-provenance case is **577 B and 6 allocations per
event**. `events.SessionEvent` is 80 bytes and `events.New` allocates nothing
(executed, `unsafe.Sizeof` and the benchmark above).

**Scale.** Per event, always: one exclusive acquisition of `s.mu`. Per event on
a watch-originated turn: 577 B and 6 allocations. A watch delivery that streams
a long answer, the delegate and notification wakeups, which is where long
outputs concentrate, pays 500 × 577 B ≈ **288 KiB and 3,000 allocations per
turn**, on top of 500 exclusive lock acquisitions every turn regardless.

**Fix design.** Make the no-provenance case free and the read lock-free:

- `emit` calls `activeCausalProvenance`; change it to read a pointer
  (`s.activeProvenanceSnapshot *provenance.Causal`) published atomically
  whenever `replaceActiveProvenance` / `mergeProvenance` / `finishActiveProvenance`
  mutate it. Those are already the only writers (`agent/session_provenance.go:26`,
  `:29`, `:40`, `:53`, `:55`). `emit` then does an `atomic.Pointer.Load` with no
  lock and no copy, and `prov is nil` in the common case.
- Shrink what the watch case copies. `Clone` copies `Chain` in full and then
  `truncateChain` may copy it again (`agent/provenance/provenance.go:147`:
  `next := make([]Entry, 0, maxDiagnosticChain)` plus two appends). Build the
  truncated chain directly, copy the head, copy the tail, set
  `ChainTruncated`, so a long chain costs one slice, not two.

Aliasing note: a shared pointer means two events on the stream point at one
`Causal`; that is already true of the nested slices today (the copy is shallow)
and no consumer mutates `ev.Provenance` (grep: read-only uses in
`agent/watch_fire.go`, `server/`, `appwire`).

**What could break.** Any consumer that mutates `ev.Provenance` in place would
now affect every other event sharing the snapshot. The test to add: emit two
events under one watch provenance, assert both still carry the
`(watch_id, generation)` key, then `replaceActiveProvenance(nil)` and assert the
next event carries `nil`. `agent/watch_fire_test.go`'s self-influence cases
already assert the first half.

**How to verify the win.** The benchmark above
(`BenchmarkAuditEmitWithProvenance`) is the measurement: 6 allocs/op and
577 B/op should fall to 4 and ~390 B with the single-copy chain. The no-
provenance case must stay at 0 allocs/op, which is the regression guard. For
the lock, a `-mutexprofile` of a streaming round with `s.mu` replaced by an
`atomic.Pointer.Load` should lose the `Session.emit` frame under
`sync.(*Mutex).Unlock`.

### 5. Medium-High: the hub overlay allocates a `[]Change` and its params for every streamed delta

**Sites.** `internal/appoverlay/overlay.go:441-448` (`appendText`) and
`:492-507` (`appendOutput`), both ending in a one-element composite literal:

```go
return []Change{{Method: appwire.NotifyOverlayDelta,
    Params: appwire.OverlayDeltaParams{Key: key, Field: ..., Delta: text}}}
```

reached from `Overlay.Event` (`:223-241`), which the server calls for every
delta once the projection commits (`server/appwire_record_events.go:516`,
`overlayEvent`).

**Mechanism.** Each delta allocates (a) the one-element `[]Change` backing
array, (b) the `OverlayDeltaParams` boxed into `Change.Params any`, and (c) the
`Change` return value's escape. The text itself is appended to `slot.text`
(`:446`, `:501`) which is amortized and correct. For `streamDelta` there is also
a `"stream:" + streamID + ":" + kind` key built per delta when the slot is first
created (`:362`), that is once per round per stream, not per delta, since
`:363` finds the slot afterwards.

**Evidence.** executed, same prebuilt binary, `-benchmem -benchtime 200x`:

```
BenchmarkAuditOverlayStreamDelta-18      200   1276 ns/op    240 B/op   5 allocs/op
BenchmarkAuditOverlayToolOutputDelta-18  200  12206 ns/op  44585 B/op   3 allocs/op
BenchmarkAuditOverlaySnapshot-18         200    947.9 ns/op  1424 B/op  3 allocs/op
```

Per streamed text delta: **240 B and 5 allocations**. That is more than my
read-only guess of 3: the extra two are the `appwire.OverlayDeltaParams` value
escaping into `Change.Params any`, and the `Change` slice growing. The tool
output case shows **3 allocations but 44,585 B/op**: that is the amortized
cost of 4 KiB deltas appending into `slot.output` (`overlay.go:501`), i.e. the
output's own bytes; its allocation *count* is already low, so the fix below
targets the stream case.

**Scale.** Per streamed text delta: 5 allocations, 240 B (executed). At ~500
deltas per turn that is 2,500 allocations and 120 KiB per turn per watched
thread, and the hub runs one overlay per thread. The coordinator's profile
attributes 140 ms to `chatcompletions.decodeStream.func2`; the overlay is the
consumer side of the same event stream, so it runs at the same frequency.

**Fix design.** Return a value, not a slice, for the single-change cases. A
`func (o *Overlay) Event(ev) []Change` can keep its API, but the hot branches
should write into a caller-provided scratch:

```go
// Overlay gains:
type ChangeBuf struct{ changes [2]Change; n int }
func (b *ChangeBuf) push(c Change)   // appends into the array, or grows
// appendText/appendOutput take *ChangeBuf and return nothing
```

and `Event` returns `b.changes[:b.n]` with the buffer owned by
`server`'s projection goroutine (one per thread). Because `Event` already holds
`o.mu` and the caller immediately consumes the slice inside the same commit
(`appwire_record_events.go:516`), a borrowed slice is safe; document it on
`Event`.

**What could break.** The slice's lifetime. `pending = append(pending, ...)`
copies the changes into `pendingAppNotification` before the lock is released in
the caller, so a borrowed slice is consumed immediately, but a test that keeps
the returned `[]Change` past `Event` would see it mutated. Note the contract.

**How to verify the win.** `testing.B` running `Overlay.Event` with an
`AssistantTextDeltaData`: allocations/op should go from 3 to 0–1 (the returned
slice header only, if the buffer is reused). Do the same for
`ToolCallOutputDeltaData`.

### 6. Medium: streamed event payloads retain whole tool outputs and whole arguments

**Sites.** `agent/session_tools.go:454-461`:

```go
const chunk = 4000
for i := 0; i < len(full); i += chunk {
    j := min(i+chunk, len(full))
    s.emit(events.ToolCallOutputDeltaData{..., Delta: full[i:j]})
}
```

and `agent/session_stream.go:~430`
(`Delta: message[len(prev):]`), plus `partialJSONStringField`
(`agent/session_stream.go:539-540`), which allocates `key := `"` + field + `"`
per call.

**Mechanism.** `full[i:j]` is a substring of the tool's entire output string.
Every one of the ~25 events for a 100 KiB output therefore carries a `string`
header whose data pointer is inside the one big 100 KiB allocation, so as long
as **any one** delta is retained (by a subscriber, by the overlay's `[]Change`
until the commit, by a `watch` snapshot), the whole output stays reachable. The
same holds for `message[len(prev):]` against the communicate argument buffer and
for the preview delta against `msg[0:len(b)]`. A 4 KiB chunk is a good wire size
and a bad heap unit.

**Evidence.** read. Go slicing does not copy; the string header is 16 bytes and
its pointer is into the parent.

**Scale.** Per tool call: one 100 KiB-to-multi-MB parent string pinned for the
duration of the delta stream; up to 256 such events in the session's event
channel (`agent/session_init.go:526` buffers 256). Retention, not churn: this
shows up as a larger live heap (and therefore more GC work per cycle) rather
than more allocations.

**Fix design.** Copy on the way out for the chunk, since the chunk is already
copy-sized:

```go
Delta: strings.Clone(full[i:j])
```

`strings.Clone` was added for exactly this and returns a same-size allocation.
Cost: one 4 KiB copy per chunk (~25 × 4 KiB = 100 KiB for the call, which is
the output's own size, the same bytes the wire will send anyway). Benefit: each
retained delta pins 4 KiB instead of the parent. Apply the same at the
communicate-preview delta and in `partialJSONStringField` (replace the
`key := "`+field+`"` allocation with a `strings.Cut` over a pre-built needle
from a small `sync.Map` cache, or index directly for the two fields actually
used).

Also note the send loop's channel is 256 deep
(`agent/session_init.go:526`, `:1205`) and `SessionEvent` is 80 bytes, so the
buffer itself holds only 20 KiB of headers, the retention is entirely in the
payload strings behind them.

**What could break.** Nothing behavioural: `strings.Clone` is byte-identical,
and tests that compare `Delta` values are unaffected. A test that asserts the
delta shares the parent's backing array would break, which would be a test
asserting a non-contract.

**How to verify the win.** `go test ./agent -run TestX -memprofile` on one
tool call returning a 1 MiB output, `pprof -inuse_space`: the deltas' retained
size should drop from ~1 MiB to ~100 KiB. `-benchmem` allocations/op rises by
one per chunk, which is the intended trade (churn for retention).

### 7. Medium-High: GC is untuned at GOGC=100 against a 3–6 MB live heap

**Sites.** No production site: **executed**, the coordinator's
`rg 'SetGCPercent|SetMemoryLimit|GOGC|GOMEMLIMIT' --glob '*.go'` matches
nothing outside a research doc (`docs/superpowers/research/2026-10-20-catalog-ownership/catalog-retention.go:30`
calls `runtime.GC()` but is not built). The daemon entry points are
`cmd/evener/serve.go` and `cmd/evener/main.go`.

**Mechanism.** The GC runs when the heap doubles from the live set. With a live
heap of 3–6 MB the goal lands at 8–13 MB, so the collector comes back every
~5–8 MB of allocation. Since a large share of the program's allocation is
short-lived (the finding list above: schema marshals, request bodies, history
copies, per-event envelopes), the heap is almost entirely young garbage, and
that is the worst case for the default pacer: it triggers often, marks little
that survives, and re-triggers immediately.

**Evidence.** **executed** by the coordinator on a 200-round scripted-provider
run: `GODEBUG=gctrace=1` shows 157 cycles in a 4.3 s run, 387.6 ms total pause
(mean 2.47 ms), live heap after a cycle 3–6 MB, heap goal 8–13 MB. Their CPU
profile attributes 420 ms of 2310 ms (18.2%) to `runtime.gcBgMarkWorker`. Note
that gctrace's own per-cycle `P%` column reads 0% in every cycle: the 18.2% is
mark-assist and background-mark work spread across the round, not pause. My
independent run of `BenchmarkRoundOverhead` under `GODEBUG=gctrace=1` produced
101 cycles over ~4.5 minutes with `P%=0` and a goal that climbed 4→43 MB, that
harness spends its wall time in a 10 ms mock provider latency, not allocating,
so it is not a useful GC measurement and is quoted only for the goal/heap shape.

Arithmetic from the coordinator's numbers: 157 cycles ÷ 4.3 s = 36.5 cycles/s;
at an 8–13 MB goal that is 290–475 MB/s of allocation, and that is a *floor*
(the goal is net of the live set). Pause alone is 9.0% of wall time; the 18.2%
figure is the honest total.

**Fix design.** One small, deliberate pacer change, not a subsystem. In the
daemon's entry (where the process knows it is a long-lived server, e.g.
`cmd/evener/serve.go` before the server starts, and *not* in the library):

```go
// The daemon allocates hundreds of MB/s of short-lived request and event
// garbage against a few MB live. GOGC=100 makes the pacer chase a goal it
// reaches every ~5 MB. Trade RSS for cycles.
debug.SetGCPercent(400)        // or: debug.SetMemoryLimit(256 << 20) with GOGC left at default
```

`SetMemoryLimit` is the better fit if RSS matters more than CPU, because the
pacer then only tightens when the process actually approaches the cap.
Suggested shape: `GOGC=400` and `GOMEMLIMIT=512MiB` as the two knobs, defaults
set in code, overridable by env for a user who cares about memory. This pairs
only as well as findings 1–3 land: tuning a pacer whose input is avoidable
garbage is the second-best answer.

**Scale.** 157 → roughly 40 cycles/s at GOGC=400, so mark work falls ~4×, which
turns 18.2% of CPU into roughly 5%. Cost: heap goal 8–13 MB → 32–52 MB, i.e.
tens of MB of RSS on a host that already runs 100 MB+ binaries
(`evener` is 60 MB on disk).

**What could break.** Memory ceilings. A user running many sessions on a small
VPS would see RSS rise by the GOGC factor of the live set. `SetMemoryLimit` plus
GOGC is what bounds that; a hard `SetGCPercent` alone does not. Pin a test that
asserts the daemon sets a non-default pacer, and an integration test that a
long run does not exceed the limit.

**How to verify the win.** Same harness, same round count: `GODEBUG=gctrace=1`
cycle count and total pause, plus `go tool pprof -sample_index=contentions`-free
CPU profile showing `gcBgMarkWorker` below 6%. Cite both before/after cycle
counts and the RSS delta from `/usr/bin/time -l`.

### 8. Medium: per-round maps allocated for empty sets and rebuilt for a read

**Sites.**
- `agent/session_model_call.go:204`, `suppressedDocuments := make(map[string]struct{}, len(s.rejectedToolResultDocuments)+len(s.pendingToolResultDocuments))`, then two copy loops, per round. When both source maps are empty (the normal case: no document was ever rejected) this allocates a zero-length-capacity map header plus two no-op loops. Only `replayScope.dropsToolResult` (`:227`) reads it.
- `agent/session_conversation_provenance.go:305-317`, `snapshotConversationCoverageLocked` allocates `make(map[string][]string)` and walks all history every round, whether or not any turn carries a `ConversationCoverageRef`.
- `agent/session_stream.go:352-356`, `consumeModelStream` allocates four maps and a slice per attempt: `toolArgs map[string]*strings.Builder`, `toolNames map[string]string`, `communicateText map[string]string`, `communicatePreviewStarted map[string]bool`, `communicatePreviewOrder []string`. The first two are needed; `communicateText` and `communicatePreviewStarted` are only touched when the round actually calls the result tool (`:461`, `:435`).

**Mechanism.** `make(map[...])` with a zero size still allocates the `hmap`
header, and it is per round or per attempt. `snapshotConversationCoverageLocked`
is worse: a full 51-field walk of history per round to build a map that is
almost always empty.

**Evidence.** read.

**Scale.** Per round: 2–3 map headers plus one full history walk
(`:305`). Per attempt: 5 map/slice headers. Small in bytes (a few hundred B),
but each is a GC-visible object, and the history walk at `:305` is linear in
turns on every round.

**Fix design.** Make the empty cases free:

- `suppressedDocuments`: hold it as a `map[string]struct{}` only when
  `len(s.rejectedToolResultDocuments)+len(s.pendingToolResultDocuments) > 0`;
  when zero pass a nil map and let `dropsToolResult` return `false`
  (`replayScope.dropsToolResult` is already nilable, check `scope.dropsToolResult != nil`
  at its use site). Cheapest version: `if len(...) > 0 { build }` with a
  `var suppressedDocuments map[string]struct{}`.
- `snapshotConversationCoverageLocked`: scan for the first
  `turn.ConversationCoverageRef != ""` and return nil early. The caller
  (`conversationInputsWithCoverageLocked` at `:319`) already handles a nil map
  via `covered, ok := coverage[...]`.
- `consumeModelStream`: allocate `communicateText` and
  `communicatePreviewStarted` lazily inside `emitCommunicatePreview` (`:426`),
  which is only reached when the result tool's arguments stream in. The
  `communicatePreviewOrder` slice can start nil and grow (it is appended to at
  most once per preview).

**What could break.** Nil-map reads are fine in Go; nil-map *writes* panic.
`emitCommunicatePreview` writes `communicateText[callID] = message` and
`communicatePreviewStarted[callID] = true` (`:428`, `:436`), so the lazy init
must be at the top of that closure. `suppressedDocuments` is only read.

**How to verify the win.** `-benchmem` on `BenchmarkRoundOverhead`: allocations
per round minus 3–5. Real check: a `pprof -alloc_objects` top-ten list for a
one-round run should lose the `snapshotConversationCoverageLocked` entry.

### 9. Medium: struct size and pointer density make the GC scan work it need not

**Sites.** `agent/schema/turn.go:439` (`schema.Turn`, 51 fields, 20 pointer-ish),
`agent/session.go:109` (`Session`, 100+ fields),
`appwire/types_turn.go:183` (`ThreadItem`, 10 pointer-ish fields),
`appwire/types_turn.go:500` (`OverlayItem`, 6 strings + a `ThreadItem`).

**Mechanism.** `Turn` is the unit the collector sees most often: it is copied
four times per round (finding 2), stored once per transcript entry, and walked
by both estimators. Its 20 pointers per turn mean a 200-turn history presents
~4,000 pointers to the marker, per copy, per cycle. `Session` is a single
object so its size matters only as a root scan, but it is 100+ fields with
many maps and slices, so it has a wide pointer fan-out.

**Evidence.** executed for the sizes I could take directly (`unsafe.Sizeof`
through the prebuilt binary), read + arithmetic for `schema.Turn` (which lives
in the `agent` module and was not in the test binary's import closure when I
wrote it):

```
events.SessionEvent        80 B     llm.Message              72 B
events.ToolCallOutputDeltaData 48 B  llm.ContentPart         112 B
events.ToolCallEndData    152 B     llm.Response            264 B
events.SessionStartData   192 B     provenance.Entry         96 B
events.RoundTimings        88 B     provenance.Causal        56 B
agent.Session            7416 B
```

`Turn` field census by pattern over the struct body: 51 exported fields, 20
starting with `*` or `[]`, 24 `string`, remainder scalars and embedded values;
≈921 B by arithmetic. Label that one **suspected** until someone adds
`unsafe.Sizeof(schema.Turn{})` to a test in the `agent` package, it is the
single number most of this report's byte totals scale from.

**Scale.** Scan cost, once per GC cycle over the live set: with 157 cycles per
4.3 s and a few thousand live Turn pointers, this is a steady tax on
`gcBgMarkWorker`, the same 18.2% the coordinator measured. It does not grow
with speed of the CPU; it grows with history length and session count.

**Fix design.** No interface change is worth it here; two cheap moves:

- Stop copying the headers (finding 2). Four copies of the same 200×920 B array
  is four times the pointer set the marker must walk.
- Split the cold half of `Turn` behind one pointer. `Turn` carries ~15 fields
  that are non-nil on a small minority of kinds (`AskResolution`,
  `AttentionResolution`, `CommandInput`, `Communicate`, `Completion`,
  `ConversationFrontier`, `ConversationProvenance`, `DelegateDeliveryCommits`,
  …). A `TurnExtras *TurnExtras` holding those collapses ~10 pointers to 1 and
  shrinks the struct by ~150–200 B, at the cost of one indirection on the rare
  paths. That is a schema/transcript-format change (`json` tags move), so it is
  a bigger project than the rest of this list; it is the structural fix, not the
  first move.

**What could break.** Any change to `Turn` changes the transcript line format,
which `decodeStrictJSON` (`agent/transcript/transcript.go:284`) decodes with
`DisallowUnknownFields`, so a reader of the wrong build fails loudly on every
entry. That is a deliberate property (see the `kata kq8c` note at `:288-296`)
and any split must keep the JSON shape stable by embedding.

**How to verify the win.** `unsafe.Sizeof(schema.Turn{})` in a one-line test
before and after; and `GODEBUG=gctrace=1` with `gc` scan statistics
(`GODEBUG=gctrace=1` plus `pprof -sample_index=alloc_space` on the live set):
mark time per cycle should fall with the pointer count.

### 10. Low-Medium: per-watch `fmt.Sprintf` and per-match snapshots when watches exist

**Sites.** `agent/watch_fire.go:36` and `:38`:

```go
deliveries = append(deliveries, jm.watchSendSnapshot(cfg, dec.watchedIdentity,
    fmt.Sprintf("event: %s", kind), ev).withSelfInfluence(...))
...
n := jm.watchNotificationFromWatch(cfg, dec.notifyJobID,
    fmt.Sprintf("event: %s", kind), ev.Provenance)
```

**Mechanism.** `onSessionEvent` runs for **every** session event
(`agent/session_events.go:265`), holding `jm.mu` across the loop over
`jm.watches` (`watch_fire.go:19-54`). Even with no watches the function still
allocates nothing and returns, that part is clean. With a watch present, each
*matched* event builds an `"event: " + kind` label with `fmt.Sprintf` (reflective
formatting, one allocation plus the wrapper), a `watchSendSnapshot`, and a
`withSelfInfluence` copy. `kind` is an `events.EventKind` (a string type), so
`"event: " + string(kind)` is a non-reflective concat.

**Evidence.** read. Also note the event channel consumer path: `onSessionEvent`
runs on the emitter's goroutine inside `Session.emitWithProvenance`, so a watch
match adds `fmt` reflection to the session's streaming loop.

**Scale.** Per matched event per watch. A wildcard watch on the full event
stream matches hundreds of events per turn; a specific-kind watch matches a few.
Grows linearly in watch count.

**Fix design.** Replace both `fmt.Sprintf("event: %s", kind)` calls with
`"event: " + string(kind)`, and hoist the result into a local computed once per
match (`label := "event: " + string(kind)` before the branch). Optionally skip
the whole loop when `len(jm.watches) == 0` before taking `jm.mu`, a
lock-free early return for the overwhelmingly common session with no watches.

**What could break.** The label text is asserted in watch tests and may appear
in persisted watch-send records; `"event: " + string(kind)` produces identical
bytes, so only a test that asserts on the *allocation* changes. The
`len(watches) == 0` early return must still run `publishWatchActivity` for the
deferred cleanup at `:13`, guard only the lock-and-loop, not the defer.

**How to verify the win.** A `testing.B` that installs one wildcard watch and
calls `jm.onSessionEvent` with an `AssistantTextDeltaData`: allocations/op should
drop by 1, and a `-mutexprofile` of a session with no watches should show
`jobManager.mu` gone entirely.

## Top ten allocation sites by bytes per model round

Assumptions stated so the table is checkable: 200-turn history, ~920 B per
`schema.Turn` (field census in finding 9), ~20 tool definitions totaling ~60 KiB
of schema JSON, request body ~400 KiB (200 turns × ~2 KiB tool-result text),
one provider attempt per round unless noted. Bytes are **per round**; "×/round"
is how many times the site runs. Sources: `read` (struct/scaffold arithmetic)
unless the Evidence column says executed. The one executed column is the
coordinator's CPU profile share, which places each site's cost, not its bytes.

| # | Site | ×/round | Bytes/round (est.) | Mechanism | Evidence |
|---|---|---|---|---|---|
| 1 | `llm/token_count.go:83`, `json.Marshal(req.Tools)` | 3–4 | 180–480 KiB | Whole schema list marshaled to measure its length ÷ 4; result discarded | read; 70 ms in coordinator profile |
| 2 | `llm/providers/internal/protocolhttp/call.go:112`, `json.Marshal(c.Body)` | 1–2 | 400–800 KiB | Full request body encoded to bytes after being built as a map tree | read; 80 ms `Prepare` in profile |
| 3 | `llm/providers/chatcompletions/messages.go:65-210`, `map[string]any` per message/part | 1–2 | 200–400 KiB | Request materialized as a generic map tree before marshal; one map per node | read; inside the 16.9% `encoding/json` |
| 4 | `agent/session_model_call.go:199` + `:460` (×2), `append([]schema.Turn{}, s.history...)` | 3 | 540 KiB | 3 × 180 KiB of `Turn` headers copied; 20 pointers each for the marker | read |
| 5 | `agent/context_metrics.go:28`, history copy in `ContextStatus` | 1/round + 1 per `TOOL_CALL_END` + 1 per `ASSISTANT_TEXT_END` | 180 KiB + 180 KiB per item event | Hub re-samples `facetContext` per item event and each sample copies all history headers | read |
| 6 | `agent/session_model_call.go:2161` (`expandHistory`) via `:283` | 1 | ~41 KiB + text | Builds `[]llm.Message` for the whole history (56 B + 24 B/part) | read |
| 7 | `llm/providers/chatcompletions/stream.go:174`, `json.Unmarshal([]byte(data), &chunk)` | 1 per SSE event (~500+ for a long turn) | 20–60 KiB | Converts the `[]byte` SSE payload to a string, then the decoder allocates a chunk struct with strings per delta | read; `decodeStream.func2` is 140 ms in the profile |
| 8 | `agent/session_events.go:409`, `provenance.Clone` on a watch-driven turn | 1 per event (~525/round), watch turns only | 288 KiB on a watch turn, 0 otherwise | 6 allocations/577 B per event when the session carries watch provenance; **zero** allocations when it does not (executed) | executed |
| 9 | `internal/appoverlay/overlay.go:446`, `:506`, `[]Change` + params per delta | 1 per delta (~525/round) | ~45 KiB | One-element slice and boxed params allocated per streamed delta | read |
| 10 | `agent/session_model_call.go:204` + `session_conversation_provenance.go:305`, per-round maps and a full history walk | 1 each | ~2 KiB + O(turns) CPU | Maps allocated even when both source sets are empty; a 51-field walk per round for a map that is usually empty | read |

Not in the top ten by bytes but worth naming because it is free to fix: four
maps plus a slice per stream attempt (`agent/session_stream.go:352-356`), of
which two are only used when the round calls the result tool.

## Measurement

Commands run, with the honest state of each.

- `GODEBUG=gctrace=1 go test ./agent -run '^$' -bench 'BenchmarkRoundOverhead' -benchmem -benchtime 3x`
  with `GOCACHE=$EVENER_SCRATCH_DIR/gocache-gc`, `-count 1`, log tee'd to
  `$EVENER_SCRATCH_DIR/gc-round-bench.log`. **Did not finish**: killed at ~6
  minutes (the brief's budget is ~4) with no `Benchmark` result line printed.
  What the log does contain, and all it is good for: 101 `gc N` lines, every one
  reporting `P%` of 0, with the heap goal climbing `4 MB → 43 MB` over the run.
  The harness spends its wall time in a 10 ms mock provider latency
  (`agent/round_timings_test.go:158`), so its allocation rate, and therefore
  its GC pressure, is nothing like a real turn's. This run is why the report
  does not quote a measured bytes/op for one round.
- `go test ./agent -bench 'BenchmarkRoundOverhead' -benchmem -benchtime 1x -memprofile $EVENER_SCRATCH_DIR/gc-round.mem`,
  also killed before printing a result line, before the profile was written.
  No `-memprofile` top-sites list exists.
- **executed**, and the source of findings 4 and 5's numbers: a temporary
  `agent/zz_audit_gc_test.go` (since deleted) compiled with
  `go test -c ./agent -o $EVENER_SCRATCH_DIR/agent-gcaudit.test`, the compile
  did finish, then run directly as `./agent-gcaudit.test -test.run ... -test.bench ... -test.benchmem -test.benchtime 200x`,
  so no further build-cache work was needed and no other auditor's build was
  contended. Results quoted verbatim in findings 4 and 5:

  ```
  BenchmarkAuditEmitTextDelta-18            200    2542 ns/op      1 B/op   0 allocs/op
  BenchmarkAuditEmitToolOutputDelta4k-18    200    309.6 ns/op    48 B/op   1 allocs/op
  BenchmarkAuditEmitWithProvenance-18       200    1619 ns/op    577 B/op   6 allocs/op
  BenchmarkAuditEventsNew-18                200     66.88 ns/op    0 B/op   0 allocs/op
  BenchmarkAuditOverlayStreamDelta-18       200    1276 ns/op    240 B/op   5 allocs/op
  BenchmarkAuditOverlayToolOutputDelta-18   200   12206 ns/op  44585 B/op   3 allocs/op
  BenchmarkAuditOverlaySnapshot-18          200    947.9 ns/op  1424 B/op   3 allocs/op
  ```

  `TestAuditStructSizes` in the same binary printed the sizes quoted in
  finding 9. The ns/op figures are noisy (shared host, 200 iterations); the
  B/op and allocs/op figures are deterministic and are what I use.
- The GC numbers used in finding 7 are the coordinator's, from their 200-round
  scripted-provider run: 157 cycles / 4.3 s, 387.6 ms total pause, live 3–6 MB,
  goal 8–13 MB, 18.2% in `runtime.gcBgMarkWorker`. Labelled executed (theirs).
- `sync.Pool` census: `grep -rn "sync.Pool" --include=*.go . | grep -v _test`
  → zero matches (**executed**).

**What I could not measure.** Bytes per round for a real session end to end (no
finished `BenchmarkRoundOverhead`, no `-memprofile`, so the top-ten byte
totals in the table are arithmetic over measured struct sizes and read call
counts, not a profile); the tool-schema byte size for the real registered tool
set (finding 1 scales directly with it, and nobody has printed it); the
live-heap contribution of the retained event payloads in finding 6
(`-inuse_space` on a 1 MiB tool output); the number of `json.Unmarshal` calls
the chat-completions decoder really makes per turn (the profile names the
function, not the count); `unsafe.Sizeof(schema.Turn{})`; and the mutex
contention of `activeCausalProvenance` (no `-mutexprofile` was produced).

## Clean

Checked and already efficient; do not re-audit.

- **Event envelope.** `events.SessionEvent` (`agent/events/events.go:189`) is
  80 bytes: `Kind string`, `Timestamp time.Time`, `SessionID string`,
  `Data EventData` (2 words), `Provenance *Causal`. **executed**: emitting one
  text delta costs `0 B/op, 0 allocs/op`, and `events.New` alone costs
  `0 B/op, 0 allocs/op`, the payload is boxed into the interface with no
  heap allocation, the envelope is a value, and it is sent on one buffered
  channel (`agent/session_init.go:526`, 256 deep). `events.New`
  (`agent/events/eventdata.go:29`) is `time.Now().UTC()` plus three field
  assignments. `KindOf` is a virtual call to a one-line method, not a string
  switch. Do not add a pool here; there is nothing to pool.
- **Tool definitions are cached.** `s.cachedToolDefs` is built once
  (`agent/session_tool_definitions.go:362`) and read by pointer per round
  (`agent/session_model_call.go:136`), with a copy taken only where ownership is
  needed (`:222`, `ToolDefinitions()` at `:24-28`). The schema *list* is not
  rebuilt per round, only its JSON length is recomputed, which is finding 1.
- **The system prompt is cached.** `s.cachedSystemPrompt`, read at
  `agent/session_model_call.go:135`; no per-round prompt assembly on the hot
  path.
- **No `runtime.GC()` and no `debug.FreeOSMemory` in production code.**
  `runtime.GC()` appears only in a non-built research doc
  (`docs/superpowers/research/2026-10-20-catalog-ownership/catalog-retention.go:30`).
- **The overlay caps its retained output.** `maxRunningOutputBytes = 256 << 10`
  and `trimmedRunningOutputBytes = 192 << 10`
  (`internal/appoverlay/overlay.go:41-42`), and `keepTail` (`:641`) copies into
  a buffer of exactly the trimmed size so "a huge delta does not stay pinned by
  the capacity it arrived in", the substring-retention problem finding 6
  names is already solved on the overlay's side.
- **The SSE parser does not retain its read buffer.** `sseParser.flush`
  (`llm/sse.go:83`) does `append([]byte{}, b...)`, a right-sized copy of the
  event, not a slice of the 4 KiB reader buffer. `parseSSEBlocking` (`:132`)
  reuses one `sseParser` for the whole stream.
- **`StreamAccumulator` rebuilds lazily.** `PartialResponse`
  (`llm/stream_accumulator.go:177`) rebuilds only when `dirty`, and `Process`
  sets `dirty` only on real content. The one per-delta rebuild that does happen
  is on the **unused** `StreamGenerate` path
  (`llm/stream_generate.go:224`): `StreamGenerate` has no caller outside its own
  file, so the per-text-delta `acc.PartialResponse()` there costs nothing in the
  daemon.
- **Struct returns are by value on purpose and cheap.** `evaluateWatchEvent`
  (`agent/watch_fire.go:143`) returns a 48-byte `watchEventDecision` and
  `eventSnapshot` returns a 100-byte value; both stay on the stack. Do not
  "fix" these to pointers.

## Open questions

1. **GC tuning is a memory-for-CPU trade Jesse should set.** Finding 7 proposes
   `GOGC=400` with `GOMEMLIMIT=512MiB`. The RSS rise (heap goal 8–13 MB →
   32–52 MB) is small in absolute terms but it is a change to the daemon's
   memory profile on small hosts; that is Jesse's call, not an installer's.
2. **The `Turn` field split (finding 9) changes the on-disk transcript shape.**
   `decodeStrictJSON` deliberately rejects unknown fields
   (`agent/transcript/transcript.go:284`), so a build that splits `Turn` must
   keep the flattened JSON or it cannot read transcripts written by an older
   daemon. Worth a ruling before anyone schedules the work.
3. **Copying delta strings (finding 6) trades allocations for retention.** One
   extra 4 KiB copy per 4 KiB chunk to stop pinning a multi-MB output. If Jesse
   prefers minimum bytes-allocated over minimum bytes-retained, the copy is the
   wrong call for short-lived tool outputs, the trade is visible only with
   `-inuse_space` on a real 1 MiB output, which nobody has run.
4. **`sync.Pool` is absent tree-wide.** Nothing in this report needs one: the
   wins above are all "stop allocating" or "size it right", which is the better
   order. If the request body (finding 3) gets a buffer, pool it rather than
   introducing a second mechanism.
