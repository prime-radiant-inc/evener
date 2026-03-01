# Rigorous Benchmark Infrastructure Design

> **Status**: DRAFT — awaiting approval
> **Date**: 2026-02-28
> **Reviewed by**: Design review subagent, Ops review subagent (28 issues identified, all addressed)

## Problem

Our benchmarking is ad-hoc. We SSH into flower-garden, run harbor, rsync results
locally, write throwaway scripts to analyze them. There's no single place that ties
together: the exact code that ran, every LLM call it made, every subagent trajectory,
the verifier results, and the artifacts the agent produced. We can't reproduce a run
or prove what happened.

## Goals

- **Publishable rigor**: Statistical significance, reproducibility, provable results
- **Full trajectory capture**: Every session (parent + all subagents), every LLM call, every artifact
- **Exact provenance**: Tie every run to a specific git SHA, with the actual binary and prompts stored
- **Multi-agent**: Support running different agents (serf, codex-cli, others) against the same suite
- **Multi-suite**: Support different eval suites (terminal-bench, SWE-bench, custom), same archive structure
- **magic-kingdom**: New physical server replaces flower-garden for all benchmarking

## Decisions

1. **Structured Archive** — no database, no cloud services. The archive IS the database.
   Files organized so any future tool can consume them. Can always build DB/CXDB on top later.
2. **Run-first organization** — a run is one invocation against one suite. Tasks nest inside runs.
   Multiple reps are `rep-1/`, `rep-2/`, etc. inside each task directory.
3. **Store actual binaries** — ~20MB per run is nothing. Exact reproduction without rebuild.
4. **Agent is a first-class entity** — stored in the archive with prompts, skills, adapter code.
5. **Model metadata is extensible** — provider, name, reasoning_effort, plus `extra` escape hatch.
6. **tmux for execution** — harbor runs in named tmux sessions on magic-kingdom. Survives
   disconnects, supports reattach. Session naming prevents collisions.
7. **Adapter-based artifact extraction** — harbor has no post-verification hook. The adapter's
   `run()` override with `try/finally` is the only reliable extraction point.
8. **Atomic archive writes** — collect to staging directory, atomic rename on success.
   Crash at any point leaves the archive in a consistent state.
9. **Clean tree required by default** — `--require-clean` is the default; `--allow-dirty`
   opts in to running from a dirty working tree (stores `git diff HEAD` alongside snapshot).

---

## Archive Structure

```
/data/serf-evals/
└── runs/
    └── 2026-02-28T234024Z_full-53-reviewer_9bf4057/
        ├── manifest.json              # Complete run metadata (written at start, updated at end)
        ├── summary.json               # Machine-readable rollup (written during collect)
        ├── report.html                # Static HTML report (generated post-collect)
        ├── agent/                     # Snapshot of what actually ran
        │   ├── serf-linux-amd64       # The actual binary
        │   ├── install-serf.sh.j2     # Container setup template
        │   ├── prompts/               # Embedded prompts at this SHA
        │   │   ├── base.md
        │   │   ├── system.openai.md
        │   │   └── subagent_base.md
        │   ├── agents/                # Agent role definitions
        │   │   ├── reviewer.md
        │   │   ├── implementer.md
        │   │   ├── test-engineer.md
        │   │   └── explorer.md
        │   ├── skills/                # Embedded skills
        │   │   ├── ops-task/SKILL.md
        │   │   ├── test-driven-development/SKILL.md
        │   │   └── ...
        │   ├── adapter.py             # Harbor adapter that launched the agent
        │   └── git-diff.patch         # Only present if --allow-dirty was used
        └── tasks/
            ├── build-cython-ext/
            │   ├── rep-1/
            │   │   ├── reward.txt             # 1.0 or 0.0 (authoritative)
            │   │   ├── harbor-result.json     # Harbor's per-trial result object
            │   │   ├── verifier-stdout.txt    # Verifier test output
            │   │   ├── agent-stdout.txt       # Agent process stdout
            │   │   ├── api.jsonl              # Every LLM call with full raw responses
            │   │   ├── failure_category.txt   # timeout | wrong_answer | no_submit | api_error (derived once during collect)
            │   │   ├── sessions/
            │   │   │   ├── <main>.transcript.jsonl
            │   │   │   ├── <implementer>.transcript.jsonl
            │   │   │   ├── <reviewer>.transcript.jsonl
            │   │   │   └── ...
            │   │   └── artifacts/             # Filtered files the agent created in /app
            │   └── rep-2/
            │       └── ...
            ├── configure-git-webserver/
            │   └── rep-1/
            │       └── ...
            └── ...
```

