# Wave 8 — T3 (transcript parity) adversarial review

**Verdict:** APPROVED (with 1 controller handoff + 3 Minor findings; none block T3)
**Reviewer scope:** spec compliance + code quality; claims verified against Go/wire source; all gates re-run.
**Range reviewed:** `e3b9c188c..578c9dd2b` (branch `w8-transcript`, worktree `webui-w8-transcript`).
**Gates (re-run by reviewer, from `cmd/serf-hub/frontend`, AND-chained):** `npx tsc --noEmit` exit 0 → `npx vitest run` **228 files / 3293 tests, 0 failed**, exit 0 → `npm run lint` (Biome ci) exit 0 → `npm run build` clean, `dist/PLACEHOLDER` restored, tree clean. No `.go`, `reducer.ts`, or `tokens.css` changes.

---

## Probe outcomes (one line each)

- **P1 wire-shape fidelity — PASS (1 minor fixture drift, non-behavioral).** `TurnError`{message req; source/title/hint/cause? opt}, `DiagnosticCause`{kind,provider,model,status?} match `appwire/types.go` and `types.gen.ts` byte-for-byte; `SettledToolStatus(isError)`→"failed" verified live (`appwire_projection.go:438`) + reload (`apptranscript.go:381`); `argumentsJson`/`error`/`status`/`source` tags + `argumentsJSON` casing correct; `<job-notification>` attr set/order (job_id,event,job_type,status,reason,output_bytes,[budget/limit/resumable],[exit_code],[transcript_ref]) + `\nexcerpt:\n` body + communicate `{message,data.{status,concerns},artifacts}` all verified against `job_notify.go`/`session_tools_task.go`/`session_tools_communicate.go`. Notification + turn-failure fixtures are wire-exact. The ONE drift is a task_list fixture (Minor #2).
- **P2 manifest — PASS.** All 25 code/test/css files within `panes/session/transcript/**`; `reducer.ts` + `tokens.css` + all `.go` untouched; classifier is presentation-only (the only `reducer` mentions are comments; `turnFailure`/`steeringClassify`/`taskCard` import no store/reducer — `TurnFailureEndCap` imports `threadsStore.send` only, the sanctioned recovery action).
- **P3 suppress hook — PASS (mutation-verified both ways).** `suppress?` is optional; only `taskCard` registers it, so no existing descriptor changes; checked AFTER hooks (stable hook order). Forcing `suppress:()=>true` fails 7 tests (over-suppress hiding real work IS caught); `()=>false` fails 2 (view/malformed rendering IS caught). `Progress:` regex returns undefined on garbage (no crash); `Meter` guards `max>0` (no div-by-zero); `parseArgs` try/catch → `{}` on malformed JSON.
- **P4 error rendering — PASS.** `toolFailed = (error present) || status==="failed"` — error-presence primary, status corroboration, matching the fold-in note; force-open folded into the edge-triggered `autoExpand` effect (manual collapse still wins). `data-attention="error"` is **write-only across the entire frontend** (no consumer → cannot leak an errored ask into needs_you). Denied-ask surfacing is read-only and consistent with `isAckedAskUserItem` (composer/askDock, outside manifest, already excludes `error!==undefined`).
- **P5 NotificationCard — PASS.** Per-block non-greedy split verified against 2-block fixture; excerpt entity-decoded then rendered as escaped React text (XSS test: `<script>` becomes visible text, no element) — matters under CSP `script-src 'unsafe-inline'` (`httpsec.go:35`); tone→`Chip` danger/attention (allowlisted), module CSS neutral-only; raw disclosure verbatim; communicate message routed through the DOMPurify-sanitizing `Markdown` widget (8k clamp).
- **P6 turn-failure end-cap — PASS.** Taxonomy maps to REAL `TurnError` values: `provider <status>` (cause.kind==="provider", only kind that exists per `DiagnosticCause` doc), `connection` (source==="hub" — a real `diagnostic.SourceHub` — or reconnect substrings), else `source` (serf/ui/mcp/provider — all real `diagnostic.Source` consts), else defensive `error`. Recovery via `threadsStore.send(ref, text)` (real, tsc-checked); withheld when `sessionRef` absent OR no `userMessage` item (`canRetry`, tested both ways — no dead button). **Session.tsx:182 one-liner is exactly correct** (`ref` is in scope, used identically at :146/:187) — see Handoff.
- **P7 openDoc — NO.** This diff imports **no** `openDoc`/`openDocBeside`/`openBeside`/`paneActions`/`docContent` anywhere (verified globally in `transcript/**`). No file-link/doc-pane producers are introduced here. (Feeds the cross-stream layout-restore decision: T3 is not a doc-pane producer this wave.)
- **P8 gates — PASS.** See header. 228/3293 matches the report's current-total claim exactly.

---

## Additional verification

- **Mutation nets real (not vacuous, not mock-testing):** 4 targeted mutations, all caught — `toolFailed→false` (7 fail), `suppress→true`/`→false` (7 / 2 fail), `connection→false` (2 fail), break loop-pattern (2 fail). Tree restored clean after each.
- **Steering port is faithful:** `steeringClassify.ts` patterns (current-task / full-list `^Task list:` / tasks-done `completed all tasks` / task-nudge / loop / read-only / transcript) + `notificationTone`/`titleForJobNotification` match `renderer-format.js:414-494` exactly; the touched-row gate `["done","cancelled","in_progress"]` matches `renderer.js:5010` (so omitting legacy's `open→reopened` mapping is faithful, not a regression). Content-pattern-based → **MW-C correctly unnecessary**. Conscious divergences (no description on update rows, no auto-advance row, no full-list fold, no communicate facts `<dl>`) are documented for T8's sweep.
- **3 stale-comment corrections accurate:** `shellTool.tsx` (nonzero exit = clean result / status "completed"; tool-error → generic path) and `bodies.tsx` ×2 (error surfaced generically by `ToolCallItem`) are WHAT/WHY descriptions of current behavior, verified against `SettledToolStatus`; no history narration.
- **File arithmetic +5 correct** (223→228 test files, 5 new `new file mode` test files confirmed).
- **TDD RED not independently verifiable** from the squashed per-cluster commits, but the mutation-verified nets satisfy the substantive concern (tests are coupled to implementation).

---

## Findings

### Handoff (controller action required — NOT a T3 defect)
**H1. Turn-failure recovery button is dark in production until `Session.tsx:182` is wired.** `TurnBlock` is rendered as `<TurnBlock turn={turnAt(index)} />` with no `sessionRef`, so `canRetry` is false and the Retry/Reconnect button never renders (the badge+message+hint DO render). T3 correctly cannot edit this controller-owned chokepoint and built the feature behind an optional prop. The report's proposed one-liner — `renderRow={(index) => <TurnBlock turn={turnAt(index)} sessionRef={ref} />}` — is **verified correct** (`ref` is the exact in-scope var, matching `sessionRef={ref}` at :146/:187). The controller MUST apply it (or T1 must fold it in) for the recovery action to be live; otherwise a shipped, tested feature is silently non-functional.

### Minor
**M1. Report arithmetic overstates the test delta.** Report claims "baseline 223/3217 → +5 files, +76 tests". Actual: T3 added **+67 test cases** (base→cur: `ToolCallItem` 17→28, `SteeringItem` 16→21, +51 across the 5 new files; no `.each`/dynamic tests are affected by T3's changes). Since the suite is 3293, the true base is **3226**, not 3217 — the "+76 / 3217" figures are off by 9 (baseline likely measured one commit early). `+5 files` is correct; gates genuinely pass. Report-only nit, no code impact.

**M2. One task_list test fixture is not wire-reproducible.** `taskCard.test.tsx` "note-only update" uses `{action:"update", updates:[{id:1, notes:"added a caveat"}]}` (no `status`) with output `"Updated 1."`. The Go tool rejects a status-less update (`store.Update` line 467: status must be open/in_progress/done/cancelled), and `formatTaskUpdates` only emits the bare `"Updated N."` (no arrow) for a status-less update — i.e. the wire cannot produce this input/output pair. The real equivalent is a reopen: `{id:1, status:"open", notes:...}` → `"Updated 1→open."`. **Non-behavioral** (both hit the identical no-flaggable-touch → empty-card path; all assertions hold either way), but per the binding "fixtures must match REAL Go serialization" it should be corrected. (Lesser: the append fixture omits the schema-required `prompt`; immaterial since the card reads `description`.)

**M3 (observation).** A no-output Observer-callback (`Observer callback:\nmessage: X` with no `\noutput:`) drops the `message:` prose from display (kept only in the raw disclosure), because the parser extracts the message from the `output:` JSON envelope. Essentially unreachable (the callback's `structuredText` is normally non-empty), so low value — noting for completeness, no action needed.

---

## Conclusion
The four clusters are wire-true, design-system-clean, mutation-net-covered, and pass all gates. Wire shapes were verified against the Go/AppWire source (not the fixtures alone), and the one fixture drift found is non-behavioral. The single functional gap (recovery button) is a correctly-handled controller-owned chokepoint dependency, accurately flagged with a verified one-liner. **APPROVED** — H1 is a controller handoff for integration, and M1/M2 are trivial nits that may be folded into T8's sweep rather than forcing a T3 fix round.
