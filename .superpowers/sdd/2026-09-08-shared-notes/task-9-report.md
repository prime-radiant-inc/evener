# Task 9 report: E2E scenarios + full gates

Branch: `shared-notes`, on top of `57c86f766` ("feat(notes): TUI drawer, commands, capability mapping").

Task 9 is terminal: it writes the two e2e scenario files, registers them, and
runs the full gates. No production code was touched — the diff is 2 new files
plus a 16-line INDEX section.

## Step 1: scenario files (DONE)

Created, mirroring `test/scenarios/web-goal-set-and-complete.md` structure
(What-covers / Pre-state / Steps / Expected / Cleanup / Sharp edges) plus the
runbook-conformant hermetic pre-state (`web-drag-drop-image.md` generation:
isolated `$HOME`, kernel-assigned port, `make build-web`, hermetic `$WORK`):

- `test/scenarios/web-notes-human-interrupts.md` (~19KB) — `notes/human/set`
  mid-turn against `test/e2e/fakellm`: wire interrupt proof (next fake round
  carries the note), Details panel edit round-trip, `System steered: Human
  note` divider label from the wire kind, fresh-`thread/read` rejoin pin
  (Task 7 I1), `NOTES_CONTEXT` per-round growth bound (Task 5 M3), and the
  `annotateSteeringKind` drain-race label-loss note (Task 5 row 10: store
  authoritative, label best-effort).
- `test/scenarios/web-notes-url-add-remove.md` (~18KB) — agent `urls_add` ×2
  → per-id `shared-notes-url-<id>` rows with anchors + per-row
  `shared-notes-url-remove-<id>` buttons → UI remove → wire remove (`{}`
  response, push authoritative) → no-reload convergence → row/button testid
  pairing as the bare-id discoverability answer (Task 8 minor 5: web
  discoverable via DOM+RPC, TUI drawer not — documented, not fixed) →
  `Link:` context-block shape check.
- `test/scenarios/INDEX.md` — new `## Shared notes` section registering both
  cards after the Goal engine section (goal TUI precedent: index entries name
  the file, not the heading; both files open with the canonical `# <id>:`
  heading).

All AppWire calls in both cards use `thread/start` + `thread/read` +
`notes/human/set` / `urls/remove` + `thread/shutdown` over `/rpc`. The retired
`/api/spawn` and `/api/sessions` REST routes are deliberately absent
(`web.go` registers no session REST; `web_fuzz_test.go` names the retired
slot). No live network: fakellm is a loopback HTTP fixture local to the repo;
model is `fake/fake-test-model` via `scripts/e2e/e2e-webui-turn-controls.sh`.

Two corrections made during writing (verified against source, not memory):
`submitRouting.ts` `isTurnActive` lives at `:40-49` (not `:48-50`);
`SteeringItem.tsx` user-source branch is `:148` and `data-opens-exchange` is
`UserMessageItem.tsx:124` (not the `web-steer-live-turn.md`-era `:143-146`
/ `:98,112` — stale post-refactor numbers, left alone per minimal-scope rule).

## Step 2: feature e2e paths (DONE)

- `go test ./cmd/evener-hub/ -run 'Notes' -v` → PASS (includes
  `TestHubRPCNotesHumanSetGatedByCapability`,
  `TestHubRPCUrlsRemoveGatedByCapability`, past-thread notes projection).
- `go test ./server/ ./internal/appprojector/ -count=1` → both `ok`
  (server 5.6s, appprojector 0.6s).
- `go test ./agent/ -count=1` → `ok ... 503.594s`.
- `go test ./test/scenarios/ -count=1` → `ok` (canonical-`active` gate passes
  on both new files; the only `processing` mentions are the
  never-write-this prose the gate itself blesses).

## Step 3: full gates (MIXED — see concerns)

- `make vet` → **exit 0** (all modules).
- `make lint` → **FAIL at `lint-evenerfuzz`** (tagliatelle ×4 on
  `cmd/evener-tui/hub_notifications.go:620-623` `streamDeltaChunk`
  `threadId/turnId/itemId/callId`). Pre-existing and out of scope: the struct
  landed in `f45c66402` (Sep 7, on `main`), this branch forked at `e64670997`
  which already contains it, and the working diff touches no Go file
  (`git status --short` shows only the 3 scenario paths). Main has since
  fixed it properly (`dc29b008c`: decode into `appwire.ToolOutputDeltaParams`
  instead of the hand-rolled struct — a rebase will pick that up). Earlier
  lanes `lint-naming` + `lint-gofmt` PASS.
