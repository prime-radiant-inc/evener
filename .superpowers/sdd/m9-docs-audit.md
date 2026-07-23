# M9 Docs Audit — m9-live-e2e-plan.md + final-review-scaffold.md

**Date:** 2026-07-22 · **Auditor:** doc-review agent (read-only on code; file-write-only; no git add/commit)
**Scope:** `docs/superpowers/plans/m9-live-e2e-plan.md` (plan commit `1c7e8aa2f`) and
`docs/superpowers/plans/final-review-scaffold.md` (scaffold commit `6b5db1eb5`), audited against
`.superpowers/sdd/progress.md` (2026-07-22 entries) and `docs/superpowers/plans/m10-kill-list.md`
(§2 + Appendix D), as they stand at worktree HEAD `20170d355` (integration).

## Overall verdict: AMENDMENTS-NEEDED (6)

Both documents are well-grounded — every commit SHA spot-checked (18 total across both docs)
resolves to the exact content claimed, the scaffold's git re-enumeration recipe is syntactically
and semantically correct, the failure protocol and ratification framing are exact matches to their
governing sources, and there is no fabricated data or invented floor number anywhere. The six
amendments below are real gaps (a coverage hole, a missing falsification mechanism, two staleness
bugs against the same-day ledger, and a documentation hedge), not showstoppers — none blocks
understanding the plan's intent, but each should be fixed before the docs are treated as
execute-blind-ready.

---

## Per-checklist-item findings

### A. S7 deletion-blast-radius coverage — AMENDMENTS NEEDED (3 sub-findings)

S7 cards 1+2 (`m9-live-e2e-plan.md` §5, lines 236–244) jointly enumerate **all 24** of kill-list
§2.1's original rows exactly — verified row-by-row: card 1 (SPA-consumed, browser network layer)
covers the 12 S/S+T-visible rows, card 2 (the 13-route `hubapi.Client` contract, named exhaustively:
health/bare-session/send/tasks/interrupt/compact/clear/fork/model/spawn/spawn-schema/models/tree)
covers the 13 T-visible rows; union = all 24, zero gaps, zero invented rows. **This part is CLEAN.**
Both `/doc` routes are explicitly covered by card 6 with concrete falsify content (raw bytes, raw
mode via native pane, no dead legacy-asset links). **Also CLEAN.**

Three gaps against the Appendix-D-amended total:

1. **`/api/dirs/create` and `/api/git/head` (2 of the 3 newly-PROTECTED W6 fetch endpoints) have no
   explicit live-exercise card anywhere in the plan.** Grep confirms zero mentions of "dirs/create"
   or "git/head" in the document. `/api/search` gets a concrete card-3 treatment ("confirm palette
   search → open works live"); `dirs/create` and `git/head` are only swept up in card 3's generic
   closing sentence ("Confirm the other §1.6 orphaned endpoints were kept... not silently deleted"),
   which is the wrong bucket — per Appendix D they are no longer orphaned, they are **PROTECTED with
   real production consumers** (`preflight.ts:41`, `branch.ts:13`) and need a live functioning check
   (e.g., drive the spawn pane's directory-create + branch-HEAD auto-resolve), not a "kept, not
   deleted" presence check. Neither S1 (the spawn-pane suite) nor S7 drives these flows.
2. **The 3 genuinely-orphaned-KEPT endpoints (`/api/upgrade`, `/api/sessions/{ref}/reasoning-effort`,
   `/api/path/validate`) have no concrete falsify condition or command.** Card 3's blanket sentence
   asserts they were "kept... not silently deleted" with no verification mechanism (no curl/fetch
   command, no explicit "404 = fail" condition) — breaking the plan's own stated convention that
   "every card states its falsification condition" (§5 intro).
3. **S7's own scope line undercounts the amended total.** §5 S7 header states "the 24 protected
   shared endpoints (kill-list §2.1)" — this is the pre-Appendix-D count. The true amended total the
   suite should describe itself as covering is 24 + 3 reclassified (search/dirs-create/git-head) + 2
   doc-routes-pending-W8 = 29. The cards mostly reach for these in practice (search, doc routes) but
   the section's own framing doesn't say so.

### B. Flag fate consistency — CLEAN

`m9-live-e2e-plan.md` §1 and S7 card 4 both correctly state: M9 hubs launch with no `SERF_HUB_WEB`
set; `=new`, `=garbage`, and unset must behave identically; S7 card 4 gives a falsify condition
("any page route returns legacy HTML or differs by env"). Grepped every `SERF_HUB_WEB` occurrence in
both documents (10 total) — every one is either historical explanation ("the wave-6/7 live proofs
required `SERF_HUB_WEB=new`... that requirement is gone") or the no-op assertion. **No residual step
anywhere instructs setting `SERF_HUB_WEB=new`** for an M9 suite's own hub launch — §3's isolation
recipe never mentions the var. `final-review-scaffold.md` §10 restates the no-op correctly and
consistently.

### C. Ratification items hosted — CLEAN

Both items live in named suites exactly as the checklist requires: ask-transcript re-architecture →
S2 (`m9-live-e2e-plan.md` §7 #1), `/thread/{ref}` live composer → S3 (§7 #2), matching §5's
suite-scope descriptions and §6's report-contract carve-out ("Ratification observations (S2, S3
only)"). Framing is explicit and repeated: "Verdict is *described*, not *graded* — Jesse rules,"
"Jesse vetoes... if he prefers." `final-review-scaffold.md` §8 mirrors this exactly ("Neither is a
bug... it does not grade them") and cross-references S2→#1/S3→#2 identically. No go/no-go language
found in either doc for these two items.

### D. Isolation recipe completeness — AMENDMENT NEEDED (1)

All five required elements are present in §3: fake `$HOME` with the host-global-flock rationale
stated explicitly; distinct loopback port per suite; repo `.env` sourced via an explicit (non-bare)
path (`set -a; . ./.env; set +a`, correctly dodging the zsh bare-`. .env` gotcha since `./.env`
contains a slash); never-echo credential rule (explicit bullet); isolated browser profile (own
remote-debug port + own profile dir per suite, plus clean-profile-start hygiene against ghost
cross-origin tabs). **This part is CLEAN.**

The model-id gap is real: §3 and §9 both state `openai/gpt-5-nano` flatly as "the wave-6-close
precedent" and "the *actual* wave-close value... not invented," with **no hedge**. Grep confirms
`oai-work/gpt-5.4-mini` appears nowhere in the document. Per `progress.md`'s same delivery entry
("T5 done, dry-run verdict adjudicated, M9 docs delivered"): *"Dispatch-time note: the plan names
`openai/gpt-5-nano`; the PROVEN close recipe is `oai-work/gpt-5.4-mini`... the dispatch prompt's
recipe governs; verify the model id at dispatch."* This caveat lives only in the ledger, not the
plan document — a future reader of the plan alone would take the model id as settled. The doc needs
a one-line hedge.

### E. Failure protocol — CLEAN

§8 matches Jesse's standing rules by exact citation: `feedback_await_behavior_not_timeouts` ("await
the actual behavior, never widen a ceiling to absorb it") and `feedback_fix_flakes_root_cause`
("always fix flakes at the root") are both cited by their real memory-file names, correctly applied.
Product-vs-test classification, env-limited-is-a-finding-not-a-failure, present-but-not-visible ≠
absent, and no-silent-fixing are all present with concrete precedent citations (the W6-close
env-limited items). §6's per-suite report contract requires `PASS`/`FAIL`/`ENV-LIMITED` per card and
`PRODUCT DEFECT`/`TEST-HARNESS ARTIFACT`/`ENV-LIMITED` per finding, with root cause — fully present.

### F. Scaffold's re-enumeration recipe — AMENDMENT NEEDED (1)

**Git mechanics verified live against the actual repo — sound.** `git log --oneline --first-parent
2e2dccab5..HEAD` and the `--merges` variant are syntactically and semantically correct for the stated
purpose; `2e2dccab5` resolves and is a real ancestor of HEAD. Cross-checked the §2b snapshot table
against the live repo: at the scaffold's snapshot commit `33ba4c736`, `git log --oneline
--first-parent 2e2dccab5..33ba4c736` returns exactly **51** commits, matching the doc's claim
verbatim. All 4 merge commits the table names (`ed5057be2`, `47012b40c`, `f68e6b559`'s neighbors,
`c07d0aa2f`) and all the doc-plan/inventory SHAs (`ae3f71adb`, `d043a1b34`, `835c17e2b`, `a7b61bda8`,
`1b657aca4`) resolve to exactly the content the table describes. The "subtract reviewed-stream
ranges" refinement is not strictly load-bearing today (every wave has landed via a true `--no-ff`
merge commit, so `--first-parent` already structurally excludes wave-internal commits without a
manual subtraction step) but is a reasonable forward-looking safety net for future merges that might
not preserve that discipline — not a bug. (Confirmed 60 first-parent commits exist as of current
HEAD `20170d355`, i.e., 9 more have landed since the snapshot — exactly the kind of growth the doc's
own "will have grown" caveat anticipates.)

**The staleness bug:** §11's "Open question for the controller — ...whether the deletion series and
the W8 merge should each get a *focused* controller-commit re-review as they land... or all be
audited in one pass at the end" is presented as **unresolved**, but it was already answered in the
**same ledger entry** that delivered both docs: *"(Q1) FOCUSED per-landing re-reviews for the M10
deletion series and the W8->integration merge (keeps the final audit surface small, matches the
every-controller-commit-reviewed pattern); the final pass audits residue via the scaffold's
re-enumeration recipe."* §2's closing lines ("Still to be added **at final-review time**: the W8 →
integration merge... the M10 deletion + flag-flip series...") and §4's duty list carry the same gap
— nowhere does the scaffold state that these land with their own focused reviews as they happen, with
the final pass only confirming that occurred (rather than performing the audit itself).

### G. Internal consistency (suite count / batch / pacing cap) — AMENDMENT NEEDED (1)

Suite IDs, definitions, and cross-references are internally consistent throughout: S1–S7 defined in
§5 map identically to §4 (batch 1 = S7/S1/S2/S4 = 4, batch 2 = S3/S5/S6 = 3, 4+3=7), §6 (report
contract), and §7 (ratification homes, S2→#1/S3→#2 in both directions). **No internal-consistency
defect.**

External staleness: §4 (twice) and §9 (once) hardcode "≤ 4 heavy agents concurrent" / "within the
≤4 cap" as the boundary for controller reordering. This is now stale twice over. Per
`progress.md`, **same day**, both after the plan's commit `1c7e8aa2f`: cap raised 4→8
("`63903e4d1` — cap raised to 8 per Jesse... Memory updated"), then 8→16 ("`## 2026-07-22 — Jesse:
"at least 16 slots." Cap -> 16...` — Cap raised 8 -> 16 per Jesse (live). Memory updated."). The
current, durable cap is **16**, not 4. The plan's batch-of-4 split and its "controller may reorder
within the ≤4 cap" language would, read literally, forbid a reordering the controller is now
actually permitted to make (e.g., running more than 4 suites at once). The S7-leads-first
sequencing rationale itself is capacity-independent (a de-risking preference, not a resource
constraint) and does not need to change — only the hardcoded numeral does.

### H. Placeholder scan — CLEAN (no new findings beyond A/F)

Grepped both documents for `TBD`, `similar to suite`, `FIXME`, `XXX`, `placeholder` (case-insensitive).
The only hits are: the plan's own self-review correctly claiming "no placeholders" (true — no literal
TBD/similar-to-N string exists), an unrelated `PLACEHOLDER` dist-file reference (build step, not a
doc placeholder), and the scaffold's own self-disclosed "wave8-report.md not yet written" note (which
is explicitly and honestly flagged, not a silent gap — matches its own self-review claim). The one
functional placeholder-like gap found — S7 card 3's unspecified verification mechanism for the
orphaned-KEPT endpoints — is already captured under **A** (sub-finding 2) rather than re-listed here.

---

## Amendment list

| # | Doc | Section | One-line fix |
|---|---|---|---|
| 1 | `m9-live-e2e-plan.md` | §5, S7 (card 3 + surfaces line) | Add an explicit live-exercise sub-check for `/api/dirs/create` and `/api/git/head` (drive the spawn pane's dir-create + branch-HEAD auto-resolve) with a falsify condition, matching the `/api/search` treatment — don't lump them into the generic "kept" sentence for genuinely-orphaned endpoints. |
| 2 | `m9-live-e2e-plan.md` | §5, S7 card 3 | Give the 3 orphaned-KEPT endpoints (`/api/upgrade`, `reasoning-effort`, `/api/path/validate`) a concrete falsify condition/command (e.g., direct fetch each route, assert non-404), not just a bare assertion they were "kept." |
| 3 | `m9-live-e2e-plan.md` | §5, S7 intro ("Surfaces:") | Update "the 24 protected shared endpoints (kill-list §2.1)" to cite the Appendix-D-amended total (29: 24 + 3 reclassified + 2 doc-routes-pending-W8) so the suite's stated scope matches what its cards actually need to cover. |
| 4 | `m9-live-e2e-plan.md` | §3 (model line) + §9 (self-review) | Add a one-line hedge that `openai/gpt-5-nano` is subject to dispatch-time verification against the then-current proven recipe (ledger already notes `oai-work/gpt-5.4-mini` as proven) — don't state the model id as unconditionally settled. |
| 5 | `m9-live-e2e-plan.md` | §4 (pacing, ×2) + §9 (self-review) | Replace the hardcoded "≤4 heavy agents concurrent" / "within the ≤4 cap" language with a reference to the controller's then-current concurrency cap (now 16, raised twice same-day after this doc was committed) rather than a pinned numeral; the S7-leads-first sequencing rationale can stay as-is. |
| 6 | `final-review-scaffold.md` | §2 (closing lines) + §4 (Duty 1) + §11 (open question) | Replace the "open question" about focused vs. single-pass controller-commit review with the controller's actual decision (Q1, already ledgered same day as both docs): the M10 deletion series and the W8→integration merge each get a focused re-review as they land; the final pass audits only that this occurred, via the re-enumeration recipe — it does not perform the audit itself. |

---

## Notable positive findings (not amendments)

- All 18 spot-checked commit SHAs across both documents (including the scaffold's full §2b table and
  the plan/scaffold/kill-list delivery commits themselves) resolve to exactly the content claimed —
  no fabricated or mismatched citations found.
- The scaffold's git re-enumeration recipe was executed live against the actual repository and
  produces exactly the commit count the document claims at its stated snapshot commit.
- Every "no invented floor numbers" self-review claim in both documents (24/13/6/5/262/17/2 in the
  plan; 51/0/0/0/1/5/12/16/1/3/24/13/262/18/250/2 in the scaffold) was cross-checked against
  `m10-kill-list.md` and `progress.md` and holds.
