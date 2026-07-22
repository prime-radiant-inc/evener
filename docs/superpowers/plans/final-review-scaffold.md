# Final Whole-Branch Review — Adjudication Scaffold

**Date:** 2026-07-22 · **Author:** controller (doc-only) · **Branch:** `worktree-webui-workspace-shell`
**When it runs:** the **last** serial step — after W8 merge → M10 deletion + flag-flip → M9 live e2e.

> This scaffold exists so the final reviewer **verifies rather than re-derives.** Every stream and
> its close/absorb round was already adversarially reviewed; the whole-branch web rewrite is the sum
> of those reviews **plus** a thin layer of controller-authored merge/wiring/fix/plan commits that no
> adversary has seen. The final review's job is (a) audit that thin controller layer, (b) confirm the
> cross-wave seams still cohere on the final tree, (c) confirm the deletion stayed inside its reviewed
> inventory, (d) confirm the M9 evidence supports the two ratified-by-default items, and (e) re-run the
> gates. It is **not** a re-review of the streams. Use the pointers below; do not re-litigate settled
> decisions.

---

## 1. Baseline — the last final review (do not re-do its work)

The previous whole-branch pass is **`.superpowers/sdd/final-two-wave-review.md`** (opus), verdict
**READY FOR WAVE 6**, report committed at **`18e049f5f`**, reviewed range **`0f3bcaff2..2e2dccab5`**
(reviewed tip `2e2dccab5`). It found **0 Critical / 0 Important / 0 Minor** code defects, all six gates
green, and it **already audited** the controller-authored commits up to `2e2dccab5`:
- `e7243bd71` main-absorb catalog unions + regenerated doc (correct supersets, no drops).
- `388129621` SA5011 guard, `2789b561f` requireclass comment reword, `5722471dc` helpers comment.
- Merges `e9b6b5395` / `087c0fd1f` / `3024b6b97` / `2e2dccab5` collision surfaces (empty combined diffs,
  supersets of both parents).
- The five cross-wave seams (prefs single-source, requireclass over the union, `initPrefs` +
  scheme-listener, dispatch-map completeness, `ItemModel.error`/`exitCode` consistency).

**The final review starts from `2e2dccab5` and audits forward.** Everything at or before that tip is
covered; do not re-audit it. Its Duty-3 triage (§6/§7 below), Duty-4 stale-prose list (§8), and
Duty-2 seam analysis are inputs to carry forward, not work to repeat.

---

## 2. Controller-authored commit inventory since the last final review

These are **the only never-adversarially-reviewed changes** on the branch. Everything else is a
reviewed stream (its exact range + verdict is in `.superpowers/sdd/progress.md`). The final reviewer
audits **this list** the way the last review audited its controller commits — merge-resolution
correctness, superset-not-subset, no silent drops, gate currency.

### 2a. Enumeration method (re-run at final-review time — the list below will have grown)

The branch will accrete more controller commits before the final review runs (W8 merge, the deletion
series, any M9-surfaced fixes). Regenerate the authoritative list:

```
# All first-parent commits since the last-reviewed tip:
git log --oneline --first-parent 2e2dccab5..HEAD
# The merge commits among them (each carries a controller conflict resolution to audit):
git log --oneline --merges 2e2dccab5..HEAD
```

