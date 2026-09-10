# Tufte WebUI: draft PR and continuation handoff

Snapshot: 2026-09-10. This is a dated operational handoff. The enduring design rules live in [the design system](../../web-ui/design-system.md); [decisions](../../web-ui/decisions.md) preserve their history.

## Start here

- **Draft PR:** <https://github.com/prime-radiant-inc/evener/pull/1124>, `tufte-webui` into `main`. Keep it draft and unmerged until the remaining acceptance below is complete.
- **Verified implementation:** `4dcd98dc9ed0dcdb700637809b38e150c5b664f3`. Its whole tree matched the independently reviewed writer commit `3189a3c05ee537c9233b5405188947fcbf645fed`. Subsequent documentation commits must be distinguished from this tested implementation commit.
- **Final integrated automated gates:** all twelve runner steps exited zero; every command/tee pipeline was `[0, 0]`. The complete shell output and independent power audit were inspected. Actual test windows ran from `2026-09-10T19:28:57.993646+00:00` through `2026-09-10T19:38:57.317056+00:00`.
- **Hands-on acceptance:** phone Send-focus (P1) and workspace Activity coverage (W1) endorsed. Tools/navigation (P2) still requires the same reviewer's mixed-origin native retest. Its prior verdict remains **REJECT** until then.
- **Preview:** <http://m5:5197/>, detached PID `66313`, deliberately left running. It serves the older `e96fcc8dfe682b82425c34f6e1c9d5a1e9e39519` version of three changed modules. Do not test the new repair against it yet.
- **Latest instruction:** Jesse said, “open the draft. then do the docs next. and the punch list of what's remaining. ALL the context someone would need to pick up for you”. This handoff and the evergreen docs are the immediate delivery. The preview restart and native retest remain explicit follow-up work.

### Authority and limits

Jesse approved the editorial instrument direction, autonomous implementation, actual-browser reviewers, feature-branch push and an unmerged PR. He later authorized opening a **draft before unfinished acceptance** to meet a PR deadline, then prioritized docs and this handoff. That changed the delivery order, not the meaning of acceptance.

Jesse separately authorized restarting **only the isolated preview**, at the same origin/port/command/config/cwd, **after final gates pass**. The automated prerequisite now passes; the restart has not happened. The user's live hub must remain untouched. No main merge, force push, assertion weakening, unrelated redesign, or replacement of the current tools reviewer is authorized. Completed source reviews and unaffected panel endorsements stand.

## Remaining punch list

Work in this order when resuming product acceptance:

1. **Re-orient without restarting work.** Verify branch, local/remote heads, dirty state, PR draft/base/head, and the preview's actual process identity. Read this handoff, the current three canonical docs, and the local evidence ledger. Do not rerun finished reviews or the two failed launchers.
2. **Make the preview current safely.** Preserve old PID/log/route/source evidence. Verify the exact stale process identity immediately before stopping it; restart only that process at the same identity except PID. Prove normal module URLs contain the current embedded source for all five modules listed below. Check all fixture routes and denied backend/filesystem paths. A fresh page and a matching git HEAD alone are insufficient.
3. **Resume the same tools reviewer.** Supply the clean tested implementation identity, documentation-only delta if applicable, new preview PID, final gates/power evidence and five-module source proof. Give it exclusive browser access. Run every affected scenario below through real controls; no source-only substitute. Await its callback without polling.
4. **Resolve any actual retest finding.** Preserve the report and raw evidence before investigation. Use the same scoped writer where possible, a real failing regression, the smallest repair, independent scoped review, safe integration, covering final gates and the same reviewer's exact native retest. Stop and reslice after two ineffective cycles. Do not repeat a full status/geometry matrix without a concrete affected requirement.
5. **Audit native acceptance.** Read the complete report, source/DOM/console evidence and relevant captures. Require an explicit ENDORSE, every required task completed, no blocked required task, and browser release. Record P1/W1/P2 separately. Do not erase the historical scroll-guard failure.
6. **Reconcile delivery state.** Update this dated snapshot and the PR checklist with actual outcomes. Check final documentation links and diff, prove any post-gate delta is docs-only or run the affected final gates, inspect CI, verify pushed head/base/draft state, and leave the PR unmerged. Draft removal requires completed acceptance; it is not implied by passing CI.
7. **Retire only safe leftovers.** Preserve named evidence first. Use native worktree disposal only for clean, integrated lanes with verified ancestry; no force or dirty discard. Preserve the required reviewer and running preview. Never delete pre-existing inputs or historical failure evidence.

