# manage_worktree — live ergonomics findings (2026-07-03)

Ran the five worktree scenario cards against real models to judge how the tool
*feels* to an agent and how comprehensible it is across capability tiers.

**Tiers exercised:** `kimi/kimi-for-coding` (strong) and `openai/gpt-5.4-mini`
(weak/small). `lunaroute/glm-5.2-nvfp4` was intended as a third tier but the
gateway returned `status=500 An internal error occurred` on every call — a
provider outage, unrelated to the tool; GLM contributed no data.

**Method:** each run is a hermetic temp git repo + isolated `SERF_STATE_DIR`
(config symlinked from `~/.serf`, mutable state separate), driven
non-interactively. Assertions cross-checked against on-disk truth (sidecars,
`git worktree list`, leftover branches), not just the agent's claims.

## Verdicts

| Scenario | kimi (strong) | gpt-5.4-mini (weak) |
|---|---|---|
| S1 create-and-orient | ✅ pass | ✅ pass |
| S2 lifecycle merge-back | ✅ pass | ✅ pass (kept branch) |
| S3 list-and-cleanup | ✅ pass (via remove+force) | ⚠️ partial — correct reasoning, no data loss, cleanup blocked by ownership guard |
| S4 cold-discovery (tool-agnostic prompt) | ✅ pass | ✅ pass |
| S5 error-legibility | forced past ownership, silently discarded a dirty edit | ✅ stopped and reported options |
| S5b dirty-refusal (isolated) | ✅ read the refusal, correctly declined force | — |

## Strengths (keep these)

1. **Discoverability is strong.** In S4 the prompt never says "worktree",
   "branch", or names a tool — just "set me up a clean, separate place ... that
   won't touch my current working copy." Both tiers reached for
   `manage_worktree create`; neither did `cp -r`, `git stash`, or a
   branch-in-place. The description's framing ("a scratch lane to try something
   that might not pan out") sells the tool from need alone.
2. **Result messages are the tool's biggest asset.** `create`/`exit`/`remove`
   each return a string that states what happened, where the agent now is, and
   the way back — e.g. *"Created and entered worktree ... Subsequent tools
   operate inside it; use manage_worktree exit to return to the main
   checkout."* Both models parroted these facts back accurately and oriented
   correctly.
3. **branch = name is understood immediately** by both tiers — the param doc
   "also used as the branch name" earns its keep.
4. **Isolation semantics land.** In S2 both models added `Greet()` in the lane,
   committed, exited, and correctly confirmed `Greet()` is absent in the main
   checkout.
5. **The refusal messages that DO fire are excellent.** The merge-gate refusal
   ("branch is not merged ... neither ancestry nor patch-equivalence holds;
   merge first or re-invoke with force") — kimi read it and worked around it.
   The dirty refusal ("has uncommitted changes (use force to remove anyway):
   M main.go") — kimi read it and correctly declined to force.

## Findings (ranked)

### F1 — Cross-session ownership guard is the dominant friction in cleanup [design decision, Jesse's call]

The natural "come back later and clean up worktrees" flow is *always*
cross-session (the cleanup session ≠ the creating session). So `remove`
reliably refuses: *"created by a different session (01K…); refusing without
force."* Seen in **both S3 and S5, both models.** Consequences:
- It trains models toward `force` (kimi reached for `force:true` immediately in
  S3 and S5).
- The weak model (mini) treats the refusal as a hard stop and leaves the lane
  uncleaned (S3: correct reasoning, incomplete cleanup).
- The *right* verb — `prune` — sidesteps this entirely (prune gates on the
  occupancy lock, not creator identity), but no model reached for it (see F2).

**Options:** point the refusal at prune ("...or use `prune` to collect
unowned unchanged lanes"); or don't apply the ownership guard to an *unlocked*
lane (a dead session's lane is exactly what cleanup targets).

### F2 — `prune`'s description undersells it; models never use it [one-line fix, safe]

Current: *"prune (clean up stale worktree registrations)."* prune is actually
the bulk-cleanup verb — it removes unchanged/merged lanes regardless of
creator. Because the description sounds like a narrow git-plumbing chore, both
models hand-rolled cleanup with `list`+`remove` and hit F1's guard. **No run
used prune.** Reword toward: *"prune (remove worktrees that have no unmerged
work — unchanged or already-merged lanes, including ones left by finished
sessions)."*

### F3 — `remove` guard ordering masks a dirty tree behind ownership [data-loss-adjacent, Jesse's call]

In `remove`, the ownership check (refuse-without-force) runs *before* the
dirty-tree check (refuse-without-force), and a single `force` overrides both.
So when an agent forces past ownership — the common cross-session case — it
**never sees the dirty-files warning** and can silently discard uncommitted
work. This is exactly what kimi did in S5: it force-removed `busy-lane` (which
had an uncommitted edit) to get past the ownership refusal and discarded the
edit, never warned. The dirty message itself is excellent (S5b proves models
respect it) — it's just masked. **Options:** check dirtiness before ownership;
or require the agent to have seen the dirty refusal before `force` clears it;
or split `force` (ownership) from `force_dirty` (uncommitted work).

### F4 — `list` result message is a bare count; the rich data is under-advertised [one-line fix, safe]

The `list` result carries a superb structured `entries` array (per lane:
`ahead_commits`, `dirty`, `merged`, `merged_arm`, `locked`, `prunable`,
`creator_session`, …). mini read it and reasoned perfectly with no shell-out.
But the human-readable `message` is only *"2 managed worktree(s)."* — so kimi,
reading the message, judged list uninformative and shelled out to `git log`
per lane to rediscover what the result already contained. Put a
one-line-per-lane summary in the message (name · ahead · dirty · merged) so
models that don't parse the data array still get the signal.

### F5 — weak model fills every optional arg [informational, no action]

gpt-5.4-mini emits every optional parameter on every call (`base_ref`, `path`,
`force`, `delete_branch`, even empty strings) plus a `purpose`. Harmless (the
tool tolerates unknown/empty args) but notable: the small model treats the
schema as "populate everything." Not worth changing unless it causes friction
elsewhere.

## Post-fix validation (F2 + F4)

Re-ran S3 on both tiers with the reworded `prune` description and the
one-line-per-lane `list` summary:

- **gpt-5.4-mini** (the tier that leans on the tool's own text): read the new
  summary (`"untouched-lane (0 ahead, clean, merged); work-lane (1 ahead,
  clean, unmerged)"`) with **no shell-out**, then reached for **`prune`** —
  removing the empty lane and skipping the one with work in one call, cleanly
  sidestepping the ownership guard that blocked it before. Went from "partial,
  blocked" to "correct and complete." Both fixes fired as intended.
- **kimi**: still verified via raw `git log`/`git rev-list` and drove
  `remove`+`force`. The strong model is capable enough to hand-roll regardless
  and didn't adopt either change — a fair datapoint that F2/F4 help the weaker
  tier most. Correct end state either way.

Takeaway: the description and result-message wording move the *weaker* model's
behavior materially; the stronger model succeeds through capability and is
less sensitive to it. Both now reach the right end state; the small model's
*path* is much cleaner.

## Reproduction

Cards: `test/scenarios/worktree-*.md`. Harness + transcripts under
`/tmp/wt-scen-run1/` (`run.sh`, `matrix.sh`, `inspect.py`, `analyze.sh`).
Models: `kimi/kimi-for-coding`, `openai/gpt-5.4-mini`.
