# Handoff addendum — max_wait_ms orchestration, live state (2026-06-13)

Written 2026-06-13 by the Fable orchestrator session mid-flight, on news that
Fable access is being discontinued. This SUPPLEMENTS, does not replace,
`2026-06-13-session-handoff.md` (read that first — its directives, queue, and
done-means are all still binding). This note records only what that doc could
not know: the *verified* state of the in-flight max_wait_ms orchestration as of
this writing, and one hazard the next session MUST account for.

## Verified facts (checked against the tree, not trusted from agent reports)

- Branch `job-control-spec` tip is now `908ee0d4`. It ADVANCED 8 commits while
  the Track A/B implementer subagents were running (recursion/handoff docs plus
  two small code fixes: `9117af28` touches `agent/execenv/streaming_test.go`
  group-kill test; the `appwire/client.go` buffer-cap-4096 comment). **The
  worktrees branched from `3826df54` and are 8 commits behind this tip.**
- THE HAZARD: `git diff job-control-spec..wip/maxwait-a` shows those 8 commits'
  changes *in reverse* (deletions of new superpowers docs, the appwire/streaming
  edits). That is NOT the subagent editing out of scope — it is the base moving
  forward underneath the worktree. Do not "revert" those; they are already on
  the branch. `git merge-tree` of A onto the current tip = **0 conflicts**.
  But: A implemented and went green against the STALE base. Re-running full
  gates on the merged tree is mandatory, not optional — the group-kill test fix
  and appwire bump arrive only at merge.

## Track state (worktrees under `.claude/worktrees/`)

- **A — `wip/maxwait-a`, 4 commits** (`f1c3a49c` schemas, `c0cbbd54` decoders,
  `38f2b66f` complete-or-handle, `144fd1ac` model-facing sweep). Self-reported
  green (`go test ./agent/...`, lint) AGAINST THE STALE BASE. Merges cleanly per
  merge-tree. Verified on-disk: zero `"background"/"block"/block_timeout_ms` in
  `definitions.go`; all five tools carry `max_wait_ms` with the §2 descriptions;
  complete-or-handle implemented via a `settle` closure from `runShell` +
  `marshalCompleteOrHandleResult` two-layer check + `finalizeKeptSync`.
- **A OVERSTEPPED its lane**: commit `144fd1ac` also swept ~14
  `test/scenarios/*.md` cards (Track C's scope) and rewrote normative AND some
  non-normative doc regions. Harmless because C never ran — but see the gap
  below. Param-name sweep across all cards is clean (zero stray
  `background=`/`block=true`/`block_timeout_ms`).
- **A's CARD GAP (needs finishing as Track C work)**:
  `test/scenarios/job-shell-lifecycle.md` did NOT follow the spec's Track C
  instruction. Spec: DELETE the rejected-combo arm (the combo is now
  inexpressible), merge the explicit-background arm into the promotion shape,
  and ADD a complete-or-handle arm (§6.4). A instead KEPT a "rejected combo"
  arm repurposed to a negative-`max_wait_ms` error and the title still says
  "rejected combo"; no complete-or-handle arm was added. Fix before declaring
  Track C done. Also re-audit the delicate `job-list-and-recovery.md` and the
  RUNNING-state/notification fixtures (must be bound-outliving, e.g. `sleep 2`,
  per §3) — A's sweep changed params but a fixture-semantics audit was not its
  brief.
- **B — `wip/maxwait-b`, 1 commit** (`b83f44d0`). Swept the non-normative doc
  regions. Its grep classification is STALE: it "left alone" lines it labeled
  "Track A's normative bullets" — but A edited those in a different worktree, so
  B never saw A's new text. After merging A, B must rebase onto the post-A tip
  and re-grep; expect B's commit to shrink (A already did some of it). Decide at
  rebase whether B's commit still carries unique content or folds away.
- **C — `wip/maxwait-c`, 0 commits, clean**. Never dispatched (orchestrator
  stopped to handle the model-transition message first). Either dispatch a fresh
  Track C to finish the `job-shell-lifecycle` arm + fixture audit on top of
  A, or fold that remaining work into the A-merge fixup. Most cards are already
  done by A; only the gap above remains.
- **D — not started**. Haiku comprehension gate; runs after A merges. Six final
  descriptions (five tools + job_watch) from merged `definitions.go`.

## Recommended resume sequence for the next session

1. Merge A onto current `job-control-spec` (`--no-ff`). Run ALL THREE gates on
   the merged tree yourself (`make test && make lint && go test ./agent/...
   -race`). Do not trust A's stale-base green.
2. Close A's card gap (job-shell-lifecycle rejected-combo deletion +
   complete-or-handle arm) and audit the delicate cards — as a fixup commit or a
   fresh Track C sonnet on top of the merge.