---

## Manifest Schema

The manifest is written twice: at launch (with `status: "running"`, no `results`) and
updated after collection (with `status: "complete"` and `results` populated). If a run
crashes, the manifest shows `status: "running"` or `status: "collecting"`, making it
easy to distinguish incomplete from complete runs.

```json
{
  "schema_version": 1,
  "run_id": "2026-02-28T234024Z_full-53-reviewer_9bf4057",
  "status": "complete",
  "job_name": "full-53-reviewer",
  "suite": "terminal-bench@2.0",
  "machine": "magic-kingdom",
  "started_at": "2026-02-28T23:40:24Z",
  "completed_at": "2026-03-01T01:15:33Z",
  "reps": 1,
  "concurrency": 4,

  "agent": {
    "name": "serf",
    "git_sha": "9bf4057",
    "git_branch": "main",
    "git_dirty": false,
    "build_time": "2026-02-28T23:40:00Z",
    "adapter": "serf_agent:SerfAgent",
    "kwargs": {
      "enable_reviewer_gate": true,
      "max_rounds": 100
    }
  },

  "model": {
    "provider": "openai",
    "name": "gpt-5.3-codex",
    "reasoning_effort": "xhigh",
    "extra": {}
  },

  "environment": {
    "harbor_version": "0.1.44",
    "docker_image": "terminal-bench:2.0@sha256:abc123...",
    "dataset_commit": "e4f5a6b",
    "python_version": "3.13.2"
  },

  "rep_mapping": {
    "build-cython-ext": {
      "rep-1": "Qi4Hivi",
      "rep-2": "n8pPrwp"
    }
  },

  "results": {
    "task_count": 89,
    "pass_count_majority": 52,
    "pass_count_strict": 48,
    "pass_count_any": 55,
    "pass_rate_majority": 0.584,
    "pass_rate_strict": 0.539,
    "pass_rate_any": 0.618
  }
}
```

### Rep numbering

Reps are numbered deterministically: harbor trial hashes for the same task are sorted
lexicographically, and rep numbers are assigned in sorted order. The `rep_mapping` field
preserves the mapping from rep number to harbor trial hash, so analysis is always
reproducible regardless of completion order.

### Status lifecycle

```
running → collecting → complete
running → failed        (harbor crashed, never collected)
collecting → failed     (collect crashed mid-way)
```

The `--collect-only` and `--resume` commands check status before proceeding.

---

## Orchestration Wrapper

A single `tools/run-eval.sh` replaces `eval-task.sh` and `check-eval.sh`.

### Lifecycle

```
 1. Preflight     → verify clean git tree (or --allow-dirty), check magic-kingdom reachable
 2. Build         → cross-compile serf with ldflags (skip with --no-build)
 3. Snapshot      → copy prompts, agents, skills, adapter, install-serf.sh.j2 from repo
 4. Create run    → create run directory on magic-kingdom, write manifest (status: running)
 5. Deploy        → scp binary + adapter to magic-kingdom
 6. Launch        → SSH into magic-kingdom, create tmux session, run harbor inside it
 7. Monitor       → poll reward.txt files via SSH, check harbor liveness, print progress
 8. Collect       → tools/collect-run.sh: rsync to staging dir, normalize, atomic rename
 9. Summarize     → generate summary.json from reward.txt files, update manifest (status: complete)
10. Report        → generate report.html from transcripts + api logs + results
```

### Usage

