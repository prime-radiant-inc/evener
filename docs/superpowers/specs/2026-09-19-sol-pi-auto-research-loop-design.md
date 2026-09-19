# SoL-Pi auto-research loop: design

Status: approved design, pending implementation planning
Date: 2026-09-19
Branch: `sol-pi` (worktree `sol-pi`); all work lands as pull requests onto `main`
Paper: "SoL-Pi: Recursively Scaling Auto-Research Loops for Efficient Agent Harness" (arXiv 2609.20519, NVlabs)

## Summary

Build the paper's auto-research loop as a real capability of this harness: an
oracle analyzer that measures where tokens go in real session transcripts, a
scored environment pool, a paired rollout runner over headless `evener run`
sessions, tamper-proof selection gates with an append-only ledger, one-way
held-out validation, and a bundled research-loop skill that worktree-isolated
delegate lineages execute to propose, implement, review, and gate candidate
harness changes. Seed the loop's first run with the paper's four surviving
mechanisms, adapted to evener. Done means at least one mechanism (or a
loop-discovered improvement) passes held-out validation with a measured token
efficiency win on the daily-driver model and merges to `main` as a pull
request.

## Why

Token traffic is the binding cost of long-running agent work. The paper shows
that harness-layer improvements, found by a disciplined search, cut recorded
token traffic by 44.7-49.0 percent and API cost by about a third on their
harness. Evener already has most raw ingredients (headless runs with
trajectory export, token accounting, worktree-isolated delegates, a doctor
forensics suite, a cheap-model side channel). What is missing is the process
that turns those ingredients into gated, honestly-measured harness
improvements. This design builds that process and runs it once on its own
first candidates.

## Constraints

- Default tests stay deterministic and offline. No default test issues a live
  request; live rollouts run only behind `EVENER_LIVE_TESTS=1` with an
  explicit run cap.
- Live pilot budget: roughly 40 to 80 live rollouts per selection pass.
  Dev-gating runs on a cheap fast model (deepseek-4.1-flash via lunaroute);
  held-out validation runs on the daily driver (glm-5.3-vision).
- Nothing lands on `main` except by pull request. The `sol-pi` branch holds
  the work; each spine slice and each surviving mechanism is its own PR.
- Disk is at 95 percent: run artifacts live in scratch directories, pruned
  each pass; lineage worktrees are disposed on merge.

## Architecture

New subcommand family on the existing dev-tools CLI: `evener-dev research
...` (same home as agentshards and modulelint). Five components:

1. **Oracle analyzer** (`research oracle`). Walks a state directory of real
   session transcripts and their `api.jsonl` files and measures where input
   tokens went: adjacent edit-then-command round trips and the token cost of
   each intervening request; bytes of large tool observations re-sent across
   requests before masking or compaction; build and test log volumes from a
   declared command set; compaction timing versus context window pressure.
   Emits an append-only `oracle-ledger.json` plus a human summary ranking
   candidate mechanisms by projected savings on the measured workload.
   Offline; defaults to a bounded sample of recent sessions. The report is
   aggregates only.
2. **Environment pool** (`research/environments/`). Verifier-driven task
   directories in the paper's synthetic style: a task prompt, a small
   checked-in fixture repository to work in, and a `verify.sh` script that
   scores the finished work. All local, no network. The committed criteria
   file names the dev set (about 10 environments) and the held-out set (about
   4). Held-out environment directories live outside the repository, in a
   private directory Jesse owns; the validate command takes a
   `--heldout-dir` flag. A worktree-confined lineage cannot read what its
   worktree cannot see, so the isolation is enforced by the sandbox, not by
   convention.
3. **Rollout runner** (`research rollout`). Per run: copy the environment's
   fixture repository to a fresh scratch working directory, execute a
   headless session via the existing `evener run` (with `--export-atif`, a
   max-rounds cap, model and flags from the base or candidate config), run
   the verifier, append a run row to the ledger. Base and candidate runs
   interleave. Provider failures (rate limits, stream cuts) are marked
   infra-failed and excluded from gates rather than counted as task
   failures. A `--max-live-runs` cap keeps each pass inside the pilot
   budget.
4. **Gates and ledger** (`research gate`, `research freeze`,
   `research validate`). Criteria live in a committed `research/criteria.toml`
   fixed before any experiment runs. The ledger is append-only JSONL in a run
   directory, never committed, with hash-chained rows so silent edits are
   detectable. Freeze records the candidate's git SHA, its config delta, its
   dev-gate verdict, and a hash of the criteria file; validate re-derives
   the criteria hash and refuses to run if it changed, then runs the frozen
   candidate on held-out environments on the daily driver. A failed
   validation permanently rejects that frozen SHA. Validation results never
   feed back into search.
5. **Research-loop skill** (`internal/bundled/skills/auto-research/`). The
   shared lineage template: read the oracle ledger, propose a concrete
   harness change that addresses measured overhead, implement it, pass an
   independent reviewer, run the fixed dev experiment, analyze trajectories,
   apply the gates, record everything. Its rules forbid editing the criteria
   file, running held-out environments in dev, and rewriting ledger rows. A
   lineage is a worktree-isolated delegate executing this skill with a
   candidate brief.

## Data flow