## Design intent and scope

The product model is conversation for understanding, nearby evidence for verification, and explicit controls for intervention. The change applies book-like transcript typography, marginal metadata on wide panes, dense readable tables, restrained surfaces, and semantic color across the shell, transcript, forms, settings, composer and shared widgets in light and dark themes.

The UX choices are substantive: put authored intent beside the exact action and native evidence; distinguish a collaborator's current lifecycle/attention from its historical launch receipt and authored report; keep failures and required action discoverable; separate disclosure from independent transcript Open; preserve the precise inspection origin across nested and responsive navigation; retain composer focus after ordinary Send; show incomplete Activity coverage without hiding known work.

Existing folding, keyboard behavior, preferences, workspace persistence and semantic hue meanings were retained contracts, not inventions of this change. The styling supersedes the older decorative Beautiful UI direction while preserving attribution/history. Quiet chrome must still expose usable controls.

Jesse asked whether the design theory is evergreen, whether colors were considered, and whether line spacing is well tuned. The canonical docs explain the theory and tradeoffs. Warm light/dark anchors include `#FAF9F6` and `#191918`; semantic ink/washes and retained ANSI roles remain deliberate. Prose uses 18px at 1.6 leading (28.8px), 12px paragraph spacing; compact UI/title leading uses 1.4/1.25, with a 4/8/16/24 spacing rhythm. The token contract previously passed 1,585 tests on unchanged token bytes. There was **no comparative leading A/B study, perceptual display/color-vision study, or measured usability uplift**. These are honest limits, not new mandatory design work.

Read [the spec](../specs/2026-09-09-tufte-webui-design.md), [the implementation plan](../plans/2026-09-09-tufte-webui.md), and [the full-app fixture guide](../../web-ui/editorial-preview.md). Source and tests are authoritative over stale project prose.

## Implementation and completed review

Key implementation checkpoints:

| Area | Reviewed source / integration |
|---|---|
| Shared editorial foundations | `dd3efd8bb`, palette guard `a1adbcbf3` |
| Inline tools and collaborator provenance | `49eef34df`, corrected `9fe75393c`, integrated `6a9ccfa1a` |
| Shell/application surfaces | `a48f26b44`, rail correction `968e3d8bf`, integrated `e100df41e` |
| Phone Send target | `3aba85b25`, integrated `dd9382be2` |
| Child focus / retained nested owner | `ce9d1348f`, `f44ba07bd`, `d6b614253` |
| Final actual-AppShell fixture | `65d08179c`, integrated `23019bfb9` |
| Ordinary Send focus | `c3efeaffa`, integrated `5e5e3d9bf` |
| Incomplete Activity coverage | `0b0facd9b`, integrated `25d66b5c7` |
| Responsive immediate-parent Back and cycle safety | `4fb739d5e`, `3e4bb511b`, integrated `e96fcc8df` |
| Exact mixed-origin repair | `3189a3c05`, integrated `4dcd98dc9` |

The last repair changes seven frontend files: `src/panes/session/transcript/openTranscript.tsx` and its test; `src/shell/workspace.ts` and its test; `src/shell/mobile/StackHost.tsx` and its test; `src/shell/AppShell.test.tsx`. All paths are under `cmd/evener-hub/frontend/`.

It captures the live Open origin before canonicalization/placement. A private map keyed by `OpenPaneRecord` tracks actual origin records, validates parent refs and lifetimes, and prunes removed/replaced/restored endpoints. Phone Back prefers local history, then the recorded origin, then the retained immediate-parent fallback. Cycle checking walks those same choices. It does not delete siblings, always prefer SESSION panes, or introduce a generic persisted navigation system.