```bash
# Full suite, 3 reps (requires clean git tree by default)
./tools/run-eval.sh --job reviewer-v4 --model openai/gpt-5.3-codex \
  --reps 3 --concurrency 4 --ak enable_reviewer_gate=true

# Single task, quick iteration
./tools/run-eval.sh --job test-cython --task build-cython-ext \
  --model openai/gpt-5.3-codex --reps 5

# Different agent entirely
./tools/run-eval.sh --job codex-baseline --adapter codex_agent:CodexAgent \
  --model openai/gpt-5.3-codex --no-build

# Just collect and report (run already finished on magic-kingdom)
./tools/run-eval.sh --job reviewer-v4 --collect-only

# Watch a running job
./tools/run-eval.sh --job reviewer-v4 --status

# Resume a crashed run
./tools/run-eval.sh --job reviewer-v4 --resume

# Allow running from dirty tree (stores git diff)
./tools/run-eval.sh --job quick-test --allow-dirty --task build-cython-ext
```

### tmux session management

The wrapper creates a named tmux session `eval-<job-name>`.

**Collision prevention**: Before launching, check `tmux has-session -t eval-<job-name>`.
If it exists, refuse to launch and print the existing session's status. Use `--force` to
kill the existing session and start fresh.

**Harbor as primary command**: The tmux session runs harbor directly (not inside a nested
shell), so when harbor exits, the session shows its exit status.

**Zombie cleanup**: After successful collection, `tmux kill-session -t eval-<job-name>`.
The `--status` command lists active eval sessions and reports which have a live child
process vs. which are dead shells.

### Monitoring and liveness

The poll loop checks two things every 30 seconds:

1. **Progress**: Count `*/verifier/reward.txt` files in harbor's job directory. This is
   more robust than parsing `result.json` (which can be partially written).

2. **Liveness**: Check if harbor is still running:
   ```bash
   tmux list-panes -t eval-<job> -F '#{pane_pid}' | xargs kill -0
   ```
   If the harbor process is dead, alert immediately rather than polling forever. Print
   the tmux pane's last output to help diagnose the crash.

**Stale progress detection**: If the progress count hasn't changed for 30 minutes, print
a warning (task may be stuck or harbor may be wedged).

