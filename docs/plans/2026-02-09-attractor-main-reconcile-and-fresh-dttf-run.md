# Attractor Main Reconciliation + Fresh DTTF Run Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Reconcile the remaining fan-in failure-routing reliability gap on top of current `main`, preserve deterministic retry behavior and Anthropic CLI contract guarantees with regression tests, then validate end-to-end via a brand-new DTTF run from scratch (no resume).

**Architecture:** Keep `main`’s current engine lifecycle and finalization flow, and add a narrow routing policy layer for failed outcomes at `parallel.fan_in` nodes so failure paths cannot silently fall through unconditional edges. Reuse that policy from both run and resume traversal paths to avoid drift. Preserve fail-closed behavior and retry gating driven by canonical `failure_class` metadata.

**Tech Stack:** Go (`internal/attractor/engine`), Attractor DOT execution, provider CLI adapters (`claude`, `gemini`, `codex`), `go test`, detached `kilroy attractor run` validation with artifact assertions (`progress.ndjson`, `status.json`, `cli_invocation.json`, `final.json`).

---

## Why This Is Idiomatic (and Why Not a Big Refactor)

- Most idiomatic for current architecture: introduce one small next-hop policy helper and call it from both `runLoop` and `resumeFromLogsRoot`.
- This fixes the system, not the symptom: it prevents future drift between run and resume behavior.
- Major refactor (full state-machine rewrite) is not beneficial now because `main` just landed major lifecycle changes; a focused policy module is lower-risk and easier to validate.

---

### Task 1: Establish Main-Branch Baseline With Red Repro for Fan-In Fail Routing

**Files:**
- Create: `internal/attractor/engine/failure_routing_fanin_test.go`
- Modify (only if helpers are needed): `internal/attractor/engine/parallel_test.go`

**Step 1: Add a failing integration test for fan-in all-fail routing**

Add a graph where:
- `par` (`shape=component`) fans out to three deterministic failing branches
- branches converge at `join` (`shape=tripleoctagon` => `parallel.fan_in`)
- `join` has only an unconditional edge to `verify`
- there is no `condition="outcome=fail"` edge and no node-level retry target

Assert:
- `join` outcome is `fail` with reason containing `all parallel branches failed`
- `verify` is never executed
- terminal run fails rather than falling through via unconditional edge

**Step 2: Run only the new test (expect red on current main)**

Run:
```bash
go test ./internal/attractor/engine -run 'TestFailureRouting_FanInAllFail_DoesNotFollowUnconditionalEdge' -v
```

Expected:
- FAIL (current behavior selects unconditional edge after fan-in fail)

**Step 3: Commit red test checkpoint**

```bash
git add internal/attractor/engine/failure_routing_fanin_test.go
git commit -m "test(attractor): reproduce fan-in fail routing fallthrough on main"
```

---

### Task 2: Implement Shared Next-Hop Policy for Fan-In Failure Outcomes

**Files:**
- Create: `internal/attractor/engine/next_hop.go`
- Create: `internal/attractor/engine/next_hop_test.go`
- Modify: `internal/attractor/engine/engine.go`
- Modify: `internal/attractor/engine/resume.go`

**Step 1: Add policy module for failed fan-in outcomes**

Implement:
- `resolveNextHop(g, from, out, ctx) (*resolvedNextHop, error)`
- `selectMatchingConditionalEdge(...)`
- `resolveRetryTargetWithSource(...)`

Policy:
1. If source node type is `parallel.fan_in` and outcome is `fail` or `retry`:
- evaluate matching conditional edges first
- then retry target chain (`retry_target`, `fallback_retry_target`, graph-level)
- otherwise no next hop (terminal fail)
2. For all other cases, preserve current `selectNextEdge` behavior.

**Step 2: Wire resolver into run + resume traversal**

- In `runLoop` (`engine.go`), replace direct `selectNextEdge` hop selection with resolver.
- In `resumeFromLogsRoot` (`resume.go`), use the same resolver.

**Step 3: Add resolver unit tests**

Cover:
- fan-in fail does not choose unconditional edge
- fan-in fail chooses retry target when present
- fan-in fail chooses matching conditional edge over retry target
- non-fan-in behavior remains unchanged

**Step 4: Run focused tests**

Run:
```bash
go test ./internal/attractor/engine -run 'TestFailureRouting_FanInAllFail_DoesNotFollowUnconditionalEdge|TestResolveNextHop_' -v
```