3. Rebase B on post-A, re-grep, merge or drop as a no-op.
4. Run Track D (Haiku gate). Then the queue in the main handoff (dead-field
   sweep, roborev findings, head_bytes, live matrix, recursion).
5. Final grep gate: ZERO `block_timeout_ms`/`"background"`/`"block"` param
   forms in `definitions.go`, `docs/job-control.md`, `test/scenarios/`.

## Worktree cleanup

`.claude/worktrees/{a,b,c}` + branches `wip/maxwait-{a,b,c}` exist. Remove with
`git worktree remove` and `git branch -d` once each track's content has merged.

## Coordination note from the orchestrating session (appended ~02:05Z)

- The card gap you flagged is CLOSED: `61bc3d12` merged a complete Track C
  sweep (16 cards + 1, spec §3 surgery included). Your A4 card edits were
  superseded at conflict resolution by the deeper sweep — semantically a
  strict superset of A4's renames; do not re-do cards, and a grep-verify
  of A4's card claims against the tree will show the deeper versions, not
  yours. The scenarios leg of your done-criteria grep already passes.
- B↔A contract conflicts during your rebase are the DESIGNED overlap (A
  carried its own normative sections); resolve in favor of whichever text
  is spec-v3-complete per file, same rule we used for cards.
- The tip has moved twice under your worktrees (`61bc3d12` latest). Same
  hazard you already documented; same handling.

## CLOSED — orchestration complete (tip `d6218e4b`)

All four tracks are merged into local `job-control-spec` and the tree is
verified GREEN. What landed and how it was verified (every claim checked
against the tree, not trusted from agent reports):

- **A** merged at `e8423884` onto the live tip (0-conflict merge-tree, then
  `--no-ff`). Full gates re-run on the MERGED tree (not A's stale-base green):
  `make test` exit 0, `make lint` exit 0, `go test ./agent/... -race` exit 0
  (the complete-or-handle `settle` seam is race-clean against the live
  group-kill fix).
- **C** came in via the parallel session's `61bc3d12` (16 cards + the
  shell-lifecycle §3 surgery). Verified on-disk: `job-shell-lifecycle.md` title
  now says "complete-or-handle", the rejected-combo arm is GONE, and arm (f) is
  a real complete-or-handle arm (chatty→kept job_id+truncated, quiet→ephemeral,
  kept job emits NO terminal notification, full bytes via job_read_output). The
  gap I flagged is genuinely closed.
- **B** dropped as superseded. A's overstep already carried the full param
  sweep; B's commit was redundant and REGRESSIVE in the normative bullets (it
  obediently left A's lane stale-old in its own worktree). I diffed A's merged
  doc vs B's final doc, salvaged only B's genuine non-normative improvements
  (complete-or-handle in the durable-record list, the state-diagram label, two
  anti-pattern phrasings, watch-send no-wait) as `d6218e4b`, and deleted
  `wip/maxwait-b` (-D, content folded).
- **D — Haiku comprehension gate: PASS 8/8** from the six final descriptions
  alone (five tools + job_watch, extracted from merged `definitions.go`). All
  the load-bearing distinctions answered correctly: shell-0=session-default vs
  read-0=snapshot; truncated:true+job_id ⇒ kept job, read the rest via
  job_read_output; resume-inline = positive max_wait_ms; live-steer ignores the
  bound (returns on delivery); grep+positive-max_wait_ms = the one-call "wait
  for ready". No reword cycle needed.
- **Done-criteria grep CLEAN** in all three target areas (param forms only;
  `running_in_background` response field correctly retained): zero hits in
  `agent/internal/tool/definitions.go`, `docs/job-control.md`,
  `test/scenarios/`.
- Worktrees `.claude/worktrees/{a,b,c}` removed; `wip/maxwait-{a,b,c}` deleted.
  (A separate `wip/recursion-early` worktree exists — not mine, left alone.)

NOT done, correctly deferred to the existing queue (NOT part of this
orchestration's done-means): the **dead-field sweep**. Checked it: the
delegate/send decoders map `max_wait_ms` onto live `Background`/`BlockTimeoutMS`
plumbing exactly per spec §3 (still read downstream — not dead). The one real
candidate is `shellArgs.Background` at `agent/job_shell.go:119`, which the tool
layer no longer sets true; removing it touches the complete-or-handle-reshaped
background branch, so it belongs in the deliberate sweep, not a rushed merge
fixup. Also still queued (unchanged): the two roborev findings, `head_bytes`,
the live 14-card matrix, and recursion (PRI-2204). NOT pushed; Jesse merges.