The immutable three integration proof cases, original AppShell proof, eighteen controls, 234 focused tests, typecheck and seven-file Biome gate passed. Independent source reviewer `dlg_034MWMzSVv1cz5ocSnVPDm` returned APPROVED / ADDRESSED / no findings. That review is complete and must not be repeated without a new source delta.

### Outstanding native failure, precisely

The old preview reproduced a real mixed-origin bug. Starting with retained parent SESSION, older child TRANSCRIPT and grandchild TRANSCRIPT, explicitly route to the child SESSION (with composer). Open the grandchild from that SESSION, then open the next descendant on desktop. After resizing to phone, Back returned to the grandchild TRANSCRIPT and then the **older child TRANSCRIPT**, rather than the originating child SESSION. The composer was absent. Prior generated order was `S5,T6,T7,S8,T9`; generated IDs are observations, not constants to hardcode.

The integrated repair is source-approved and automated-green. Its native fix remains unverified. The inverse control must return a TRANSCRIPT origin even when a same-ref SESSION is retained.

## Same-reviewer native retest contract

**Required reviewer:** `dlg_034MTEwjtEUSAvwYyN6shR`, transcript `local:034MTEwjtEYhmk8qqxZ8xr`, original primary finding at turn 203. The reviewer has read the new brief and reported PREPARED. It is holding, with no new browser actions or report writes. Resume it with `delegate_send` from its controlling session. If a different continuation cannot control that delegate, recover the original controller or ask Jesse; do not silently replace it.

The prior replacement of lost reviewers was explicitly authorized. That historical authorization does not waive the current same-reviewer requirement.

The reviewer owns only new ignored `W/panel-tools-mixed-retest.md`, `W/panel-tools-mixed-retest-evidence/`, its own scratch and automatic captures. Here and below, `W=.superpowers/sdd/2026-09-09-tufte-webui` from the checkout root. Preserve every older report. Freeze the parent source/HEAD for the native review and grant exclusive browser use. Never touch parent profile `8` / tab `A48FB2D1F845E3C18043A3727AA73129` or other users' tabs.

### Required scenarios

1. At **1280×1000, Dark/XL, actual 500px split**, parent → visible independent child Open on desktop → resize **390×844 without reload** → actual mobile Back. It must return to the immediate parent identity/context, not Welcome or Sessions drawer.
2. At **1440×1000, Light/M, actual 580px split**, parent → visible child Open → visible grandchild Open on desktop → resize **390×844** → Back to child → Back to parent. Record the complete literal chain; no bounce, revisit, owner promotion or lost retained context.
3. Exercise both nested origins: top-level SESSION → child → grandchild, and explicitly routed nested SESSION → grandchild → next descendant. Complete phone Back walks and the deeper desktop-to-phone transition. Preserve the original top-level owner and each immediate parent.
4. Repeat same-width phone direct/nested Back and same-width desktop inspection/context controls. Explicitly navigate to another known fixture route and prove new-route precedence over old ancestry. Deliberate navigation is valid only for this control, never as a Back substitute.
5. Reproduce the **exact mixed SESSION-origin sequence** above with actual visible Open controls. Record each generated pane ID/type/ref and composer presence. The older same-ref TRANSCRIPT stays retained but must not receive the SESSION return. Walk Back completely.
6. Exercise the **inverse TRANSCRIPT origin**, retaining its same-ref SESSION. Responsive Back must select the actual TRANSCRIPT initiator without a composer. Also verify local phone-history precedence.
7. Check reachable phone Open controls are at least **44×44px**, document/pane containment, and readable nearby native evidence through real disclosure/scroll. Preserve all retained sibling contexts. The unrelated completed full status/900-character matrix need not be repeated.
8. Capture actual console entries before deliberate reload and before cleanup; placeholder auto-console files are insufficient. Restore Light/M through visible controls, close only the owned tab/profile resources, release browser ownership, and run `git diff --check && git status --short && git rev-parse HEAD` with zero exit and unchanged clean dispatch HEAD.

Use real visible controls and trusted native pointer/keyboard/wheel events. Read-only eval may inspect stores, DOM focus, geometry and fixture calls; it must not mutate stores, invoke handlers, force focus/clicks, or replace Back with reload. Distinguish selected pane from DOM keyboard focus. Invalid cyclic/malformed workspace bags and closed-parent negatives remain source/unit coverage, not invented native fixture modes.