Expected:
- PASS

**Step 5: Run package tests**

Run:
```bash
go test ./internal/attractor/engine -v
```

Expected:
- PASS

**Step 6: Commit implementation**

```bash
git add internal/attractor/engine/next_hop.go \
  internal/attractor/engine/next_hop_test.go \
  internal/attractor/engine/engine.go \
  internal/attractor/engine/resume.go \
  internal/attractor/engine/failure_routing_fanin_test.go
git commit -m "fix(attractor): enforce fan-in failure-routing precedence in run and resume"
```

---

### Task 3: Lock Deterministic No-Retry Behavior for Fan-In All-Fail

**Files:**
- Modify: `internal/attractor/engine/failure_routing_fanin_test.go`
- Modify (if needed): `internal/attractor/engine/retry_failure_class_test.go`

**Step 1: Extend regression assertions**

In fan-in regression test, assert:
- `join` attempt count is exactly 1
- no `stage_retry_sleep` events for `join`
- `stage_retry_blocked` exists for `join` with `failure_class=deterministic` when class hint is present

**Step 2: Run targeted deterministic retry tests**

Run:
```bash
go test ./internal/attractor/engine -run 'TestFailureRouting_FanInAllFail_DoesNotFollowUnconditionalEdge|TestRun_DeterministicFailure_DoesNotRetry|TestRunWithConfig_CLIDeterministicFailure_DoesNotConsumeRetryBudget' -v
```

Expected:
- PASS

**Step 3: Commit test hardening**

```bash
git add internal/attractor/engine/failure_routing_fanin_test.go internal/attractor/engine/retry_failure_class_test.go
git commit -m "test(attractor): guard deterministic no-retry semantics for fan-in all-fail joins"
```

---

### Task 4: Add Anthropic CLI Contract Artifact-Level Integration Guard

**Files:**
- Create: `internal/attractor/engine/anthropic_cli_contract_integration_test.go`
- Modify (if needed): `internal/attractor/engine/codergen_router.go`

**Step 1: Add end-to-end CLI invocation artifact test**

Create a fake `claude` CLI test matrix:
- case A: verbose capability supported -> `cli_invocation.json` includes `--verbose` with `stream-json`
- case B: verbose unsupported -> preflight fails deterministically with actionable reason (or invocation avoids invalid contract; pick one contract and enforce it consistently)

**Step 2: Run targeted Anthropic contract tests**

Run:
```bash
go test ./internal/attractor/engine -run 'TestAnthropicCLIContract_|TestDefaultCLIInvocation_Anthropic|TestRunWithConfig_Preflight|TestResume_Preflight' -v
```

Expected:
- PASS

**Step 3: Commit**

```bash
git add internal/attractor/engine/anthropic_cli_contract_integration_test.go internal/attractor/engine/codergen_router.go
git commit -m "test(attractor): add anthropic stream-json/verbose artifact contract integration coverage"
```

---

### Task 5: Full Test Gate on New Worktree

**Files:**
- None

**Step 1: Engine package gate**

Run:
```bash
go test ./internal/attractor/engine -v
```

Expected: PASS

**Step 2: Repo-wide gate**

Run:
```bash
go test ./... -v
```

Expected: PASS

**Step 3: Commit if any test fixtures/docs were adjusted**

```bash
git add <adjusted-files>
git commit -m "test(attractor): align fixtures after fan-in routing policy reconciliation"
```

---

### Task 6: Validate With a Fresh DTTF Run (No Resume)

**Files:**
- Create runtime artifacts under `/tmp/kilroy-dttf-main-fresh-<timestamp>/`
- Optional docs update: `docs/strongdm/attractor/reliability-troubleshooting.md`

**Step 1: Build kilroy in this worktree**

Run:
```bash
cd /home/user/code/kilroy/.worktrees/attractor-main-dttf-newrun-2026-02-09
go build -o ./kilroy ./cmd/kilroy
```

Expected: binary at `./kilroy`

**Step 2: Create fresh run root and clean repo copy**

Run:
```bash
RUN_ROOT="/tmp/kilroy-dttf-main-fresh-$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -p "$RUN_ROOT"

git clone --no-local /home/user/code/kilroy/.worktrees/attractor-main-dttf-newrun-2026-02-09 "$RUN_ROOT/repo"
```

Expected: fresh checkout at `$RUN_ROOT/repo`