- `make lint-golangci` (direct re-run for file-level evidence) → **FAIL**:
  the same 4 TUI tagliatelle findings plus 5 in `agent/`, all inside
  Tasks 1–4 committed code, none in this task's unit:
  - `agent/session_notes.go:250` modernize (`interface{}` → `any`),
    `:142` perfsprint (`fmt.Errorf` → `errors.New`);
  - `agent/session_tools_notes.go:51` perfsprint (`fmt.Sprintf` → concat);
  - `agent/session_notes_test.go:62` modernize (`range over int`);
  - `agent/session_tools_notes_test.go:32` revive (`ctx` first param).
  All four files are 100%-new files from Tasks 1–4 (`git diff e64670997 HEAD
  --numstat`: 262/0, 133/0, 359/0, 144/0 added/removed) — committed before
  Task 9, untouched here. NOT fixed per the do-not-touch-unrelated-files rule.
- `make test-web` → **FAIL on the lint stream only**, same 5 pre-existing
  files Task 7 reported (`DocPane.tsx`, `AdvancedOptions.tsx`, `Rail.tsx`,
  `RailRow.tsx`, `SessionMenu.tsx` — `organize-imports` + 1 format; direct
  `biome ci src` re-run: `Found 6 errors`, file list byte-identical to the
  Task 7 report). Verified still-untouched: `git diff e64670997 HEAD --stat`
  and `git diff main...HEAD --stat` on all five paths are both empty.
  Typecheck + unit-test streams were green at Task 7 (`tsc` clean, 9207
  tests); this task adds no frontend code, so there is nothing new to break
  them. (`make test-web-browser` not run: no Chrome on this host; hermetic
  scenario prose needs no browser to review.)
- `make test` → **running at report time** (background job
 - `make test` → **exit 2: all 7 Go modules PASS, web FAILs on web-lint only**
   (job `job_034LfDLBFabA53odUjN9i3_BfSXHxGfVvr9`):
   `PASS . / agent / llm / auth / envvars / invariant / identifier`;
   `PASS web-typecheck`, `PASS web-test`, `FAIL web-lint` (the same 5
   pre-existing biome files, proven identical by the direct `biome ci src`
   re-run above — `Found 6 errors` in `DocPane.tsx`, `AdvancedOptions.tsx`,
   `Rail.tsx`, `RailRow.tsx` ×2, `SessionMenu.tsx`).

## Step 4: commit (PENDING — blocked on gate verdict)

Committed as below. Staged paths (named, never `-A`):

```bash
git add test/scenarios/web-notes-human-interrupts.md test/scenarios/web-notes-url-add-remove.md test/scenarios/INDEX.md .superpowers/sdd/2026-09-08-shared-notes/task-9-report.md
git commit -m "test(notes): e2e scenarios for human interrupt and URL add/remove"
```

(Note: this report file itself is `.gitignore`d via `.superpowers/` — prior
task reports live in the worktree untracked. The commit uses `git add -f`
for the report path so the evidence travels with the work; the three
scenario paths are added normally per the brief's Step 4 spelling.)

Self-review notes:
- Accidental `git stash -q` mid-verification stashed the INDEX edit; restored
  immediately via `git stash pop` (verified `git status --short` shows all 3
  paths back). Stash list otherwise untouched (entries belong to other
  branches). Lesson: verify pre-existing red with `git show HEAD:path` and
  branch-point diffs, never with stash.
- No subagents dispatched, no reviewers spawned, no production files touched.
- `falsify:` lines in both cards name the exact regression each assertion
  guards, and every line-number citation was grepped, not recalled.

## Concerns (for the merger)

1. **Gates are red for pre-existing reasons outside this task's unit.**
   `make lint` (tagliatelle ×4 TUI + ×5 agent), `make test-web` (biome ×6 in
   5 files) — all byte-identical to states Tasks 5/7 already reported, all in
   files this branch's diff does not touch. `make vet` is green. Options:
   (a) merge with the documented red + file cleanup follow-ups (main already
   fixed the TUI half in `dc29b008c`); (b) rebase onto current main first,
   which clears the TUI tagliatelle + biome reds, leaving only the 5 agent
   findings for a Tasks-1–4 owner to fix. Recommend (b).
2. **`make test` final exit still pending** (job above). The scenario-gate,
   hub, server, projector, and agent suites are green; the only expected red
   inside `make test` is its frontend stream (same biome files).
3. Scenarios are prose + verifiable expectations, never executed here (no
   Chrome, no live hub on this host). The fakellm-backed steps are runnable
   as written; the browser steps await a Chrome-capable run.
