# Session handoff — max_wait campaign in flight, recursion queued behind it

> ## ⮕ CURRENT STATE — 2026-06-13 ~05:30Z (post-consolidation; supersedes the body below where they conflict)
>
> **One branch, one checkout now.** Per Jesse: job control led into recursion; it is ONE project on `job-control-spec` in the MAIN checkout. No more per-task worktrees — implementers commit to `job-control-spec` directly (serialized; orchestrator hands-off while one runs). HEAD = `dde9bb09`.
>
> **DONE + merged + verified green on the consolidated tree** (`make test` 77 ok, `make lint` 0, `-race` clean, production dark — `delegation_allowance` in no tool schema):
> - max_wait_ms unification (all 4 tracks), head_bytes on `job_read_output`, the two roborev contradiction fixes.
> - Recursion **T1–8** (dark early tranche) merged at `dde9bb09` — `delegation_allowance` carrier, dormant `tree_counter`, the deaf-coordinator drive-down headline test (`agent/job_delegate_drivedown_test.go`, `t.Skip`-tracked RED until T14), seam flips 1–6. All gated by allowance=0 default ⇒ behavior-neutral at the tool surface.
>
> **IN FLIGHT:** the live 14-card e2e matrix is running on fresh `dde9bb09` binaries (hub up at 127.0.0.1:9180, smoke-verified SMOKE2_OK; ledger `/tmp/e2e-matrix-final.md`; sonnet runner, sequential). This is the gate before recursion T9–18.
>
> **NEXT (the goal):** matrix green → commit ledger+addendum → **recursion T9–18** on this branch (plan `docs/superpowers/plans/2026-06-13-recursive-subagents.md`, execution brief `2026-06-13-recursion-EXECUTION.md`; spec v3 SIGNED OFF; six open questions RESOLVED in the plan head, Jesse-vetoable). T14 = deaf-coordinator drive-down (unskip the headline, make it green) is the hard core. Then live coordinator e2e cards, then roborev sweep (#8).
>
> **DEFERRED, behavior-neutral, NOT a gate:** the dead-field sweep (`shellArgs.Background`, task #11). It is write-never in production. A 2026-06-13 attempt found `TestRunShellBackgroundCloseDuringStartDoesNotCommitJob` is coupled to the dead immediate-Background path — converting it makes `jm.close()` block (fake's Wait never returns; abandonment runs before `setShellSignal`). **OPEN QUESTION for Jesse:** is that a real shutdown-edge bug or a fake artifact? Removing the field needs that test redesigned or deleted (deletion needs Jesse approval). Do NOT rush it; it doesn't block the matrix or recursion.
>
> **Autonomy contract (Jesse, 2026-06-13):** drive to recursion-done solo; STOP and surface only for: a real product regression needing a non-trivial fix; a recursion design wall (esp. T14); anything wanting a test deleted / assertion weakened; a spec ambiguity the plan doesn't cover. Linear is IGNORED (Jesse's call).

Written 2026-06-13 ~00:50Z by the outgoing session (model transition; Opus
takes over). This is the cross-model substitute for the outgoing session's
working memory. Read fully; verify state against `git log` and the artifacts
before acting — background agents may have advanced or died since this commit.

## Standing directives from Jesse (verbatim intent, binding)

- **Zero back-compat, no temporary shims.** Good EVENTUAL architecture and
  code is the goal. Delete dead/write-only fields the boundary changes orphan.
- **Token-efficient implementation**: opus coordinates only, sonnet
  implements, haiku gates comprehension; pointed prompts; targeted gates per
  task, full gates at merges; ONE live matrix run on the final surface.
- **Never push** `job-control-spec`; Jesse merges himself. Branch is many
  commits ahead of origin — that is intentional.
- Recursion spec v3 is **SIGNED OFF** (Jesse, 2026-06-13, in another chat).
- TDD red-first, no fake-green, never weaken a card/assertion; amendments to
  current-designed-behavior are authorized with root cause cited (precedent
  5d0af135, reaffirmed for this campaign).

## What is in flight RIGHT NOW (state may have moved)

An opus orchestrator (with sonnet implementers in worktrees) is implementing
`docs/superpowers/specs/2026-06-13-max-wait-unification.md` (**v3** — read it
whole; §0.6 complete-or-handle is load-bearing and was added late). If the
orchestrating session died, its agents died with it; the WORK survives in:

- `wip/maxwait-a` (Track A: schemas/handlers/complete-or-handle — first commit
  landed: all five schemas converted)
- `wip/maxwait-b` (Track B: contract sweep — committed in worktree)
- `wip/maxwait-c` (+ possibly c1..c4 slices; Track C: ~19 scenario cards)
- Worktrees under `.claude/worktrees/`; check `git worktree list`.

**Resume protocol:** assess each wip branch (`git log job-control-spec..wip/…`,
`git diff --stat`); finish incomplete tracks with fresh sonnet implementers
(track definitions are in the spec's §4 sweep inventory; the full orchestrator
brief is reproducible from spec v3 + this doc); merge SEQUENTIALLY A → B → C
with full gates per merge (`PATH="$HOME/go/bin:$PATH" make test && make lint
&& go test ./agent/... -race`); B rebases on post-A before merging (A carries
normative contract sections). If the main tree is mid-merge/conflicted from a
killed orchestrator: `git status`, `git merge --abort`, re-merge cleanly.
Track D (Haiku comprehension gate, six descriptions, one reword cycle allowed)
runs after A merges. Done = grep-verify ZERO hits for the old API forms —
`block_timeout_ms`, `"background"` / `background true|false|=true|=false`,
`"block"` / `block true|=true` — in `agent/internal/tool/definitions.go`,
`docs/job-control.md`, `test/scenarios/`. Bare-word `background`/`block` in
unrelated prose (UI "blocks", CSP, background pollers) is NOT a failure; the
gate is the API vocabulary, per the max_wait spec's sweep list.
(docs/superpowers/** are immutable history — never edit those.)

## The queue after max_wait merges (in order)

1. **Independent verification** — never trust implementer claims: re-run all
   three gates yourself; grep-verify each claimed change against the tree.
2. **Dead-field sweep** (Jesse's good-architecture directive): spec §3 let
   internal structs "keep fields as plumbing" — delete any now write-only
   (candidates: `shellArgs.Background`, delegate/send `Background` plumbing,
   `defaultJobBlockTimeoutMS` if a track missed it). Cleanup commit.
2b. **Two deferred roborev findings** (in track-owned files, fix post-merge):
   the caller-notification-delivery card's HEADER still says "exactly ONE
   rendered current frame", contradicting its amended Run 2 (latest-per-
   delivery-boundary); and contract `:989`'s "not full output" contradicts
   complete excerpts ("Complete output below.") — both one-line rewrites.
3. **`head_bytes` on `job_read_output`** (punch item, do BEFORE the matrix so
   one live run covers the final surface): symmetric with `tail_bytes`; kills
   the secret that `grep` is the only window into a shell job's non-tail
   bytes. TDD; schema + handler + contract in one commit; no schema
   min/max/default keywords (the strict-zero rule).
4. **Full live 14-card e2e matrix** on fresh builds. Recipe + gotchas:
   `docs/agentic-testing.md`, plus hard-won rules in
   `docs/superpowers/research/2026-06-12-e2e-rerun-addendum.md` and the memory
   file: spawn model `openai/gpt-5.5` (OAuth `openai`, never `oai-work`); NEVER
   `--state-dir`/`SERF_STATE_DIR`; card command text VERBATIM in spawn prompts
   (quote repositioning caused a false FAIL); NEVER `pgrep -f`/`pkill -f`
   (self-match killed a shell and forged a phantom orphan); sonnet runner,
   sequential, ledger after every card; runner never kills the hub.
5. **Recursion execution** (PRI-2204): plan is committed and reviewed —
   `docs/superpowers/plans/2026-06-13-recursive-subagents.md`, 18 tasks, all
   six §1 seams and the §9 matrix mapped; its six open questions are RESOLVED
   in the plan head (orchestrating-session decisions, Jesse-vetoable). Method:
   opus orchestrator + sonnet implementers, worktrees, headline deaf-
   coordinator red first (skip-then-unskip WITH captured red evidence),
   contract §8 amendments ride their code commits, phases gate green, then
   author + live-run coordinator e2e cards. Estimate: one focused overnight.

## Queued Linear updates (Linear MCP was down; post when it returns)

- **PRI-2204** → state In Dev. Comment: spec v3 signed off by Jesse
  2026-06-13; plan committed (2026-06-13-recursive-subagents.md) with six
  open questions resolved in-plan; execution sequenced behind the max_wait_ms
  surface unification (spec 2026-06-13-max-wait-unification.md v3).
- **PRI-2206** comment: two punch items from the knob audit — (1) `head_bytes`
  on `job_read_output` (scheduled this campaign, before the matrix re-run);
  (2) caller-delivery two-keys (direction note committed at
  `docs/superpowers/specs/2026-06-13-caller-delivery-two-keys-note.md`,
  trigger: coordinator-card authoring). Audit extras, no action queued:
  `max_runtime_ms` doubling as wait bound on buffered envs (fixed in Track A
  text), `target`/`send.to` alias-subset asymmetry, `agent_type` as a
  capability surface (recursion-era, pairs with delegation_allowance).

## Loose ends and context a fresh session would lack

- **roborev** post-commit hook auto-reviews every commit; tonight produced
  many. Sweep the queue (`roborev` CLI / roborev-fix skill) before calling the
  campaign done.
- The plan-writer used `--no-verify` (self-reported); materially a no-op — the
  repo has NO pre-commit hook, only roborev's post-commit, which ran anyway.
- An `appwire` notification-buffer flake was root-caused and fixed at 4096
  (`bfc8f9fc`) — the loud-failure overflow guard was deliberately KEPT; the
  unbounded-mailbox redesign is explicitly NOT wanted without its own
  conversation.
- Goal-engine v2 direction note exists
  (`2026-06-12-goal-engine-v2-design-note.md`); its incident narrative still
  needs Jesse — no record exists anywhere else.
- The e2e scratch dirs `/tmp/serf-e2e-*` accumulate; `rm -rf` is hook-blocked
  on this host — leave them or ask Jesse.
- Session memory file (richer history + the same key facts):
  `~/.claude/projects/-home-jesse-git-prime-radiant-serf/memory/serf-handoff-state-2026-06-12.md`.

## Done-means for the whole arc (unchanged)

max_wait merged + verified + matrix green 14/14 on final surface (ledger
committed) → recursion plan executed: §9 matrix green, §8 amendments landed
with code, coordinator cards pass live, §10 dark rollout with the counter
bind disclosed → all of it unpushed, Linear updated, roborev swept — then
Jesse merges.