Transcripts and `api.jsonl` feed the oracle. The oracle's ledger seeds and
ranks candidates. Each candidate plus the dev environment pool feeds the
runner. ATIF trajectories and verifier results become ledger rows. The gate
consumes rows and emits verdicts. Frozen candidates go to one-way held-out
validation. Survivors merge as pull requests onto `main`.

## Selection core

- **Capability gate**: the candidate's task success rate on the dev set must
  not fall more than a declared tolerance (default 10 percentage points)
  below the base pair, and the candidate branch must pass the full repo
  gates (`make lint`, `make vet`, `make test`) as a binary floor.
- **Efficiency gate**: at least one declared metric must improve by its
  threshold. Declared metrics, computed from recorded usage and the pricing
  data in `llm/pricing.go`: recorded input tokens, output tokens, API
  request count, estimated cost. Efficiency is computed over environments
  where both arms succeeded, pooled, and capability is checked first.
- **Retention**: among candidates passing both gates, only nondominated
  (Pareto) results under the declared metrics are retained.
- **Honesty at small N**: dev-gating at two repetitions per environment is
  noisy. Thresholds go below the oracle-projected effect size and above
  expected noise; a candidate landing near an edge gets more repetitions
  before a verdict, not a lucky pass. The one-way held-out run on the daily
  driver is the backstop.

## Candidate mechanisms

Implementation order comes from the oracle ledger, not from prior
confidence. Each ships behind a per-session config flag, off by default
until it wins held-out validation.

1. **Action Fusion**. The file-mutation tools (edit_file, write_file,
   apply_patch) gain an optional `run_after` parameter. The command executes
   inside the same tool call after the mutation applies, and its output
   returns in the same observation. An edit-then-test cycle drops from three
   requests to two. The model chooses which commands to fuse; commands that
   must inspect the mutation result first stay separate.
2. **Checkpoint compaction reminder**. At task-list step completion
   boundaries, the harness injects a steering reminder that compaction is
   available and cheap right now; the agent elects compaction through its
   existing `compact_context` tool. The harness never forces compaction. The
   cost gate lives in the reminder: it is injected only when projected
   input savings, estimated from the observed request rate between
   completed steps and the remaining step count, beat the prompt-cache
   rewrite cost computed from the cache-write token accounting and pricing
   data in `llm/pricing.go`. Later reminders demand a larger margin.
3. **ObservationPack (conditional build)**. The shell tool already digests
   large retained output, and the obs_mask strategy masks old observations
   at compaction, so evener may already truncate well. The oracle measures
   the residual: bytes of large observations actually re-sent across
   requests. Build only if the measured headroom is real. If built: results
   over 10 KiB are archived to session artifacts; the next two requests see
   the full result; later requests see a stable handle plus original size
   plus head and tail excerpt lines, retrievable on demand via the handle,
   reusing the artifact-read machinery.
4. **Evidence-Preserving Reducer**. Build and test log observations of 4
   KiB and up (from a declared command set; file reads and search results
   bypass) are archived, and a receipt is extracted by the configured cheap
   model (the `--fast-cheap-model` side channel already exists). A
   deterministic verifier checks schema, source hash, exit status, that
   quoted lines appear in the source, and that the receipt is actually
   smaller. Any failure, suspected credentials, or no size reduction falls
   back to the original log. ObservationPack recognizes receipts and skips
   them.

## Who implements what

The spine (oracle, runner, gates, ledger, skill) is built directly on the
`sol-pi` branch. The four mechanisms then run through the loop as the paper
intends: each is a worktree-isolated lineage, a delegate with the
auto-research skill, that implements its mechanism, passes an independent
reviewer, is dev-gated, frozen, and held-out validated. The session stays
integrator and merge reviewer.

## Testing

TDD throughout, per repo rules. The scripted provider covers mechanism
behavior at the LLM boundary; the reducer's verifier is pure code; the
runner and gates are tested against two tiny committed smoke environments
with no network; freeze-protocol tests pin the criteria-hash refusal and
permanent rejection of failed validation. `make lint`, `make vet`, and
`make test` stay green on every slice and act as each candidate's capability
floor.

## Delivery plan

Pull requests onto `main`, each slice independently reviewable:

1. Oracle analyzer plus smoke environments plus offline runner mechanics.
2. Gates, ledger, freeze and validate commands.
3. The auto-research skill and its lineage rules.
4. One PR per surviving mechanism (expected: Action Fusion and the
   checkpoint reminder first, subject to the oracle's ranking).
5. The live pilot report: ledger numbers, held-out results, and what the
   loop found.

Phase 0 of the first run is a stop-or-go checkpoint: if the oracle's
best-ranked mechanism projects under 5 percent input-token savings on the
daily-driver corpus, we stop and report before building the spine.

## Risks

- The paper's savings may not replicate on evener workloads at this scale.
  The oracle-first rule and the stop-or-go checkpoint are the guard.
- Small N makes dev-gating noisy. More repetitions near edges; held-out
  validation on the daily driver is the backstop.
- lunaroute rate-limits under concurrency (observed during this design
  session). Paired runs serialize when needed; infra failures are excluded
  from gates.
- Disk pressure (95 percent). Scratch-resident artifacts, pruned per pass;
  lanes disposed on merge.
- The oracle reads Jesse's real transcripts. It runs offline on this
  machine and emits aggregates only; nothing is committed and nothing
  leaves the box.
