# W8 close (T8) — status report

**Status:** DONE_WITH_CONCERNS (one real runtime finding — the work-time epoch clock — plus
env/fixture-limited live items; nothing broken that blocks the controller merge).

**Worktree:** `webui-w8-periphery`, branch `w8-periphery`. Pre-task HEAD `2400349fb`.
**Commits this task:** `ba37a142b` (micro-items) → this report/artifacts commit.
**Merge to integration is NOT this task** (controller-owned, serial). No push performed.

## Item 0 — micro-items (`ba37a142b`, all gates green per-commit)
- **0c (RED-first):** observer-callback notification with no `output:` section now surfaces its
  `message:` prose (floor parity-m4 §8:239 "body = observer-callback prose"; wire shape
  `agent/session_tools_communicate.go:117`). Fix in `steeringClassify.ts` `parseObserverCallback`
  (`excerpt: output || proseOnly`), RED test in `steeringClassify.test.ts`. Closes pre-sweep **G1**.
- **0a:** removed the orphaned test-only `modelLabel` export from `panes/spawn/harnessModels.ts`
  (the live `modelLabel` is `chrome/statusFormat.ts`) + its 2 tests. Closes pre-sweep **G2**.
- **0b:** reworded the dev-gallery `gallery-sections/modelCatalog.tsx` comment (rich catalog shipped;
  gallery still drives it with a minimal in-memory fake).
- **0d (disposition):** `panes/doc/openDoc.ts`'s eager `import "./index"` KEPT deliberately —
  redundant with AppShell boot registration (`AppShell.tsx:32`) but retained as defensive
  self-registration for any future non-AppShell entry path (T3b review P0). Not removed.

## Item 1 — parity delta-sweep (all 192 units resolved)
- **129 stable** carried from `.superpowers/sdd/w8-presweep.md` (verified at the pre-sweep tip;
  dominated by wholesale architecture-replacement divergences: panes.js iframe/postMessage →
  dockview-native, thread.html standalone document → single React app, doc_serve HTML pages →
  native pane, htmx polls → reactive stores).
- **63 deferred swept now** against the final tree (T4/T7/T3b/T4b + fix round all merged):
  **47 verified-met / 16 consciously-diverged / 0 gap.** Per-row citations + the divergence ledger
  are in `docs/superpowers/plans/wave8-report.md`.
- **Combined roster (192): ~91 met / ~101 diverged / 0 unresolved floor gap.** The three pre-sweep
  low gaps are closed: G1 (Item 0c), G2 (Item 0a), G3 mobile-hide (live Journey 5 mobile).
- **Plan-doc framing correction** (stated, plan doc not edited): the plan's §"Genuinely open" still
  lists MW-B as an open go/no-go and single-pane-composer as pending — both are settled and landed
  (MW-B raw endpoint `770800fe8`, MW-A terminal-error `4e6936fcf`; single-pane composer live-proven).

## Item 2 — LIVE proof (real hub from this worktree, real browser, real model, no mocks)
Isolated fake `$HOME=/tmp/w8-live-home` (own `hub.lock`), `SERF_HUB_WEB=new`, port 19288, repo `.env`
sourced. Cheapest real model: `oai-work/gpt-4o-mini` (see concern re `gpt-5.4-mini`). Screenshots in
`.superpowers/sdd/w8-close-shots/` (11, `git add -f`).

