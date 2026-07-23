# W8 timestamp units review — Turn/ThreadItem epochs → milliseconds

Reviewer verdict: **FIX_ROUND** (one Important CI-lint-gate failure; trivial `gofmt -w` remedy).
Everything else — the migration's correctness, cross-consumer safety, scoping, and
persistence reasoning — is sound and independently verified.

Range: `57de2dd36..85bf63e84` (branch `w8-timestamps`, worktree base `57de2dd36`).
Method: verified every claim against code + re-ran all gates from the worktree root.

## Gate summary
`go build ./...` ✓ · `go test` (appprojector, apptranscript, appwire, serf-hub/…,
serf-tui/…, server/…, serf/…) ✓ exit 0 · `tsc --noEmit` ✓ · `vitest run` ✓ **243 files
/ 3478** (exactly as reported) · `biome ci` ✓ (667 files, no fixes) · `npm run build` ✓
(dist/PLACEHOLDER restored) · `go generate ./appwire/...` ✓ zero diff.
**FAIL: golangci-lint / `make lint`** — two `gofmt` violations (see Important #1).

## Probe outcomes
- **P1 producer completeness — PASS.** Zero `.Unix()` calls remain in producer non-test
  code (grep of internal/appprojector + internal/apptranscript is empty). The nine
  migrated sites are all the frontend-feeding absolute-epoch producers that build from a
  `time.Time`. RED evidence is credible: the committed tests flip `.Unix()`→`.UnixMilli()`
  value assertions (appwire_projection_test.go, apptranscript_test.go, turn_index_test.go),
  which necessarily failed against seconds producers (1700000000 ≠ 1700000000000) and are
  green now. Non-migrated `StartedAt` writes are correctly excluded: `server/appwire_turns.go`
  (clone/merge of `*int64`, unit-agnostic pass-through), `web_api_tree.go:417`
  (`rendezvous.Entry.StartedAt` = the seconds tree contract, consumed by `AgeString`),
  `web_api.go:146` (`hubapi.HealthResponse` hub process start), and the codex mapping (see P2).
- **P2 cross-consumer fallout — PASS.** `web_format.go:74` `activeTurnRunningFor` is fixed
  (`time.Unix`→`time.UnixMilli`) and locked by the new `TestActiveTurnRunningForReadsStartedAtAsMillis`
  (asserts "2m"; a seconds read floors to "1s"). No other seconds-assuming consumer remains:
  the TUI `ItemDuration` (cmd/serf-tui/internal/transcript/item.go:112) already assumed ms
  (`*time.Millisecond`), so the migration makes it correct rather than breaking it (it was
  1000×-too-small before) — corroborating evidence for the ms target. `app_subagent_preview.go`
  and `server/appwire_turns.go` are pointer pass-throughs. All frontend consumers
  (`reasoningFormat`, `subagentModule.durationLabel`, reducer `epochMsToISO` at 124/125,215/216)
  operate on ISO strings, so the fix correctly lives entirely on the producer side to match
  the frontend's pre-existing ms expectation.
- **P3 scoping (CreatedAt/UpdatedAt left as seconds) — ENDORSE.** Every element verified:
  producer `local_daemon.go:586-587` sets `CreatedAt/UpdatedAt = entry.StartedAt.Unix()`
  (seconds); consumer `web_api_tree.go:393-394` reads them via `hubcore.UnixTime` =
  `time.Unix(seconds,0)`; the React frontend never reads `createdAt/updatedAt` (grep for any
  read, and for `new Date`/`epochMsToISO`/`Date.parse`/`*1000`/`/1000` on them, both empty).
  Converting to ms would push `time.Unix(ms,0)` to ~year 55000 and break tree age/ordering
  for zero frontend benefit. Scoping is correct.
- **P4 frontend nets — PASS.** Re-ran the `reasoningFormat` mutation myself
  (`/1000`→`/1`): 4 RED failures (`expected 4400 to be 4`, `expected 200 to be 1`,
  `expected 10000 to be 10`), then restored clean. The subagent `durationLabel` net is a
  wire-true ISO diff added this change (+1 test → 3478). Both consumers are ISO-diff based,
  wire-true at ms scale; no seconds-scale fixture needed updating.
- **P5 persistence — PASS.** `agent/schema/turn.go:41` `Timestamp time.Time`, no
  `MarshalJSON` on `schema.Turn` (grep clean) → RFC3339 string on disk, projected to int64
  only on read (now `.UnixMilli()`). The turn-index disk format (`indexedTurn`,
  `turnIndexDisk`) persists no StartedAt/CompletedAt — the stamp is recomputed from the
  entry timestamp on every read. No baked-seconds number exists in any transcript, so old
  data renders correctly under the ms projection and no migration is needed. Jesse's
  no-back-compat ruling genuinely costs nothing.
- **P6 rename honesty — PASS.** `pendingCompletedAtUnix`→`pendingCompletedAtMillis`, and the
  `unix`/`startUnix` locals → `ms`/`startMs`. The only remaining "Unix" tokens are
  `.UnixMilli()` calls, doc comments referring to epoch *semantics* ("the Unix epoch"), and
  `ModTimeUnixNS` (nanosecond file-mtime cache identity — not a wire timestamp, correctly
  untouched). No identifier lies about units.
- **P7 gates — MIXED.** All gates in the assigned list are green (above), but the repo's
  golangci gate (`.golangci.yml` enables `gofmt`+`goimports`; run per-module by
  `make lint`→`lint-golangci`) is **red** on this branch — Important #1.

`hubapi/types.go` comment fix verified: `ActiveTurnStartedAt` doc now reads "Unix
epoch-milliseconds timestamp" (was "unix-seconds"), comment-only.

## Findings

### Important
1. **Two `gofmt` violations fail the repo's golangci / `make lint` gate.** The manual edits
   changed struct-field alignment runs without re-running gofmt:
   - `internal/appprojector/appwire_projection.go:66` — renaming `pendingCompletedAtUnix`
     (22) → `pendingCompletedAtMillis` (24) makes it the widest field in the block, so
     `pendingTurnID` and `pendingDurationMS` must re-align.
   - `appwire/types.go:492` — inserting the new doc comment between `Error` and `StartedAt`
     splits the `Turn` struct's single alignment run in two; the `ID..Error` group and the
     `StartedAt..DurationMS` group both re-align to their own natural widths.

   Confirmed with the real repo config:
   `golangci-lint run ./internal/appprojector/...` → `appwire_projection.go:66:1: File is
   not properly formatted (gofmt)`; `golangci-lint run ./appwire/...` → `types.go:492:1:
   File is not properly formatted (gofmt)`. This turns CI red on merge. `go build`/`go test`
   (the gates the fixer ran) do not catch gofmt, which is why it slipped through.

   Remedy (whitespace-only, no semantic change):
   `gofmt -w internal/appprojector/appwire_projection.go appwire/types.go`

### Minor
2. **Report overstates gate completeness.** `w8-timestamps-report.md` §Gates lists
   `go build`/`go test`/frontend gates as ✓ but does not run golangci/`make lint`, which is
   in fact red (Finding #1). The report should either run that gate or state it was not run.
3. **`cov_session_tree_pass3_fuzz_test.go` mixes ms into the seconds contract.** Changing
   `now := time.Now().Unix()` → `.UnixMilli()` (needed for the turn's `StartedAt`) also
   feeds `CreatedAt: now-100, UpdatedAt: now`, which are the *seconds* contract. Harmless —
   the fuzz drives presentation branches for crash-safety and asserts nothing on
   CreatedAt-derived age — but it now exercises the far-future-createdAt branch instead of a
   recent one (a coverage shift, not a bug). Consider deriving the turn start from a
   separate ms local to keep the fixture's units honest.

### Observation (not a finding)
- `cmd/serf-hub/internal/appsource/codex_mapping.go:29-30` relays the external codex app's
  `startedAt/completedAt` (`*int64`) straight into `appwire.Turn` unchanged. This is not a
  `time.Time.Unix()` producer, so it is correctly outside this migration; its correctness
  depends on what unit the codex app emits (no fixture/test pins it in-repo). This
  migration neither fixed nor broke it — the codex path had the identical
  (source-unit → `epochMsToISO`) mapping before and after. Flagged only so the external
  contract is on record.

## Verdict
**FIX_ROUND.** The units migration itself is correct, complete, and well-tested end to end,
and the CreatedAt/UpdatedAt scoping is endorsed. The sole blocker is the two `gofmt`
violations that fail the repo's lint gate; fix with `gofmt -w` on the two named files, then
this is APPROVED.