Then **subtract the reviewed-stream ranges** (each recorded in `progress.md` as "complete
(range, review clean)"): what remains on first-parent is the controller-authored surface. A commit is
controller-authored-unreviewed if it is (i) a merge + its conflict resolution, (ii) a direct
controller code commit (wiring, a union-gate fix), or (iii) a plan/inventory doc. `sdd:` commits are
progress-ledger prose (doc-only bookkeeping — no code to audit, but they are the decision trail §9
draws on).

### 2b. Snapshot as of this scaffold (HEAD `33ba4c736`, integration)

Reviewed-tip `2e2dccab5` → HEAD is **51 first-parent commits**. The controller-authored code/merge/doc
subset:

| Commit | Kind | What to verify |
|---|---|---|
| `ed5057be2` | **merge** W6 → integration | Union of the wave-6 surfaces; superset of both parents (rolls up the wave-internal merges `4bab9ca27`/`d8717b707`/`c7b9a3134`/`59123dd35` + main-absorb `5c3d8d66e`, all reviewed on the wave branch). |
| `47012b40c` | **merge** main-absorb | Brings MW5 Go (doc raw endpoint, terminal-error status, legacy-hydration fix, nosniff); zero-conflict per ledger — confirm the appwire catalog + doc are current (`make generate` zero-diff). |
| `f68e6b559` | **code** (controller) | Warm the Settings **and** its lazy component chunk in the pane-registration test — the union-gate cold-lazy flake fix (F3 signature). Confirm it warms the *component* chunk, not just `./index`. |
| `c07d0aa2f` | **merge** perf-tests → integration | Node env-routing + incremental tsc; disjoint from hygiene commits (`.test.ts` vs `.tsx`). Reviewed on its branch; the merge is controller. |
| `ae3f71adb`,`d043a1b34`,`835c17e2b` | **doc** (plan) | Wave-6 plan + its two amendments (recent-prompts dropped; SpawnResult qualified-ref seam ruling). Doc-only. |
| `a7b61bda8` | **doc** (plan) | Wave-8 plan. Doc-only. |
| `1b657aca4` | **doc** (inventory) | **M10 kill list** — the deletion inventory. Not code, but it is the authority the deletion is checked against (see §10); read it before auditing the deletion series. |

**Not in this list because already reviewed:** the W6 spawn-hygiene pair (`d0d186106`/`8469050e2`,
review approved, ledger), the W6 close micro-items, MW5 and its fix round (reviewed on main), the two
perf passes (reviewed on their branches). **Still to be added at final-review time:** the W8 →
integration merge + any W8 controller wiring; the M10 deletion + flag-flip series (itself
`deletion-review-approved` per ledger — but its **merge resolution + flag flip** is controller work);
and any fix commits M9 surfaces.

---

## 3. Divergence-ledger pointers (one per wave + the close sweeps)

Each wave recorded its conscious divergences in a committed report. The final reviewer confirms none
has silently changed and that the ratified ones stayed ratified — it does **not** re-derive them.

| Source | Location | What it holds |
|---|---|---|
| Wave 5 report | `docs/superpowers/plans/wave5-report.md` §"Decisions", §"Consciously-diverged" | composer/queue/pending/ask divergences; the 5 named Jesse decisions. |
| Wave 5 close sweep | `.superpowers/sdd/w5-close-t6-parity-sweep.md` (HIGH/MEDIUM/LOW + "Consciously-diverged clusters") | the two HIGHs (now A1-fixed), the MEDIUM cluster incl. the **ask-transcript re-architecture** ratification item. |
| Wave 6 report | `docs/superpowers/plans/wave6-report.md` §"Divergence ledger" | qualified ref, recent-prompts drop, chip-chrome equivalence, all-OFF notifications, Web-Locks-only, left drawer, ⌘B guard, /theme immediate, /fork omission, scan-widening, … |
| Wave 6 close | `.superpowers/sdd/w6-close-t6-report.md` (250-item sweep: 159 met / 78 diverged / 13 gap) | the headline hub-spawn journey; §1.14 spawn-reset (fixed pre-merge), StatusRow epoch clock, FOUC, showCost-inert. |
| Wave 7 report | `docs/superpowers/plans/wave7-report.md` §"Divergence ledger" (18 entries) + §"Decisions for Jesse" | read-only sections via overview, theme listener (KEPT), envMap, launchConfig no-cross-client, notifications all-OFF, model-picker cut (→W8), sidebar-mode (→W6), … |
| Wave 8 report | `docs/superpowers/plans/wave8-report.md` **(not yet written — W8 is closing)** | doc-sanitization/truncation beyond-parity, single-pane-vs-read-only split, dockview-native §3 divergences, pending-chips-beside-composer, the W6-close fold-ins. **Fold into the review when it lands.** |

---

## 4. The final review's own duties (a checklist, not new analysis)

1. **Audit the controller-authored layer** (§2, re-enumerated) — merge resolutions are supersets of
   both parents; `make generate` yields zero diff; the deletion merge + flag-flip are correct.
2. **Confirm the five cross-wave seams still cohere** on the final tree (last review's Duty 2 is the
   baseline — re-confirm, don't re-derive): prefs single-source; requireclass over the union; `initPrefs`
   + single scheme-listener; settings dispatch-map vs section registry; `ItemModel.error`/`exitCode`
   render consistency (now with W8 T3 rendering `error` — confirm it is no longer "uniformly unrendered").
3. **Confirm the deletion stayed inside its reviewed inventory** (§10) — this is the milestone's one
   catastrophic failure mode.
4. **Confirm the M9 evidence** supports the two ratified-by-default items (§8) and that no M9 finding
   was left unadjudicated.
5. **Re-run all gates** independently (tsc → vitest with file-count check, biome, build+PLACEHOLDER,
   `go build ./...`, `make lint`, `go test ./cmd/serf-hub/...`).
6. **Sweep stale prose** (§ carried from last review's Duty 4 — see §8 note).

---

## 5. Punch-item ledger — every open item, with its scheduled-where tag

From `final-two-wave-review.md` Duty 3, updated with where each landed. The final reviewer **verifies
homed**, not re-triages.

### Already resolved on-branch (confirm still fixed) — 3
- Denied/errored `ask_user` renders answerable → **A1** `deriveAskQuestions` `item.error===undefined` gate.
- Escalation-resolve not Conflict-terminal → **A1** `resolveEscalation` → `mapConflict`.
- `serf/sandbox/escalation/resolved` multi-client clear → **A1** reducer case (`reducer.ts:628`).

### schedule-W6 (verify W6 delivered) — 5
- `/` command palette → **W6-T3** (⌘K, 23 commands).
- `sidebarMode` inert → **W6-T5** consumer (`useSidebarMode`).
- OS-notification `loudScope` → **W6-T4** notifications engine.
- Notifications-default cross-wave disagreement (W7 all-OFF vs engine v3 title/favicon-TRUE) → **W6-T4**
  followed W7's decided **all-OFF** (confirm the engine reads all-OFF, ledger W6-T4).
- Instance-CRUD cross-client live-update → fixed on **main** (`28e2b2141`), arrives via main re-absorb
  (confirm present on the final tree).

### schedule-W8 (verify W8 delivered — the 12, each homed in the W8 plan) — 12
Model-picker catalog → **T2**; `ItemModel.error` text unsurfaced → **T3** (+ **MW-A** Go); optimistic
send/steer/drain chips → **T4**; model-switch not busy-gated → **T4**; model-picker not
Escape/outside-click dismissable → **T4**; `DEFAULT_EFFORT_LEVELS` fallback → **T4** (trace-first);
location cluster branch/worktree/cwd → **T4**; `showCost` no consumer → **T4** (or conscious-defer);
dir-list "N entries" header → **T7**; `withBusy` on non-destructive per-row buttons → **T7**; Installed
plugin status dot → **T7**; `/settings/providers`→`/credentials` redirect → **T1**. (Plus the cosmetic
`?cwd=` page → T7.) Confirm each landed or was consciously deferred in the W8 close.

### W6-close new items (verify dispositioned) — 4
- §1.14 spawn-no-reset (duplicate-send hazard) → **fixed pre-merge** (`d0d186106`+`8469050e2`, reviewed).
- StatusRow "495269h" work-clock epoch-zero bug → **W8-T4** fold-in (verify fixed).
- §4.2 FOUC pre-paint successor → **W8** fold-in / punch (verify dispositioned).
- `showCost` inert → **W8-T4** (same as schedule-W8 #8).

### Go follow-up (separate track) — 1
- Projector hardcodes `Status:"completed"` on error (`appwire_projection.go:437`) → **MW-A** (W8
  controller-scheduled main-writer). Confirm the wire now stamps terminal-error status.

---

## 6. Accept-permanently ledger (conscious divergences — do NOT re-litigate)

Consolidated from `final-two-wave-review.md` Duty 3 "accept-permanently (~16)" plus the wave
divergence ledgers (§3). These are **decisions**, cited here so the reviewer confirms they are still
the shipped behavior and moves on.

**Transport / failure-feedback / interaction model**
- Failure feedback = **toasts, not inline banners**; optimistic-pending failure → **toast-and-remove**.
- Plain send is now **optimistic** (beyond parity).
- Transport is **AppWire JSON-RPC**, not REST.
- **ConfirmDialog everywhere** for destructive/consequential actions (beyond parity; legacy used native
  `confirm()` or nothing).
- **Queue-edit is text-only** (dropped image attachments → warning toast, never auto-restored).
- **Draft-preserve** on ask-Conflict (append-after-blank-line, not overwrite) — **Jesse-approved 2026-07-21**.
- **Cancelled-tone neutral** (color-is-attention).
- **Hide-don't-clear** ask lockout (composer inert, draft `text` survives).

**Shell / routing / panes**
- Mobile **nav-as-page** via React conditional render (< 900px), not `body[data-settings-pane]`.
- **Qualified `thread.serf.ref`** everywhere (wire-truth; legacy `local:`-strip is dead-on-arrival).
- dockview-native **§3 divergences**: `panes.js` iframe/postMessage not ported; **max-3-pane cap** and
  **auto-open-observer** not ported (dockview manages space); popout is dockview-native.
- `/thread` **single-pane pending M9 ratification** (§8 #2) — the composer-live reading.
- `/thread` **unknown-ref fallback title kept** (beyond-parity; legacy blanked it).

**Settings / prefs / notifications**
- Read-only settings sections fetch **`serf/settings/overview`**, not server-rendered HTML.
- **Theme `prefers-color-scheme` listener KEPT** — **Jesse 2026-07-21** (veto not exercised).
- **credentialsStore staleness extension** stands as reviewed (Jesse no-veto).
- **launchConfigStore no-cross-client-live-update** boundary (§9/§11/§18 hold no cached state).
- **All-OFF notification defaults** (the safety pin; W6 engine follows it).
- **Web-Locks-only** leader election (no BroadcastChannel).
- Notifications **title base `"<pane> · serf hub"`**; **/theme immediate apply** (hazard-1 fix).
- **envMap** structured NAME/value inputs restored; **CustomEvents** replaced by prefs-store reactivity;
  launch-engine path/pathList = **validated free-text** (no PathPicker); `showCost`/`enterToSend` not
  mirrored to `body.dataset`.

**Transcript / palette / search**
- In-session search scan **widened beyond the floor** (text + tool output + tool error + reasoning).
- **`/fork` palette-omitted** (needs an edited message the palette can't collect).
- **Scroll-to-hit deferred** (beyond-parity).
- Doc pane **beyond-parity**: DOMPurify markdown sanitization (legacy had none) + shown **truncation
  notice** (legacy silent).

**LOW cosmetic cluster** (final-two-wave Duty 3 tail): pasted-image name uses `File.name`; `📎` chip
prefix dropped; no visible state word; no in-place "Allowed once"/"Denied" escalation state; provider
tabs → flat Combobox; empty-steer no placeholder hint; `writeFor` fork-child staging absent; draft-key
string deltas.

> The **recent-prompts row** (spawn §1.1) is **permanently dropped** — Jesse 2026-07-22 "DROP THE ROW"
> (no UI, no storage). Recorded here as a decision, not a gap.

---

## 7. Wave-8 close fold-in (pending)

W8 has not closed yet; its close will produce a divergence ledger + punch list (`wave8-report.md`) and
its own live-proof record. **At final-review time, fold W8's close output into §3, §5, and §6** the way
the earlier waves are folded here. The W8 plan's self-review already enumerates its conscious
divergences (single-pane-vs-read-only split, dockview-native §3, pending-chips placement, doc
sanitization/truncation) — those become accept-permanently entries unless the W8 close reclassifies them.

---

## 8. M9 ratification items (2) — confirm the evidence, then Jesse rules

Jesse's pre-flight authority #4: these **proceed as built** and are **described in M9 evidence for
post-hoc veto**. The final review confirms M9 actually captured the description; it does not grade them.

1. **Ask-transcript re-architecture** — no `[data-ask-anchor]`, no `.ask-settled-line`, dock not
   `form`-owned (wave-4 structural choice; `w5-close-t6-parity-sweep.md` MEDIUM #7; final-two-wave Duty 3
   must-ratify). M9 **S2** describes the assembled behavior. Confirm the description is present and
   sufficient for a veto decision.
2. **`/thread/{ref}` single-pane live composer** — the share link carries a LIVE composer (W8
   §Ambiguities #1 resolution, matching tested legacy §2.5), not a read-only snapshot. M9 **S3**
   describes it. Confirm the description is present.

Neither is a bug. Both are ratification gates whose evidence Jesse reviews on return.

---

## 9. Jesse-decision trail (each ruling, dated — the authority for every "why")

Sourced from `progress.md`. The reviewer uses this to confirm a divergence is Jesse-blessed rather
than a drift.

- **2026-07-20** — parallel implementers OK with exclusive per-stream file manifests in per-stream
  worktrees (`feedback_parallel_impl_file_ownership`).
- **2026-07-21** — strict **Biome** lint+format for all frontend JS before W5 (full eslint replacement).
- **2026-07-21** — parallelize **W5 + W7**; **W6 after W5** (palette needs W5 actions; theme wants a
  tokens.css quiet window).
- **2026-07-21** — **draft-preserve** divergence **APPROVED**; **wire-honesty** merged to main;
  questions to Jesse go **one-by-one**.
- **2026-07-21** — **"always fix the flakes. root cause"** + **"fix them in our tree"** (standing rule;
  overrides subagent "out of scope" deferrals; no timeout widening).
- **2026-07-21** — `.env` gitleaks **allowlist** (worktrees + `(^|/)\.env$`), scream-stance reversal.
- **2026-07-21** — **PUSH = HOLD LOCAL** (no origin push; Jesse pushes himself); **steer copy = "Steer
  queue now"**; **theme listener KEPT**; **credentialsStore** extension stands.
- **2026-07-21** — confirmed **autonomous run through M10**.
- **2026-07-22** — **model-picker catalog: RESTORE IN W8** (interim plain input blessed); **instance-CRUD
  broadcasts: FIX ON MAIN NOW**.
- **2026-07-22** — **recent-prompts row DROPPED**.
- **2026-07-22 (pre-flight offline authorities, adopted in Jesse's absence, vetoable on return):**
  1. **Order flip ADOPTED** — W6 close → W6 merge → W8 → **M10 deletion + flag-flip** → **M9 on the
     final artifact** → **final whole-branch review**.
  2. **Deletion = conditional pre-approval** — executes only if the kill-list Appendix-C re-validation
     is clean against the final tree, the deletion stays **strictly within** the reviewed inventory, and
     **all 24+ protected endpoints survive**; any ambiguity → deletion **waits for Jesse**. Safe-default:
     genuinely-orphaned REST routes are **kept, not deleted** (contract changes aren't drive-bys →
     punch-list).
  3. **Judgment calls** — controller adjudicates via tested-legacy parity + design-system precedence;
     every call lands in a decisions section for Jesse's veto.
  4. **M9 ratifications by default** — the two items in §8 proceed as built, flagged for veto.
- **Controller-adjudicated under authority #3 (flagged for veto):** **MW-B GO** (doc raw endpoint) and
  **single-pane composer = LIVE** (ledger 2026-07-22, W8 dispatch).

**Standing constraints for the reviewer to honor:** no pushes ever (hold-local); the branch is verified
as **ready-to-merge for Jesse's return**, not merged to origin.

---

## 10. Deletion-verification duty (the milestone's one catastrophic failure mode)

The deletion is `deletion-review-approved` (its own adversarial review) **and** conditionally
pre-approved by Jesse (§9, authority #2). The final review confirms the conditions held:

- **Scope containment** — the deletion touched **only** the kill-list inventory: 262 whole files
  (`assets/*.js` + `style.css` + `templates/**` + `jstest/**`), ~31 Go surgical sites across 11 files,
  and the flag flip. **Zero whole-Go-file deletions** (every Go excision is surgical). Cross-check the
  deletion diff against `docs/superpowers/plans/m10-kill-list.md` §1.
- **All 24 protected endpoints survive** — kill-list §2.1. **M9's S7 suite proves this live** (SPA-side
  via the browser + TUI-side via the 13-route `hubapi.Client` contract). The final review reads S7's
  evidence rather than re-testing; it confirms S7 covered all 24, especially the **TUI-only** ones the
  SPA can't reveal.
- **The `/api/search` reclassification** — the ⌘K palette made `/api/search` **SPA-consumed** (W6-T3),
  flipping it from the §1.6 orphaned cluster to **protected**. Confirm the deletion **kept** it (and the
  other §1.6 orphaned endpoints, per the safe-default) — the kill-list Appendix-C re-validation and the
  dry-run agent both check this.
- **Flag flip is a true no-op** — `newWebEnabled()` deleted; `SERF_HUB_WEB` read nowhere; the default
  serves the SPA at every page route (M9 S7 card 4).
- **No legacy residue** — `git grep htmx` empty; no live reference to a deleted `assets/*.js` /
  `templates/**`; `/doc/*` reshaped (MW-B) so no dead legacy-asset links; PWA icons + manifest + the 4
  auth-exempt icon paths **survive** (kill-list §2.4 / R3 — a naive `rm -rf assets/` was the trap).
- **Gates green post-deletion** — including the dead-symbol sweep (`staticcheck U1000`/`deadcode`) the
  kill-list §1.5-§1.6 mandates and the updated `webnext_test.go`.

If any condition is ambiguous, the deletion should have **waited for Jesse** (authority #2) — the final
review flags any deletion decision that outran the pre-approval.

---

## 11. Self-review

- **No placeholders.** Every section is concrete; the one genuinely-pending piece (the W8 close ledger,
  §7) is explicitly marked "not yet written — fold in at final-review time," not left as a silent gap.
- **No invented numbers.** Counts are sourced: **51** first-parent commits `2e2dccab5..HEAD` (git,
  §2b); **0/0/0** last-review defects, **1** must-ratify / **5** W6 / **12** W8 / **~16** accept / **1**
  Go / **3** resolved (final-two-wave Duty 3); **24** protected + **13** TUI-route + **262** deleted
  files (kill-list); **18**-entry W7 ledger (self-declared, wave7-report), **250**-item W6 sweep
  (159/78/13); **2** M9 ratification items. Commit SHAs are copied from `git log`, not reconstructed.
- **Internal consistency.** The last-reviewed tip `2e2dccab5` and report commit `18e049f5f` are used
  identically in §1, §2, §11; the two ratification items map §5→§8 the same way; the deletion authority
  in §9 and the deletion duty in §10 agree on the 24-endpoint + safe-default conditions.
- **Verify-not-re-derive** is enforced structurally: §1 fences off already-audited work; §2 gives the
  re-enumeration recipe + subtract-the-reviewed-ranges rule; §3/§5/§6 are pointer tables, not
  re-analysis; §10 reads M9's S7 evidence rather than re-testing.
- **The controller layer is the audit target**, correctly identified as the only never-adversarially-
  reviewed work, with a method that survives the commits landing between now and the final review.
- **Open question for the controller** — see the return note: whether the deletion series and the W8
  merge should each get a *focused* controller-commit re-review as they land (keeping the final review's
  audit surface small), or all be audited in one pass at the end.