Return a completed/failed/blocked table covering every scenario, actual viewport/theme/font/pane widths, exact route/main-owner/active-pane/DOM-focus chains, raw evidence paths, console findings and limitations. Any required blocked item prevents endorsement. The callback must explicitly say ENDORSE or REJECT, whether the mixed-origin finding is ADDRESSED, and that browser ownership was released. Parent inspection of the report and primary evidence remains required.

The real fixture entry supports parent, child, grandchild, great-grandchild, question and resumed sessions. Theme controls are at `/settings/theme`. Open captions and evidence disclosure details are in [the fixture guide](../../web-ui/editorial-preview.md). It uses real AppShell/stores/widgets with a deterministic FakeClient boundary; no credentials or live provider requests are permitted.

## Preview identity and safe refresh

Current recorded identity, to be rechecked live before any signal:

```text
PID: 66313
Start: Thu Sep 10 08:15:52 2026
Origin: http://m5:5197/
Listener: TCP *:5197 (LISTEN)
Cwd: <checkout>/cmd/evener-hub/frontend
Command: node <checkout>/cmd/evener-hub/frontend/node_modules/.bin/vite --config scripts/editorial-preview.vite.config.mjs --host 0.0.0.0 --port 5197 --strictPort --clearScreen false
```

The config disables HMR updates and ignores file watches. Reloading a document did not refresh compiled modules; normal-URL embedded source-map bytes proved staleness. `Composer.tsx` and `ActivityPanel.tsx` matched both old and current source because unchanged. The following three modules matched old `e96fcc8df`, not `4dcd98dc9`: `StackHost.tsx`, `openTranscript.tsx`, `workspace.ts`.

Local scripts and evidence:

- `W/preview.pid`, `W/preview.log`, `W/preview-live-verification.json` identify the old process.
- `W/stop-approved-mixed-preview.py` preserves those files and checks exact PID, start, command, cwd and listener twice around the authorized signal. **Do not run it blindly after documentation commits:** it hardcodes HEAD `4dcd98...` and creates its output directory before checking HEAD. First inspect it and adapt a new copy only after independently proving the intervening delta is docs-only and recording the new expected HEAD/output location. Preserve all original assertions and evidence.
- `W/check-preview-mixed-served-source.py` captured the five normal module URLs and decoded the embedded source maps; `W/preview-mixed-source-before-restart/` is immutable stale-source evidence. For the post-restart run, use a new output directory and the actual verified commit. Require all five `sourcesContent` values byte-equal their `git show <commit>:cmd/evener-hub/frontend/<path>` sources, and present fix markers. Do not reuse/overwrite the old output or claim boolean constants prove live identity.
- `W/verify-preview-live.py` checks eight HTML fixture routes, `/rpc`, `/api`, `/auth`, `/doc` and out-of-frontend file denial, then PID/listener. It rewrites its result file, so preserve that file first.
- `W/panel-current-preview-rider.md` names the old build/PID. Supply a new explicit current-build rider at native dispatch; do not let its stale identity override the refreshed proof.

After exact-process stop and listener release, launch the same command above with the native shell tool's **detached** mode from the same frontend cwd. Preserve and then update the ordinary `preview.pid`/`preview.log` paths; verify the real returned PID and actual listener rather than assuming a shell wrapper PID. Do not alter config, ports, user services, or other Vite processes. Never terminate by a broad process-name match.

A new clone needs `npm ci` in the frontend before browser/build gates. Do not reinstall or share another worktree's dependency directory merely to run these docs checks. The preview's resolved proxy must remain absent; the ordinary browserguard config inherits live-hub defaults and is not a safe public preview by itself.

## Verified gates and preserved failures

The completed final parent run is `job:job_034Lh4Nt7V2E7g6Fjc7JGk_vMfWF4KidID4`. Read its retained output with `read_transcript`; all 35,974 raw bytes were read. Results and numbered raw logs are in `W/mixed-final-panel-parent-gates-absolute/`.