| # | Journey | Result |
|---|---|---|
| 1 | Hub-spawn via rich catalog → real turn | **PASS** — grouping/badges(tools/vision/web-search/reasoning)/cost/context-window live; gpt-4o-mini read notes.md |
| 2 | File card → Open beside → doc pane (md + image) | **PASS** — markdown (DOMPurify, `<strong>`) + image (`/doc/image`, decoded) beside session; MW-B `/doc/file?format=raw`→`text/plain`+`nosniff` |
| 3 | Subagent → Open transcript pane | **NOT LIVE-DRIVEN** — gpt-4o-mini never invoked delegate; no subagent row produced. Code+test verified (`subagentModule.tsx:141`→`openBeside({type:"transcript"})`, T3b P5, jsdom) |
| 4 | Layout persistence across reload | **PASS** — both doc panes survived a full reload (D3 fix; layout NOT discarded) |
| 5 | /thread single-pane + mobile | **PASS** — `[data-single-pane]`, rail+tab-strip+Sessions-toggle hidden (real-browser CSS), StatusRow+location cluster+live composer; mobile 390px no h-overflow (closes G3) |
| 6 | StatusRow clock / reasoning / model-switch | **MIXED** — reasoning `(default)` PASS; model-switch idle PASS + **busy-gated mid-turn PASS**; **work-clock BUG** (below) |
| 7 | PendingChips / queue under active turn | **PASS** — queued under active turn → composer cleared + chip rendered → drained to a real turn on completion |
| 8 | Settings polish | **PASS(partial)** — providers→credentials redirect PASS; marketplace "2 entries" count + per-row Refresh PASS; status dots not-provable (no installed plugins in env; T7-tested); per-row busy window sub-second |
| 9 | PWA brand | **PASS** — manifest `#0e1116`, index.html theme-color `#0e1116`, `crossorigin=use-credentials`, `start_url` token-injection, `application/manifest+json`+`no-store` (authed curl) |
| 10 | Turn-failure diagnostics | **PASS** — red end-cap `data-turn-error`, taxonomy badge "provider 400", **live Retry button** (D4 fix confirmed not-dark) |

Bonus live-verified: auth wall (401 unauth `/`, 200 exempt icon, 401 gated manifest); sticky-default
model across spawns; graceful "Couldn't load models" on a bad cwd (no crash).

## HEADLINE FINDING — work-time clock still shows epoch absurdity (`~495274h`)
Root-caused from the live React fiber: `model.activeTurnStartedAt = "1970-01-21T15:46:14.627Z"`
(`Date.parse` → `1784774627`), which is **exactly `Date.now()/1000`** — the wire's
`SerfThread.ActiveTurnStartedAt` is epoch-**seconds** but the reducer's `epochMsToISO` reads it as
epoch-**ms** → 1970 → `now − ~epoch ≈ 495274h`, and it is not cleared when `status.type==="awaiting"`
so the idle clock keeps ticking. The W8-T4 guard (`statusFormat.ts:61`) only catches `startedMs <= 0`,
so a positive seconds-scaled anchor slips through. Manifests when a session is hydrated mid/just-post
activity (a fully-settled cold reload showed a sane `30s`). **Not fixed** — `reducer.ts` is a standing
off-limits chokepoint and the true locus may be the Go daemon (`ActiveTurnStartedAt` unit); routed to
the controller/Jesse as a decision (Go-daemon-unit vs reducer-coercion vs a `status==="active"`
frontend guard). Severity MED-HIGH (cosmetic but blatantly wrong; screenshot `j6-worktime-epoch-BUG.png`).

## Item 3 — gates
Frontend (from `cmd/serf-hub/frontend`): `tsc` 0 · `vitest` bare **243 files / 3474 tests** (baseline
3475; net −1 = −2 `modelLabel` +1 observer) · `biome ci` 0 · `build` 0 (`dist/PLACEHOLDER` restored,
tree clean). Go (worktree root): `go build ./...` + `go test ./cmd/serf-hub/...` — see wave report /
gate logs. (Final close-gate re-run captured to `/tmp/w8-final-*.log`.)

## Concerns
1. **Work-time epoch clock** (above) — the one real runtime finding.
2. **`oai-work/gpt-5.4-mini`** (the brief's precedent model) returned provider-400 "invalid image data"
   on a text-only first turn; traced to the test's 1×1 PNG being rejected by serf's vision
   side-channel (a fixture artifact, not a serf or model-availability bug) — the same 1×1 PNG later
   poisoned a session's context on gpt-4o-mini too. Switched to `oai-work/gpt-4o-mini` for the clean
   journeys. Both failures doubled as live Journey-10 evidence.
3. **Journey 3 not live-driven** — gpt-4o-mini would not invoke the delegate tool, so no real
   subagent existed to open a transcript beside. Feature is code+test verified.
4. **Status dots / per-row busy window** not visually caught (no installed plugins in the fresh env;
   sub-second busy window) — both T7-tested + mutation-verified.

## Not this task
Controller: wave→integration merge + focused re-review of the three controller wiring commits
(`9b14e3aaf`, `6c2e51b1e`, `2e2878e3c`); M10 deletion + flag-flip; M9 full e2e + ratifications
(single-pane live composer, ask-transcript re-architecture, /thread location-cluster render); final
whole-branch review.
