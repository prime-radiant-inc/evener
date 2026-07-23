# W8 → integration merge — focused re-review

**Verdict: APPROVED** (one Minor, comment-only, non-blocking).

**Reviewer scope (by construction, disjoint from the per-stream gate reviews):** the
controller-authored direct commits, the wave-close commits, the post-review
reconciliation commits on stream branches, every `--no-ff` merge commit, and the
integration-side merge `5fe6804c1`. Each stream's own `BASE..reviewHEAD` range was
already adversarially reviewed at its gate and is explicitly NOT re-litigated here.

Worktree `webui-merge-review` @ `5fe6804c1` (branch `w8-merge-review`).
Integration merge = `5fe6804c1` (p1 `4ee6e2a7a` integration, p2 `25e754b42` = `w8-periphery`).

---

## Gate (run once at the tip, AND-chained)

```
go build ./...                                              exit 0
go test ./cmd/serf-hub/... ./server/... ./internal/appprojector/...
        ./internal/apptranscript/... ./agent/...           exit 0  (46 pkg ok, 0 FAIL)
make lint                                                  exit 0  (7 modules PASS, 43s)
(cmd/serf-hub/frontend) npx tsc --noEmit                   exit 0  (0 errors)
(cmd/serf-hub/frontend) npx vitest run   [BARE]            exit 0  (243 files / 3490 tests)  ← matches expected 243/3490
(cmd/serf-hub/frontend) npm run lint (biome ci)            exit 0  (668 files)
(cmd/serf-hub/frontend) npm run build                      exit 0  (dist/PLACEHOLDER restored; tree clean)
```

All green. Vitest count is exactly the expected 243 files / 3490 tests.

---

## Scope enumeration (derived, then audited)

Audited **25 unreviewed direct commits** (9 code-bearing, 16 docs/report/review-artifact)
and verified **16 merges** (15 wave-internal + the integration merge) as exactly their
auto-merge.

### Merge fidelity — CLEAN
Every merge was checked with both `git show --cc` and `git show --remerge-diff`. **All 16
merges (15 wave-internal incl. the T3b absorb-merge `7f649fb66`, plus integration
`5fe6804c1`) reproduce their auto-merge exactly — zero manual-resolution / evil-merge
content.**

Integration-merge union (`5fe6804c1`) spot-checked: wave-side artifacts
(`docs/superpowers/plans/wave8-report.md`, `.superpowers/sdd/w8-close-report.md`) and
integration-side artifacts (`m9-live-e2e-plan.md`, `final-review-scaffold.md`, the sdd
ledger) all present; both `.superpowers/sdd` (85) and `docs/superpowers/plans` (218)
populated from both sides. Nothing lost either direction — the single wave↔integration
tree difference that is absent from HEAD is `panes/spawn/modelField.module.css`, which the
**wave deleted** (T2 `1bdea17a3`, ModelField→thin adapter); base had it, integration did
not touch it, so the 3-way merge correctly propagates the deletion. Not a loss.

### Controller wiring commits
- **`9b14e3aaf` (index.html theme re-sync + three-way drift lock) — PASS.** Corrects the
  drifted `theme-color` meta `#0a0a0e`→`#0e1116`. Verified the new value is the real brand
  background: tokens.css dark `--surface-0` = `#0E1116`, manifest `background_color` &
  `theme_color` = `#0e1116`, index meta = `#0e1116` — all three agree. The test reads **all
  three color surfaces** (`darkSurface0()`←tokens.css; manifest bg+theme; a new test for the
  index.html meta), each locked to `darkSurface0()`. Comment is honest.
- **`6c2e51b1e` (thread location facts on the model + epoch-anchor source guard) — PASS
  (induces the one Minor below).** `model.ts` adds exactly `cwd:string`, `gitBranch?:string`,
  `projectPath?:string`; reducer hydrates from `thread.cwd` / `thread.gitInfo?.branch` /
  `thread.projectPath` — field names & optionality verified against `types.gen.ts`
  (`Thread.cwd` required, `gitInfo?.branch?`, `projectPath?`), so the wiring is semantically
  correct, not merely type-clean. **Snapshot update is exactly the 3 new fields × 4 fixtures
  = 12 lines, no other key touched**; the `cwd` value is a static literal in the committed
  `fixtures/*.jsonl` (deterministic, machine-independent; fixtures untouched by the commit).
  The `+1` lines across 13 test files are mandatory `cwd:"/tmp/project"` additions to model
  builders (required field; StatusRow `+4` = 4 inline builders). Two new reducer tests
  genuinely lock the wiring and the guard. Epoch guard `undefined||NaN||<=0→undefined`
  verified safe across **all** `epochMsToISO` callers: it only suppresses the wire's `<=0`
  "unset" sentinel (never a legit positive ms), the `epochMsToISO(now)` callers are never
  affected, and it is a net correctness win (turn/item `startedAt`/`completedAt` also stop
  emitting bogus `1970-01-01` strings — consistent with the empty non-location snapshot diff).