| Final step | Actual result |
|---|---|
| Touched frontend Biome | 79 files, no changes, exit 0 |
| `make merge-approval-gate` | Nine lint groups; frontend/runtime builds; ROOT_FULL root, agent, llm, auth, envvars, invariant, identifier and web checks; exit 0 |
| `make vet` | Exit 0 |
| `make test-web-browser` | Layout, overflow, shell, spawn and transcript-scroll guards all pass |
| Frontend `npm run build` | Typecheck and production build, 586 modules, exit 0 |
| `npx vitest run src/dev/editorial-preview` | Six tests, exit 0 |
| Fixture isolation/touch Node tests | Fifteen tests, exit 0 |
| Original independent preparation and tests | Preparation 0; four selected tests pass, 86 intentionally filtered |
| Copied deeper preparation and tests | Preparation 0; eight selected tests pass, 172 intentionally filtered; includes duplicate direct controls |
| `git diff --check`, final HEAD/clean checks | Exit 0, unchanged `4dcd98...` |

The twelve count includes the two preparation steps separately. The runner uses `GOMAXPROCS=4`, `ROOT_P=4`, `AGENT_P=4`, `AGENT_PARALLEL=4`, strips ambient Evener live/E2E opt-ins, and preserves canonical worker/default deadline settings. Parent Biome is read-only because reviewed writer files were already formatted. Future touched frontend changes still require the repository's `npx biome check --write` step before the canonical frontend gate.

`W/panel-p2-mixed-parent-gate-absolute-runner/check-final-panel-parent-power.py 4dcd98dc9ed0dcdb700637809b38e150c5b664f3` ran with exit 0. It checked all twelve actual UTC intervals against fresh primary macOS power events, zero sleep overlap, our own two caffeinate assertions on AC, their release, each pipeline status and clean tested HEAD. Result: `W/mixed-final-panel-parent-power-check-absolute.json`; primary power SHA-256 `ba8381da2c418bfaca1cd2c509573c4cc87bda24b5c022072134372d6971bfa1`. That checker also writes exclusive outputs; do not rerun into them or reinterpret a changed docs HEAD as the original test commit.

### Earlier failures that remain part of the record

- Parent launcher attempt 1 (`..._FWi88K7GGxiU`) failed its own PID assertion because the script was invoked relatively while the predicate required its absolute path. **No tests ran.** Original own caffeinate PID25743, AC and both assertions were recorded. Evidence: `W/mixed-final-panel-parent-preflight-failure-copy/`.
- Parent launcher attempt 2 (`..._Vk5O5NPHQqUB`) failed at output-directory creation. A basename replacement had not changed the full-path literal, so it still targeted the first attempt's existing directory. **No tests ran.** The launcher retry loop stopped.
- Independent read-only reviewer `dlg_034MYZ4VI4nLXBDZabLqvW` verified the one-literal full-output-path correction, absolute argv identity and unchanged twelve gates/power assertions. The final launch then used a fresh `mixed-final-panel-parent-gates-absolute` output and the absolute script path. It actually executed the tests above. These were coordinator failures, not product-test failures.
- The mixed-origin writer's earlier all-five browser gate failed only `web-transcriptscrollguard` on initial latest-pill visibility; the other four passed. Matched unchanged baseline/candidate diagnostics and one passive trace passed, leaving the original cause **unreproduced**. Jesse authorized one unchanged complete browser gate, then build if it passed. Both passed, as did the final integrated parent browser gate. Never erase or relabel the original failed run; no startup readiness/assertion/tolerance/deadline was weakened.
- Historical web runs emitted React `act` and fixture diagnostics. Prior raw warnings/errors were preserved, not muted. Canonical parent output is a summarized wrapper; its zero exit is not a claim that every hidden historical diagnostic was absent.
- Earlier long aggregate failures included host sleep and worker-bootstrap failures; that motivated the scoped AC/caffeinate/actual-window evidence. Do not infer a causal explanation for every earlier test timeout from a later awake pass.