**Step 3: Create run config for fresh run**

Run:
```bash
cat > "$RUN_ROOT/run_config.json" <<JSON
{
  "version": 1,
  "repo": {
    "path": "$RUN_ROOT/repo"
  },
  "cxdb": {
    "binary_addr": "127.0.0.1:9009",
    "http_base_url": "http://127.0.0.1:9010"
  },
  "llm": {
    "providers": {
      "anthropic": { "backend": "cli" },
      "google": { "backend": "cli" },
      "openai": { "backend": "cli" }
    }
  },
  "modeldb": {
    "litellm_catalog_path": "/home/user/code/kilroy/.worktrees/attractor-main-dttf-newrun-2026-02-09/internal/attractor/modeldb/pinned/model_prices_and_context_window.json",
    "litellm_catalog_update_policy": "pinned",
    "litellm_catalog_url": "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json",
    "litellm_catalog_fetch_timeout_ms": 5000
  },
  "git": {
    "require_clean": true,
    "run_branch_prefix": "attractor/run",
    "commit_per_node": true
  }
}
JSON
```

Expected: `$RUN_ROOT/run_config.json` created with fresh repo path.

**Step 4: Launch NEW run from scratch (explicitly not resume)**

Run:
```bash
RUN_ID="dttf-main-fresh-$(date -u +%Y%m%dT%H%M%SZ)"
setsid -f bash -lc 'cd /home/user/code/kilroy/.worktrees/attractor-main-dttf-newrun-2026-02-09 && ./kilroy attractor run --detach --graph "'"$RUN_ROOT"'"/repo/demo/dttf/dttf.dot --config "'"$RUN_ROOT"'"/run_config.json --run-id "'"$RUN_ID"'" --logs-root "'"$RUN_ROOT"'"/logs > "'"$RUN_ROOT"'"/run.out 2>&1'
```

Expected:
- detached launch metadata in `$RUN_ROOT/run.out`
- no use of `attractor resume`

**Step 5: Poll live progress and wait for terminal state**

Run:
```bash
while [ ! -f "$RUN_ROOT/logs/final.json" ]; do
  date -u +"%Y-%m-%dT%H:%M:%SZ"
  test -f "$RUN_ROOT/logs/live.json" && cat "$RUN_ROOT/logs/live.json"
  sleep 30
done
cat "$RUN_ROOT/logs/final.json"
```

Expected:
- `final.json` exists
- terminal status and non-empty `failure_reason` when failed

**Step 6: Assert artifact expectations for the reliability issues**

Run:
```bash
rg -n '"from_node":"join_tracer".*"to_node":"verify_tracer"' "$RUN_ROOT/logs/progress.ndjson" -S
rg -n '"event":"stage_retry_sleep".*"node_id":"join_tracer"' "$RUN_ROOT/logs/progress.ndjson" -S
rg -n 'requires --verbose|provider cli preflight|stream-json contract' "$RUN_ROOT/logs" -S
jq -r '.status, .failure_reason' "$RUN_ROOT/logs/final.json"
```

Expected:
- no `join_tracer -> verify_tracer` edge when `join_tracer` fails all branches
- no `stage_retry_sleep` for deterministic `join_tracer` failures
- no Anthropic `stream-json requires --verbose` runtime mismatch
- `final.json` has non-empty `failure_reason` when status is fail

**Step 7: Document fresh-run validation commands**

Update troubleshooting docs with these exact grep/jq checks.

**Step 8: Commit docs update**

```bash
git add docs/strongdm/attractor/reliability-troubleshooting.md docs/strongdm/attractor/README.md
git commit -m "docs(attractor): document fresh DTTF run-from-scratch reliability validation checks"
```

---

## Final Merge Safety Gate

Run in this worktree:
```bash
go test ./internal/attractor/engine -v
go test ./... -v
```

Then follow repo merge policy:
1. Merge latest `main` into this feature branch in the worktree.
2. Re-run full tests.
3. Fast-forward `main` only after green.

---

## Definition of Done

1. Fan-in all-fail cannot route via unconditional fallback edge in run or resume.
2. Deterministic fan-in all-fail outcomes do not consume stage retry budget.
3. Anthropic `stream-json`/`--verbose` contract is covered by artifact-level integration tests.
4. A **new DTTF run from scratch** (fresh logs root + fresh repo copy) validates expected behavior; no resume-based validation is used.
5. Full tests pass.