- **`2e2878e3c` (retarget T4 epoch net to the guarded reducer + defense-in-depth render) —
  PASS.** Test-only. Retargets a pre-existing assertion from "reducer surfaces the epoch
  sentinel" to `toBeUndefined()` (guarded reducer), and adds a render test injecting the
  epoch anchor directly — verified non-vacuous: `totalWorkMillis` (statusFormat.ts:58-62)
  really returns banked `workMillis` for `startedMs<=0`.

### T8 close commits
- **`ba37a142b` (micro-items) — PASS.** Exactly the three stated items, nothing smuggled:
  (1) observer-callback prose surfacing (`excerpt: output || proseOnly`) with a genuine
  RED-first test; (2) `modelLabel` removal from `harnessModels.ts` + its 2 tests — verified a
  true orphan (no importers; the live `modelLabel` is `statusFormat.ts`, used by ModelSwitch);
  (3) dev-gallery comment reword (comment-only, honest).
- **`73326f7a3` (T8 sweep, live proof, wave8-report) — PASS (docs only).** 13 files, all
  `.md`/`.png` (`w8-close-report.md`, `w8-close-shots/*.png`, `wave8-report.md`). No code —
  the micro-items are correctly isolated in `ba37a142b`.

### Post-review reconciliation commits (stream second-parents, after each reviewHEAD)
All comment/format/doc-only, each matching its message, no behavior change:
- **`f251bedfe`** (fixround Minor 1+2) — removes the invented "guard is newer than T6's read"
  rationale from the `popOutPane` comment; updates the citation. Comment-only.
- **`9475a587b`** (T4b Minor 1) — line-number→symbol-form citation in the LocationCluster
  comment. Comment-only.
- **`11193c0ed`** (T3b F1) — biome import reorder in `subagentModule.test.tsx`. Test-import
  only (closes the biome-ci gate that was RED at the T3b reviewHEAD).
- **`f245b290a`** (timestamps Important 1) — pure `gofmt` column realignment in
  `appwire/types.go` + `appwire_projection.go`; field names/types/tags identical, no semantic
  change, wire shape unchanged.
- **`3b30738c4`** (cost Minor 1) — adds an honest current-model-repricing caveat to the
  `SerfThread.Cost` doc comment. Comment-only.

### Docs-only artifact commits (verified single-file `.md`, no code)
`7885ad18d` (workclock review) and the 12 stream `*-review.md` commits
(`48c30fb97 66ffbb974 41786d7db eb48b8fa4 1ced437a8 a645f9706 6e6dfa236 58a095310 2962d3eda
18b43ec9a 5b666335d 7ef6c2045`) and the 2 report-branch commits behind the report-merges
(`1b9fd617b` count-variance, `ccca2bf3f` presweep). All docs.

Note: the 3 work-clock **code** commits (`c448bd808 aadeef645 57de2dd36`) and the popout
shell commit `9f58a2588` are NOT in this scope — they were substantively covered by
`w8-workclock-review.md` (range `73326f7a3..57de2dd36`) and `w8-completion-review.md`
(P1 popout shell, BASE `57de2dd36`) respectively.

---

## Findings

### Minor 1 — stale reducer cross-reference comments left by the wiring commit
`6c2e51b1e` strengthened `reducer.ts` `epochMsToISO` to guard `undefined || NaN || <= 0`
(previously it guarded only `undefined`), but did not update three sibling comments — written
by the reviewed T4 stream (`5dfa17b59`) — that describe the *old* contract:

- `src/panes/session/chrome/statusFormat.ts:54` — "…converted by the reducer's `epochMsToISO`
  (reducer.ts:78-80, **which guards only `undefined`, never 0**) into
  `"1970-01-01T00:00:00.000Z"`".
- `src/panes/session/chrome/statusFormat.test.ts:70-77` — same "guards only `undefined`,
  never 0 … turns it into `1970-…`" claim.
- `src/panes/session/chrome/statusFormat.test.ts:80` — inline
  `// exactly what epochMsToISO(0) produces` on a `new Date(0).toISOString()` literal.

After `6c2e51b1e`, `epochMsToISO(0)` returns `undefined`, not the `1970` string, so all three
statements are now factually false and the `reducer.ts:78-80` line-span is slightly off (the
function is 77-82, guard on line 81). **No behavioral or gate impact:** `statusFormat`'s
`totalWorkMillis` guard is genuine, independent, and tested (both epoch and pre-epoch/
unparseable cases pass), so the defense-in-depth described is real — only the *explanatory
prose about how such an anchor reaches the render* is stale. This is the same class the wave
already treated as Minor and fixed with dedicated commits (`f251bedfe`, `9475a587b`); the
author correctly updated the reducer's own new comment ("statusFormat.ts rejects bad anchors
too, as defense-in-depth") but not these two back-references. Suggested follow-up: reword to
"the reducer's `epochMsToISO` now maps the zero sentinel to absent; statusFormat guards it
independently" and drop the `1970` claim.

---

## Conclusion

The merge is mechanically clean end-to-end (every merge = its auto-merge; union complete and
loss-free), every unreviewed code commit does only what its message states with tests that
genuinely lock the wired behavior, the T8 close commits are correctly scoped, and the full
gate is green (243/3490). The sole finding is a non-blocking comment-staleness nit induced by
the epoch-guard wiring commit. **APPROVED.**