The original approved full-AppShell matrix remains 16 configurations / 32 geometry samples / 16 native and trusted pairs, with a primary 900-character token, actual narrow452/wide1112 pane measurements, eight wide timestamp cases, and original independent geometry references. Additive touch coverage rejected 90 dimension mutations, 16 missing-Send samples and eight missing-Open samples. Preserve the original assertions, indexing, pairing, tolerances and reference data. These completed audits are not a request to restart that matrix.

Coverage does not include physical devices/soft keyboards, Safari/WebKit, a screen reader/assistive technology study, live providers/hub transport, or comparative optical/color testing. The initial reported Vitest advisory was a baseline dev-dependency observation; the final runner reports Vitest4.1.11. No fresh whole-dependency security audit is claimed here.

## Recovery map, ownership and evidence

Preferred existing checkout:

```text
/Users/jesse/.local/state/evener/projects/Users-jesse-git-prime-radiant-evener-fukrfUwjZk/worktrees/Users-jesse-git-prime-radiant-evener-fukrfUwjZk/tufte-webui
```

Controller transcript: `local:034Lh4Nt7V2E7g6Fjc7JGk`. Use `read_transcript`/`find_session_transcripts`, never raw transcript files. Historical exact approval is in turns189/191 and205/206; detailed continuity checkpoints include5868 and6155. The tools failure is in its own transcript turn203. These are recovery references, not instructions to reread thousands of unrelated turns.

`W` contains the chronological `progress.md`, binding briefs, unchanged gate runners, raw output, reports and audited copies. **W is ignored and is not shipped by this PR.** This committed handoff carries the required operational instructions, but a different machine cannot assume the local artifacts or delegate controls exist. Preserve/copy the named evidence before cleanup. If it is unavailable, recover it from the controller/archive or ask Jesse; do not fabricate hashes, claim prior audits were rerun, or silently relax independent checks.

| Unit | Current owner / retained reference | State |
|---|---|---|
| Phone native reviewer | `dlg_034MILorq6GYRqXtz2tghe` | P1 ENDORSE; six Send and two question cases plus shortcut; do not repeat unaffected tasks |
| Workspace native reviewer | `dlg_034MU67GTv98jaLR9bCfQ2` | W1 ENDORSE; seventeen tasks, no required failed/blocked task |
| Tools native reviewer | `dlg_034MTEwjtEUSAvwYyN6shR`; `local:034MTEwjtEYhmk8qqxZ8xr` | PREPARED/HOLD, exact mixed retest pending |
| Mixed-origin writer | `dlg_034MU42SiEkyJVzEdCTpSE`; `local:034MU42SiEnq5ugBnlteia` | Clean integrated3189 lane retained; no new work authorized |
| Mixed source reviewer | `dlg_034MWMzSVv1cz5ocSnVPDm` | Completed APPROVED; no repeat source review |
| Runner reviewer | `dlg_034MYZ4VI4nLXBDZabLqvW`; `local:034MYZ4VI4rd1hmjPQw76f` | Completed read-only preflight review |
| Evergreen docs writer | `dlg_034MYkt37miVmHMgpdBPN7`; `local:034MYkt37ml2RPmLPr9v9L` | Owns only the three canonical docs in its isolated lane; inspect its final report/commit and this handoff's delivery record before further edits |

Key local paths, all relative to W:

- `panel-tools-mixed-retest-brief.md`: current pending retest; `panel-tools-p2-retest-brief.md`, `panel-role-scripts.md`, `panel-rubric.md`, `panel-environment.md` supply the original full contracts. Old reviewer/PID clauses are superseded only by explicit current dispatch.
- `panel-tools-p2-retest-copy/`: complete prior native rejection and raw evidence. Original audit checked 86 DOM captures and three source maps; pane focus and DOM focus remain distinct observations.
- `panel-p2-mixed-source-review-copy/report.md`: final source approval; SHA-256 `be3804ee8f9615155c6ea03db0ccfb27125e4740a896d73c98288635d474c4b4`.
- `panel-p2-mixed-resumed-check-fix-copy/`: final writer report/evidence, all96 manifest entries copied byte-identically. Report SHA `25eac66e8bc18b8d36b74149dd8c5416ccd5d9cdd62a467563a852af7ebb9fbb`; manifest SHA `353b35e28c086e5f62272278ff7c44938f84ddb20e1a07cbe5164cb5538b646e`.
- `audit-mixed-delivery-parent.py`, `panel-p2-mixed-delivery-parent-audit.json`: complete original commit/power/preservation assertions replayed against copied primary evidence, not a substitute oracle. Earlier failed wrapper selected a basename instead of the exact root manifest; only that independently proven selector was corrected.
- `panel-p2-mixed-final-copy/`, `panel-p2-mixed-resumed-final-copy/`, `audit-mixed-scroll-diagnostic-parent.py`, `audit-mixed-scroll-trace-parent.py`: original failure, diagnostics and preserved unsuccessful preparation. Keep them.
- `panel-p2-mixed-integration.json`: exact source/target/base, clean preflight, no-overlap and byte-preserving integration evidence.
- `run-final-panel-parent-gates.py`, `check-final-panel-parent-power.py`: original proven runner/checker. Correct final copies are under `panel-p2-mixed-parent-gate-absolute-runner/`; `preflight-mixed-parent-launch.py` proved whole-script preservation except output paths. Their fixed output destinations are already occupied.
- `whole-branch-review-evidence/prepare-repro.mjs`, `whole-branch-review-evidence/review.vite.config.ts`, `prepare-r1b-parent-repro.mjs`, `task-4-r1b-parent-repro/vite.config.ts`: retained independent navigation checks. They depend on explicit parent imports and prepared review-local dependencies; missing dependencies are a launch/collection failure, not a test verdict.
- `task-4-docs-fix-evidence/check-doc-links.py`: original location-dependent three-doc checker. A lane copy must retain the same relative path so it checks that lane, not the parent.
- `final-pr-preparation/`: pre-draft ledger snapshot, all seven then-recorded Ruling lines and draft body. Later authorizations are recorded below.

### Commands for fresh orientation

Run from the intended checkout, inspect output, and stop on unexpected refs or dirty files:

```sh
git branch --show-current
git status --short
git rev-parse HEAD
git remote get-url origin
gh pr view 1124 --repo prime-radiant-inc/evener \
  --json url,state,isDraft,baseRefName,headRefName,headRefOid,statusCheckRollup
git diff --stat 4dcd98dc9ed0dcdb700637809b38e150c5b664f3..HEAD
```

The last command must show only the delivered documentation changes if reusing implementation gates. For new production changes, rerun the actual covering gates; do not stamp the old tested SHA onto new source. Use the existing full runner only through a fresh inspected copy with new output destinations and correct expected commit. Invoke it with an **absolute** path under `/usr/bin/caffeinate -i -s /usr/bin/env python3` on AC. Preserve the twelve commands and all original power/exit assertions; no blind retry loop.

For ordinary current-source verification, the canonical public gates are `make merge-approval-gate`, `make vet`, `EVENER_HUB_ADDR=http://127.0.0.1:1 make test-web-browser`, and frontend `npm run build`, plus the fixture and retained independent tests above. Default tests must not issue live requests. Do not treat unavailable local independent evidence as equivalent to completed verification.

## Rulings and recorded costs

These preserve the seven original recorded rulings and the later explicit delivery/restart decisions. They are historical decisions, not permission to weaken current checks.