On completion, send a Slack notification (via slackcli to #ops, configurable).

### Resume support

If harbor crashes mid-run:

```bash
./tools/run-eval.sh --job reviewer-v4 --resume
```

This:
1. Checks that the manifest exists and `status` is `"running"` or `"failed"`
2. SSHes to magic-kingdom and runs `harbor jobs resume --job-path <path>`
3. Re-enters the monitor loop

Harbor's resume skips completed trials and re-runs only the missing ones.

---

## Collect and Normalize

Collection is a standalone, idempotent script: `tools/collect-run.sh`.

### Design principles

- **Copy, don't move** — harbor's output is never modified. Re-runnable.
- **Staging directory** — collect writes to `<run-dir>.staging/`, then atomic `mv` to
  `<run-dir>/` on success. A crash mid-collect leaves the staging dir (which can be
  deleted and re-tried) and the archive unchanged.
- **`--dry-run`** — shows what would be collected without writing anything.
- **Idempotent** — running collect twice produces the same result.

### Usage

```bash
# Standalone collection
./tools/collect-run.sh --job reviewer-v4 --run-id 2026-02-28T234024Z_full-53-reviewer_9bf4057

# Dry run
./tools/collect-run.sh --job reviewer-v4 --run-id ... --dry-run
```

### Mapping

```
harbor output:                          archive:
task__hash/                      →      tasks/task/rep-N/
  agent/command-0/stdout.txt     →        agent-stdout.txt
  agent/serf-state/sessions/*    →        sessions/*
  agent/serf-state/api.jsonl     →        api.jsonl
  agent/artifacts/*              →        artifacts/   (filtered, see below)
  verifier/test-stdout.txt       →        verifier-stdout.txt
  verifier/reward.txt            →        reward.txt
  result.json                    →        harbor-result.json
```

### Rep numbering

For each task, collect all harbor directories for that task (e.g., `build-cython-ext__Qi4Hivi`,
`build-cython-ext__n8pPrwp`). Sort by the hash suffix lexicographically. Assign `rep-1`,
`rep-2`, etc. in sorted order. Write the mapping to `manifest.json`'s `rep_mapping` field.

### Failure categorization

During collection, derive the failure category for each rep and write it to
`failure_category.txt`. Categories: `timeout`, `wrong_answer`, `no_submit`, `api_error`.
Derived from harbor-result.json (exception type) and agent-stdout.txt (presence of
communicate/submit_result calls). This is done once during collect so downstream tools
don't need to re-parse stdout.

### Missing tasks

If harbor ran fewer tasks than expected (crash or `--task` filter), the archive contains
only the tasks that ran. `summary.json` reports the actual task count, and the manifest
status helps distinguish "intentionally filtered" from "crashed before finishing."

---

## Artifact Extraction

Harbor has no post-verification hook. The `TrialEvent.END` fires after the container
is already destroyed. The adapter's `run()` override is the only extraction point.

### Changes to serf_agent.py

1. **Move state dir to bind mount**: Change `--state-dir` from `/tmp/serf-state` to
   `/logs/agent/serf-state`. Harbor bind-mounts `/logs/agent/` to the host, so
   transcripts and api.jsonl persist automatically without explicit download.

2. **Extract /app artifacts with filtering**: In the adapter's `try/finally` block:
   ```python
   ARTIFACT_EXCLUDES = [".git/", "node_modules/", "__pycache__/", ".venv/",
                         "*.pyc", "*.o", "*.so", ".cache/"]
   ARTIFACT_WARN_MB = 100

   # ... in try/finally:
   await environment.download_dir("/app", self.logs_dir / "artifacts",
                                   exclude=ARTIFACT_EXCLUDES)
   ```
   The exclusion list is defined in the adapter (not hardcoded in the collect script)
   so different agents can customize it. If `/app` exceeds `ARTIFACT_WARN_MB` after
   filtering, log a warning.

3. **Keep existing download as fallback**: The bind mount is the primary path. The
   explicit `download_dir` in `finally` remains as a fallback for non-Docker environments.

---

## Report Generation

Each run produces a static HTML report at `report.html`. A separate cross-run
comparison tool produces comparison reports.

### Per-run report (report.html)

Generated by `tools/generate-report.py <run-dir>`:

- **Summary table**: Task name, reward (per rep), duration, tokens used, failure category
- **Aggregate stats**: Pass rates (majority/strict/any) with 95% CIs, total tokens, mean task duration
- **Failure breakdown**: Pie chart or table — timeout / wrong-answer / no-submit / api-error
- **Per-task detail** (collapsible): Verifier output, agent stdout, session list
- **Per-session transcript** (collapsible): Tool calls, model responses, usage stats
- **API call analysis**: Empty responses, error rates, latency distribution
- **Token budget analysis**: Input/output/cached/reasoning token breakdowns

`generate-report.py` reads the archive format directly. It reuses parsing logic from
`transcript-viewer.py` but operates on the archive layout (not harbor's raw layout).

### Cross-run comparison

Generated by `tools/compare-runs.py <run-dir-A> <run-dir-B>`:

- **Side-by-side table**: Task, reward-A, reward-B, delta (improvement/regression)
- **Aggregate comparison**: Pass rate A vs B, bootstrap CI on the difference
- **Diff of agent snapshots**: `diff agent-A/prompts/ agent-B/prompts/` inline
- **Statistical tests**: Bootstrap CI (primary), McNemar's on majority-vote (secondary)

### summary.json

Machine-readable rollup for scripting and aggregation:

```json
{
  "schema_version": 1,
  "run_id": "2026-02-28T234024Z_full-53-reviewer_9bf4057",
  "pass_rate_majority": 0.584,
  "pass_rate_strict": 0.539,
  "pass_rate_any": 0.618,
  "task_count": 89,
  "pass_count_majority": 52,
  "pass_count_strict": 48,
  "pass_count_any": 55,
  "total_tokens": 4521000,
  "total_input_tokens": 3800000,
  "total_output_tokens": 721000,
  "total_duration_s": 5420,
  "mean_task_duration_s": 61,
  "failure_categories": {
    "timeout": 12,
    "wrong_answer": 18,
    "no_submit": 5,
    "api_error": 2
  },
  "tasks": [
    {
      "name": "build-cython-ext",
      "pass_majority": true,
      "pass_strict": false,
      "pass_any": true,
      "reps": [
        {"rep": 1, "trial_hash": "Qi4Hivi", "reward": 1.0, "duration_s": 323, "tokens": 14200, "failure_category": null},
        {"rep": 2, "trial_hash": "n8pPrwp", "reward": 0.0, "duration_s": 450, "tokens": 18900, "failure_category": "wrong_answer"}
      ]
    }
  ]
}
```

All three pass rates (strict/majority/any) are reported at every level. The primary
metric is `pass_rate_majority`. Per-task per-rep data is always available for custom
aggregation.

---

## Statistical Rigor

### Minimum reps

For publishable results, we need enough reps to distinguish signal from noise.

- **3 reps minimum** for any run we plan to report. This gives us a majority-vote
  signal (2/3 or 3/3) even for flaky tasks.
- **5 reps** for definitive comparisons between two configurations. This gives enough
  data for per-task confidence intervals and paired statistical tests.
- **1 rep** is fine for quick iteration during development. Just don't claim results.

### Confidence intervals

For overall pass rate, use **Wilson score intervals** (better than normal approximation
for proportions, especially near 0 or 1). Report 95% CI in summary.json and report.html.

Example: 52/89 = 58.4%, 95% Wilson CI: [47.8%, 68.4%]

**Caveat**: Wilson CIs assume independent Bernoulli trials, but tasks have heterogeneous
difficulty — easy tasks always pass, hard tasks always fail. The real uncertainty is
driven by the ~20-30 borderline tasks. Wilson CIs are therefore slightly overconfident.
For publishable work, also report **bootstrap CIs** (resample at the task level, 10k
iterations) which make no independence assumption.

### Paired comparisons (primary: bootstrap, secondary: McNemar's)

When comparing two runs (A vs B), the **primary method** is a bootstrap confidence
interval on the pass-rate difference:

1. For each task, compute the per-task pass rate in run A and run B (using all reps).
2. Compute the mean pass-rate difference across tasks.
3. Bootstrap: resample tasks (with replacement) 10,000 times, recompute the mean
   difference each time. The 2.5th and 97.5th percentiles give the 95% CI.
4. If the CI excludes zero, the difference is significant at p < 0.05.

This preserves per-rep information (a 3/5 vs 2/5 difference contributes more than a
5/5 vs 5/5 non-difference) and makes no distributional assumptions.

As a **secondary/simple test**, also report McNemar's test on majority-vote outcomes.
Build the 2x2 contingency table:
```
                Run B Pass    Run B Fail
Run A Pass       a (both)      b (A only)
Run A Fail       c (B only)    d (neither)
```
McNemar's chi-squared = (b - c)^2 / (b + c). This is a quick sanity check but loses
information by collapsing reps to a single binary outcome per task.

**Document both methods' limitations** in the report. The bootstrap is primary.

### Handling flaky tasks

~18/89 tasks in terminal-bench are nondeterministic. Report:
- **Strict pass rate**: task passes only if ALL reps pass
- **Majority pass rate**: task passes if >50% of reps pass
- **Any pass rate**: task passes if ANY rep passes

The majority pass rate is the primary metric. Strict and any are reported for context.
The summary.json includes per-task per-rep rewards so consumers can compute whatever
aggregation they want.

### What we report

For each run:
- Pass rates (majority/strict/any) with 95% Wilson CIs and bootstrap CIs
- Failure category breakdown
- Total tokens (input/output/cached/reasoning breakdown)
- Per-task breakdown with all rep results

For comparisons:
- Delta in pass rate with bootstrap CI on the difference (primary)
- McNemar's test p-value on majority-vote (secondary)
- List of improvements and regressions with per-rep detail
- Prompt/config diff between the two runs

### Limitations (documented, not yet solved)

- **No seed control**: Most providers don't support seed parameters for tool-use models.
  LLM sampling is nondeterministic. We mitigate with multiple reps, not seeds.
- **Partial rewards**: terminal-bench uses binary rewards. SWE-bench and future suites
  may use partial credit. The archive stores raw reward values, but aggregation logic
  currently assumes binary. Will need updates for non-binary suites.
- **Concurrency as confound**: API rate limits at concurrency 4 may produce different
  behavior than concurrency 1. The manifest stores concurrency for transparency.

---

## magic-kingdom Setup

### Prerequisites (to verify on first SSH)

- Docker installed and running
- tmux installed
- Python 3.11+ (for harbor)
- uv (for harbor installation)
- Sufficient disk for Docker images + archives (~500GB recommended)
- Internet access (for pulling Docker images and API calls)
- API keys (.env file with OPENAI_API_KEY, etc.; `chmod 600`)
- SSH key-based auth (password auth disabled)

### Directory layout

```
/data/serf-evals/                    # Archive root (persistent storage)
└── runs/                            # All runs

~/eval/                              # Working directory for harbor
├── serf-linux-amd64                 # Current binary (updated per run)
├── serf_agent.py                    # Current adapter (updated per run)
├── install-serf.sh.j2               # Container setup template
├── .env                             # API keys (chmod 600)
└── jobs/                            # Harbor's working output (temporary)
```

### Installation steps

1. SSH in, verify hardware (cores, RAM, disk)
2. Install Docker, tmux, uv
3. `uv tool install harbor==0.1.44` (pin version)
4. Create directory structure
5. Copy `.env` with API keys, `chmod 600 .env`
6. Copy `install-serf.sh.j2` and `serf_agent.py`
7. Test with a single task: `./tools/run-eval.sh --job smoke-test --task build-cython-ext --reps 1`
8. Verify archive structure is correct

### Docker cleanup

After each run, the wrapper runs `docker system prune -f --filter "until=24h"` to
clean up stopped containers and dangling images. For manual cleanup:
`docker system prune -a` (removes all unused images).

### Disk management

Estimated per-run sizes (89 tasks, 3 reps, with artifact filtering):
- Binary + snapshot: ~25MB
- Per rep: ~10MB (api.jsonl ~3MB, transcripts ~1MB, artifacts ~5MB, misc ~1MB)
- Per run: 267 reps x 10MB + 25MB = **~2.7GB**
- At 5 reps: ~4.5GB per run

At 500GB usable disk, that's ~100+ full runs before concern. Retention policy (to
implement later): keep last 30 full runs; for older runs, delete artifacts/ directories
but keep summary.json, manifest.json, and reward.txt (a few KB per run).

### Migration from flower-garden

- flower-garden's historical runs stay on flower-garden (or rsync to magic-kingdom)
- New `run-eval.sh` points at magic-kingdom by default
- Old `eval-task.sh` and `check-eval.sh` are kept but deprecated (they still work for flower-garden)

---

## Tools Summary

| Tool | Purpose | Status |
|------|---------|--------|
| `tools/run-eval.sh` | Launch, monitor, collect, report — the one tool | NEW |
| `tools/collect-run.sh` | Standalone idempotent collect/normalize with --dry-run | NEW |
| `tools/generate-report.py` | Produce report.html from a run archive | NEW |
| `tools/compare-runs.py` | Compare two runs with bootstrap CIs + McNemar's | NEW (replaces compare-runs.sh) |
| `tools/api-log-analyze.py` | Analyze api.jsonl files | UPDATE to accept run dirs |
| `tools/transcript-viewer.py` | Library code reused by generate-report.py | REFACTOR |
| `tools/eval-task.sh` | Legacy launcher for flower-garden | DEPRECATE |
| `tools/check-eval.sh` | Legacy monitor for flower-garden | DEPRECATE |
| `tools/compare-runs.sh` | Legacy comparator | REPLACE |

---

## What's NOT Changing

- **Harbor** remains the eval orchestrator. We wrap it, not replace it.
- **terminal-bench@2.0** dataset stays the same
- **serf_agent.py** adapter pattern stays the same (enhanced with artifact extraction)
- **Transcript format** (JSONL) stays the same
- **api.jsonl format** stays the same
- **Docker containers** per task stays the same

## Future Work (not in scope)

- **CXDB integration** — trajectory server (https://github.com/strongdm/cxdb), tabled
- **Cloud sandboxes** — harbor supports Daytona/Modal/E2B for higher parallelism
- **Dashboard** — live web UI for browsing runs (harbor has `harbor view` but it's basic)
- **Automated regression detection** — CI that runs evals on every PR
- **Cost tracking** — actual API costs per run from billing data
- **Disk retention automation** — cron-based cleanup of old runs (v1: manual)
- **Non-binary reward aggregation** — for SWE-bench and partial-credit suites

---

## Review Issues Addressed

This design was reviewed by two critical subagents and iterated based on their findings.
All issues resolved or explicitly deferred with rationale.

### Critical (all resolved)

| # | Issue | Resolution |
|---|-------|-----------|
| 1 | Can't actually reproduce (missing Docker image, harbor version, dataset commit) | Added `environment` block to manifest with all three |
| 2 | No crash safety (partial archive on collect failure) | Staging dir + atomic rename; `status` field in manifest |
| 3 | Nondeterministic rep numbering | Sort by harbor trial hash; `rep_mapping` in manifest |
| 4 | McNemar's on majority-vote loses information | Bootstrap CIs as primary; McNemar's as secondary |

### Major (all resolved)

| # | Issue | Resolution |
|---|-------|-----------|
| 5 | No liveness check in poll loop | Check tmux child PID; alert on death |
| 6 | Unfiltered /app artifacts | Exclusion list in adapter + size warning |
| 7 | tmux session collisions/zombies | `has-session` check; kill after collect; `--force` |
| 8 | Collect not idempotent | Standalone `collect-run.sh`; copy don't move; `--dry-run` |
| 9 | No schema version | `schema_version: 1` in manifest and summary |
| 10 | summary.json ambiguous with multiple reps | Report all three metrics; primary is majority |

### Deferred (noted, won't block v1)

| # | Issue | Notes |
|---|-------|-------|
| 11 | No disk retention policy | Documented estimates; implement cron-based cleanup later |
| 12 | Docker cleanup | Added `docker system prune` to post-run steps |
| 13 | Dirty tree handling | `--require-clean` is default; `--allow-dirty` stores git diff |
| 14 | Partial rewards | Binary only for now; noted as limitation |
| 15 | No seed control | Noted as limitation; most providers don't support for tool-use |
| 16 | Failure categories re-derived each time | Derived once during collect; stored per-rep |
| 17 | install-serf.sh.j2 missing from snapshot | Added to agent snapshot |
| 18 | harbor result.json not preserved | Mapped to harbor-result.json per rep |

---

## Context from Today's Session

### Observability work completed (merged to main, commit ad348a4)
- `buildinfo/` package — stamps binaries with git SHA via ldflags
- `llm/apilog.go` — middleware logs every LLM API call to `<state_dir>/api.jsonl`
- Eval manifests in `tools/eval-task.sh`
- `tools/api-log-analyze.py` and `tools/compare-runs.sh`

### Prompt fixes completed (merged to main, commit 9bf4057)
- Coordinator verify step: "subagents clean up after themselves, check state yourself"
- ops-task skill "Final State" section
- Reviewer prompt: "would a human be 100% satisfied?"

### Benchmark results
- configure-git-webserver: 0/4 (gpt-5.2-codex) -> **3/4** (gpt-5.3-codex + reviewer)
- Full suite running: `full-53-reviewer` (89 tasks, gpt-5.3-codex, reviewer gate)
  - Monitor: `./tools/check-eval.sh full-53-reviewer`

### Git state
- Branch: main
- Latest commit: 9bf4057
- All tests passing
