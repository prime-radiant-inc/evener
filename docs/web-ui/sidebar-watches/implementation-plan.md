# Implementation plan: watches in the sidebar and activity panel

Decisions already taken by Jesse:

- Direction **A** (inline watch rows, count on the summary line).
- **No “next firing.”** Show cadence (`every 10m`) and state (`armed`), which is
  true for every watch kind.
- **Add the projection**, purely additive. No wire version bump.
- Fluent expanded view for the activity panel (round 3).

Nothing here changes watch scheduling.

## Why no version bump is correct

`appwire.ProtocolVersion = "evener-appwire-v5"` (`appwire/types.go:25`) is a
handshake constant, compared with `==` at spawn and roster checks. It is not a
schema hash. A new `omitempty` field is invisible to the equality checks in both
directions: an old daemon simply omits it, and an old hub ignores it. The
frontend must therefore treat the watch list as **absent-able**, not required.

## The path (mirrors the existing jobs plumbing)

```
agent.Session.DetailedStatus          agent/status.go
  → agent DetailedStatus.Watches      (projection of live watch summaries)
  → appwire.EvenerDiagnostics.Watches appwire/types.go:894
  → (clone)                           appwire/clone.go:187
  → server projection                 server/appwire_runtime.go:2303
  → hub thread.Evener.Diagnostics     cmd/evener-hub/app_threadread.go
  → TreeNode / NavigationSessionSummary
                                      cmd/evener-hub/web_api_tree.go:649,
                                      cmd/evener-hub/navigation_projection.go:1091
  → frontend NavigationSessionSummary cmd/evener-hub/frontend/src/protocol/types.gen.ts
```

TS types are reflection-derived by `internal/appwirets/emit.go:EmitCatalog`, so a
new Go field reaches the frontend through `make generate`. A drift test
(`internal/appwirets/emit_test.go:TestGeneratedFileCurrent`) fails until it runs,
so generation is not optional.

## Slices

### Slice 1 — daemon projection and wire type

- `agent/status.go`: add `WatchStatusInfo` and populate `ds.Watches` from the
  jobManager's live watches. The existing `liveWatchSummaries()`
  (`agent/job_watch.go:2376`) returns only `{id, source, condition, deliveries,
  createdAt}` with cadence and note flattened into prose. Add a sibling
  `liveWatchStatuses()` that reads the same `*watchConfig` values and returns
  structured fields, leaving `liveWatchSummaries` and the model-facing
  `job_list` output untouched.
- Fields: id, source, target, send_to, note, cadence kind (`after` / `every` /
  `progress` / `output` / `events`) plus its seconds, output match, events,
  deliveries, created_at, active, end_reason.
- `appwire/types.go`: `EvenerWatchInfo` and `Watches []EvenerWatchInfo` on
  `EvenerDiagnostics`, `omitempty`.
- `appwire/clone.go`: deep-copy the new slice in `cloneEvenerDiagnostics`.
- `server/appwire_runtime.go`: project it in the same place jobs are projected.
- `make generate` to refresh `types.gen.ts`.

**Acceptance:** `go test ./agent/... ./appwire/... ./server/... ./internal/appwirets/...`
passes, plus a new test that a session with one timer watch and one output watch
projects the expected structured fields, and the existing `job_list` output is
byte-identical.

### Slice 2 — hub carries watches to the session summary

- `cmd/evener-hub/web_api_tree.go`: a `diagnosticsWatches()` helper next to
  `diagnosticsJobs()` (`:653`), feeding the tree entry.
- `cmd/evener-hub/navigation_projection.go:1091`: carry the list (or just the
  count plus bounded rows) on `NavigationSessionSummary`.
- `hubapi/navigation.go`: the Go struct for the summary.
- Absence from an older daemon must yield an empty list, never an error.
- A subtree rollup must count each watch once: a receiver watch counts for its
  receiver, not its owner.

**Acceptance:** `go test ./cmd/evener-hub/...` passes, with a test that an entry
whose diagnostics omit `Watches` still builds a summary, and one that a summary
carries the projected rows.

### Slice 3 — frontend: count and fold-out rows

- `railNodes.ts`: a `WatchRailNode` kind in the `RailNode` union, watch children
  in `splitChildren`, and a watch count in `activeWorkSummary`.
- `RailRow.tsx`: the watch row (drawn SVG clock, note as title, cadence + state
  as the gloss), and the count segment in `activityGloss`, inserted **before**
  branch so ellipsis never eats it.
- The gate: `showsActivity` gains `watches > 0`, so an idle session whose only
  pending work is a watch still shows its count. This is a deliberate amendment
  to “a quiet row is one line”.
- Neutral ink only; danger reserved for runaway drops. Cap many watches with a
  `+N more watches` note.

**Acceptance:** the frontend unit gate for the touched files passes, plus tests
for: the count on an otherwise-quiet watch-bearing row, the count placed before
branch, neutral (not `--alive`) styling, and the many-watches cap.

### Slice 4 — frontend: fluent activity panel

- The watch block per `mockups/round-3-fluent.html`: note as prose, facts as one
  sentence, and a delivery timeline drawn only from real timestamps.
- The timeline needs per-delivery instants, which the live registry does not keep
  (it holds a count). Two options, in order of preference:
  1. add a bounded in-memory ring of recent delivery instants (small, additive,
     no scheduling change) and project them; or
  2. ship the block without the timeline and add it later.
- Do not draw a timeline for an output or event watch; say why in one line.

**Acceptance:** the frontend unit gate passes, plus tests for: prose note
rendering, the facts sentence, timeline marks from supplied instants, and the
no-schedule case rendering the explanatory line instead of a timeline.

## Risks

- **Old daemon, new hub:** the field is absent. Every consumer treats a missing
  list as empty. Test this explicitly in slices 2 and 3.
- **Double counting:** receiver watches are visible to two sessions. The rollup
  must not count one watch twice.
- **Drift:** `types.gen.ts` must be regenerated in the same commit as the Go
  field, or `lint-generated` fails.
- **Load:** the machine is under heavy load; run focused package tests rather
  than whole-module suites.