1. Run disjoint transcript and shell writers in isolated worktrees after foundations land. Cost if wrong: integration rework, bounded by frontend gates.
2. Stop ineffective repeat fixes after two cycles and reslice/root-cause reassess. Cost: extra diagnosis rather than weaker verification.
3. The fixture integration had two real merge bases. Compare the lanes against their shared reviewed source tree after proving both bases' ancestry and exact disjoint changed paths; verify every post-merge blob. Cost if wrong: integration rework. This is a resolved historical graph issue, not permission to choose a convenient base later.
4. Correct the original Send-focus proof's independently invalid setup only: required `TurnStartResponse.turn` and removal of unsupported/ignored `ByRoleOptions.exact`, preserving literal matching and all actions/assertions. Cost if wrong: masking the focus boundary; corrected proof had to remain focus-red against immutable original production before candidate green.
5. Jesse authorized replacement of unavailable earlier sessions with original briefs/evidence and exact retests. Cost: lost continuity, mitigated by complete findings/handoffs. The now-specified same tools reviewer remains required.
6. Jesse authorized replacing stale isolated PID36337 while preserving URL/port/command/config/cwd and all evidence. Cost: preview disruption/wrong-process termination, bounded by exact identity checks and served-source proof. This produced current PID66313.
7. Jesse authorized one unchanged canonical all-five browser run and conditional build after the original initial-scroll failure remained unreproduced. Cost: an intermittent defect may remain despite a later pass; keep the failed evidence/unknown cause and apply later required gates/native acceptance.
8. Jesse separately authorized replacing now-stale PID66313 **only after final gates pass**, at the same isolated identity except PID. Cost: preview disruption/wrong-process termination; the same exact identity/preservation/source-proof safeguards apply. It has not been executed.
9. Jesse selected **Open a draft by the deadline**, explicitly allowing a draft with unfinished acceptance listed. Then he ordered docs and a complete pickup handoff next. Cost: a published draft can be mistaken for accepted work; keep draft status, blockers and tested SHA explicit. This authorizes neither merge nor a false panel endorsement.

## Delivery record

The draft was created and read back as OPEN / `isDraft=true`, base `main`, head `tufte-webui` at `4dcd98...`. The documentation follow-up is a separate reviewed delivery on that branch. Check the current PR head and the documentation commits rather than treating the implementation SHA as the final documentation SHA.

At this handoff, the next product action remains the safely refreshed preview followed by the **same** tools reviewer's native retest. The preview is deliberately unchanged; P2 is not endorsed. All earlier completed reviews, original geometry assertions, failures, ownership boundaries and user-service protections remain intact.

### Documentation delivery and additional pickup cautions

- The three canonical docs were committed at `934d2b9958a50d74d0e4102e870e6f8b398fb473`. Independent reviewer `dlg_034MZDxOlNOLJvn0JOflEJ` approved that delta and this handoff, with no Critical/Important findings: 31 canonical-doc links and six handoff links, zero errors. The handoff commit was `91c0a6bd5360976a9b2d91527e856f71c1e17059`; exact-ref, disjoint-path integration produced `959ff2738b1d7fa23c7c009f211829e5b51b5104`. Parent link checks passed, all non-documentation bytes matched tested `4dcd98...`, and the pushed PR head was read back as `959ff2738...`, OPEN and draft. This operational addendum follows that delivery; it changes no implementation or acceptance requirement.
- The canonical guide's §8 preserves previously adjudicated accessibility limits: FocusScope does not make outside content `inert`, and Tooltip retains touch-device timer/`aria-describedby` behavior to avoid losing assistive descriptions. These are historical limits with architectural/real-device tradeoffs, not newly authorized Tufte fixes or evidence of complete assistive-technology acceptance.
- A fresh `ps` check confirmed PID66313's exact command/start; `lsof` still showed `*:5197`. It also warned that it could not stat the OrbStack NFS mount and an APFS mount under `/private/tmp/17@113AD943/c`, so do not infer complete filesystem visibility. Raw output: `job:job_034Lh4Nt7V2E7g6Fjc7JGk_qsU2LAJyuwNV`. No process was stopped or restarted.
- The last inspected CI snapshot showed only `roborev` PENDING; no CI-green claim is made. Inspect the current pushed SHA's checks when resuming, without treating this historical snapshot as current state.
- Native worktree inventory confirmed these retained writer lanes clean and merged by ancestry into `tufte-webui`: `dlg_034LxBv1d7RF1uHG7J45JT` (P1), `dlg_034Lxlh1zY6RgpJ80N6aqW` (P2), `dlg_034LyKmdCzZjMIlvFeeeyE` (W1), and `dlg_034MU42SiEkyJVzEdCTpSE` (mixed origin). Preserve them for now. After evidence preservation and the necessary retest, recheck their actual refs/status and dispose only named safe lanes. Do not globally prune the many unrelated worktrees on this host. The docs writer's report/evidence were copied byte-identically to `W/final-docs-delivery-copy/` before integration.
