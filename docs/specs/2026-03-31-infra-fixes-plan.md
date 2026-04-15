# Infrastructure Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix stale agent tarball reuse, rebase on main for turn_count fix, document invalid baseline, clean up experiment branches, relaunch.

**Architecture:** Include git SHA in S3 tarball path (prevents binary mismatch). Rebase worktree on main (picks up turn_count + other fixes). Mark invalid results. Clean build + verified launch.

**Tech Stack:** Bash (harbor-runner/launch.sh), Go (agent/session.go), Git

---

### Task 1: Rebase worktree on main (MUST be done inline — not subagent)

This task involves interactive conflict resolution and cannot be delegated.

**Files:**
- All files in worktree

- [x] **Step 1: Check divergence**

```bash
echo "Worktree ahead of main by:"
git log --oneline main..HEAD | wc -l
echo "Main ahead of worktree by:"
git log --oneline HEAD..main | wc -l
```

- [x] **Step 2: Stash any uncommitted changes**

```bash
git stash
```

- [x] **Step 3: Rebase onto main**

```bash
git rebase main
```

Known conflict files and resolution strategy:
- `agent/session.go` — accept BOTH: main's `modelResponses` turn_count fix AND worktree's task-driven workflow changes (task population in NewSession, dynamic reasoning effort). The two changes touch different parts of the file.
- `agent/subagents.go` — accept worktree's version (has task_list parameter + AgentName fix + working_dir removal). Main's prompt dedup fix should already be in worktree (was cherry-picked as commit bbd3f96).
- `tools/serf_agent.py` — accept worktree's version (reasoning_effort="low"). Main has "xhigh" from v55 which the worktree intentionally reverted.
- `agent/agents/coordinator.md` — accept worktree's version (YAML tasks with h/m/m effort levels). Main has v55 prose-based planning which the worktree replaced.
- `docs/experiments/*` — accept worktree's versions (more recent experiment data).

- [x] **Step 4: Pop stash if needed**

```bash
git stash pop 2>/dev/null || true
```

- [x] **Step 5: Verify turn_count fix is present**

```bash
grep "modelResponses" agent/session.go | head -3
```
Expected: `modelResponses` field definition and usage in `SessionMeta`/`SessionSnapshot`.

- [x] **Step 6: Verify all tests pass**

```bash
cd agent && go test ./... -count=1
```
Expected: All tests pass. If any fail, fix them before proceeding.

---

### Task 2: Fix tarball path to include git SHA

**Files:**
- Modify: `/Users/jesse/prime-radiant/harbor-runner/launch.sh:121`

- [x] **Step 1: Write a test script**

Create `/Users/jesse/prime-radiant/harbor-runner/test-sha-extraction.sh`:

```bash
#!/bin/bash
# Test that we can extract GitSHA from a serf binary.
set -e

BINARY="${1:-serf-linux-amd64}"
if [[ ! -f "$BINARY" ]]; then
    echo "FAIL: binary not found: $BINARY"
    exit 1
fi

# Extract SHA using the same method launch.sh will use.
SHA=$(strings "$BINARY" | sed -n 's/.*GitSHA=\([a-f0-9]*\).*/\1/p' | head -1)

if [[ -z "$SHA" ]]; then
    echo "FAIL: could not extract GitSHA from $BINARY"
    exit 1
fi

if [[ ${#SHA} -lt 7 ]]; then
    echo "FAIL: extracted SHA too short: '$SHA'"
    exit 1
fi

echo "PASS: extracted GitSHA=$SHA from $BINARY"

# Verify the S3 path would contain the SHA.
RUN_ID="wave-test-123"
EXPECTED_PATH="agents/$RUN_ID/$SHA/agent.tar.gz"
echo "PASS: S3 path would be: $EXPECTED_PATH"
```

- [x] **Step 2: Run the test against current binary**

```bash
chmod +x ~/prime-radiant/harbor-runner/test-sha-extraction.sh
~/prime-radiant/harbor-runner/test-sha-extraction.sh ~/prime-radiant/serf/.claude/worktrees/task-driven-workflow/serf-linux-amd64
```
Expected: `PASS: extracted GitSHA=...`

- [x] **Step 3: Update launch.sh to include SHA in tarball path**

In `/Users/jesse/prime-radiant/harbor-runner/launch.sh`, replace line 121:

Old:
```bash
AGENT_S3_PATH="agents/$RUN_ID/agent.tar.gz"
```

New:
```bash
# Include git SHA in tarball path so different binaries never collide.
# Uses sed (not grep -P) for macOS compatibility.
AGENT_GIT_SHA=""
if [[ -f "$AGENT_DIR/serf-linux-amd64" ]]; then
    AGENT_GIT_SHA=$(strings "$AGENT_DIR/serf-linux-amd64" | sed -n 's/.*GitSHA=\([a-f0-9]*\).*/\1/p' | head -1)
fi
if [[ -z "$AGENT_GIT_SHA" ]]; then
    echo "Warning: could not extract GitSHA from binary, using 'unknown'" >&2
    AGENT_GIT_SHA="unknown"
fi
echo "  Agent binary SHA: $AGENT_GIT_SHA"
AGENT_S3_PATH="agents/$RUN_ID/$AGENT_GIT_SHA/agent.tar.gz"
```

Note: uses `sed -n 's/.../p'` instead of `grep -oP` because macOS grep doesn't support `-P`.

- [x] **Step 4: Run the test again to confirm sed extraction works**

```bash
# Inline test of the sed command
strings ~/prime-radiant/serf/.claude/worktrees/task-driven-workflow/serf-linux-amd64 | sed -n 's/.*GitSHA=\([a-f0-9]*\).*/\1/p' | head -1
```
Expected: outputs a 7+ character hex SHA.

- [x] **Step 5: Commit**

```bash
cd ~/prime-radiant/harbor-runner
git add launch.sh test-sha-extraction.sh
git commit -m "fix: include git SHA in agent tarball S3 path

Prevents stale binary reuse when backfill or retry reuses a run-id.
Previous: agents/RUN_ID/agent.tar.gz (first upload wins).
New: agents/RUN_ID/GIT_SHA/agent.tar.gz (each binary gets own tarball).

Uses sed instead of grep -P for macOS compatibility.

Root cause: wave-b622a6c ran binary from commit 49a82bb because
launch.sh skipped upload when tarball already existed in S3."
```

---

### Task 3: Document invalid baseline and clean up

**Files:**
- Modify: `docs/experiments/experiment-log.md`
- Modify: `docs/experiments/NOTEBOOK.md`
- Modify: `docs/experiments/scoreboard.json` (remove invalid run data)
- Delete: experiment branches

- [x] **Step 1: Add INVALID entry to experiment-log.md**

At the top of the experiment log, add:

```markdown
## wave-b622a6c INVALID — wrong binary deployed (Mar 31)

**Run:** wave-b622a6c-20260331-0508 (267 items, 89 tasks × 3 reps)
**Intended binary:** commit b622a6c (task-driven workflow, h/m/m effort, AgentName fix, working_dir removed)
**Actual binary:** commit 49a82bb (pre-AgentName fix, pre-working_dir removal, xhigh effort levels)

**Root cause:** launch.sh caches agent tarball by run-id in S3. The first
launch attempt (which failed due to uncommitted files) uploaded a tarball
built from 49a82bb. The backfill reused the same run-id, and launch.sh
skipped upload because the tarball already existed. All 267 instances ran
the wrong binary.

**Evidence:**
- S3 tarball `strings` shows `GitSHA=49a82bb`, not `b622a6c`
- Implementer task stores contain coordinator tasks (Inventory, Plan, Delegate)
  instead of implementer tasks (Understand, Do the work, Verify, Clean up)
- Effort levels show xhigh (from 49a82bb) not high/medium (from b622a6c)

**Impact:** All results from this wave are invalid. The reported mean of 0.469,
the 22 improvements and 18 regressions, and all root-cause analyses based on
this data are unreliable. The "coordinator stuck" pattern and "framework
regressions" were actually caused by implementers receiving the coordinator's
task workflow.

**Fix:** git SHA now included in S3 tarball path. See launch.sh fix.

**Results:** DISCARDED.
```

- [x] **Step 2: Update NOTEBOOK.md current state**

Replace the current state section with updated information noting the invalid
baseline and the infrastructure fix.

- [x] **Step 3: Remove invalid run from scoreboard**

If wave-b622a6c data was collected into the scoreboard, remove it:

```bash
python3 -c "
import json
with open('docs/experiments/scoreboard.json') as f:
    sb = json.load(f)
# Remove any task scores that came from the invalid run
for task, info in sb.get('tasks', {}).items():
    if info.get('best_run') == 'wave-b622a6c-20260331-0508':
        # Reset to previous best
        info['score'] = None
        info['best_run'] = None
        info['reps'] = []
with open('docs/experiments/scoreboard.json', 'w') as f:
    json.dump(sb, f, indent=2)
print('Scoreboard cleaned')
"
```

Actually — check first whether any task's BEST score came from this invalid run.
If no task has its best score from this run, no cleanup needed.

- [x] **Step 4: Delete experiment branches**

```bash
for branch in exp/effort-all-high exp/effort-selective exp/effort-h-m-m exp/effort-split exp/effort-xh-m-m exp/effort-adaptive exp/p1a exp/p1b exp/p1c exp/p1d exp/p1e exp/p2a exp/p2b exp/p2c exp/p2d exp/p2e exp/p2f exp/p2null; do
    git branch -D "$branch" 2>/dev/null && echo "Deleted $branch" || true
done
```

- [x] **Step 5: Commit**

```bash
git add docs/experiments/
git commit -m "docs: wave-b622a6c INVALID, clean up experiment branches"
```

---

### Task 4: Clean build, verify, and relaunch baseline

- [x] **Step 1: Ensure clean working tree**

```bash
git status
```
Must show no uncommitted changes. If dirty, commit or stash.

- [x] **Step 2: Build fresh binary**

```bash
make build-linux
```

- [x] **Step 3: Verify binary SHA matches HEAD**

```bash
HEAD_SHA=$(git rev-parse --short HEAD)
BINARY_SHA=$(strings serf-linux-amd64 | sed -n 's/.*GitSHA=\([a-f0-9]*\).*/\1/p' | head -1)
echo "HEAD: $HEAD_SHA"
echo "Binary: $BINARY_SHA"
if [[ "$HEAD_SHA" == "$BINARY_SHA" ]]; then
    echo "MATCH — binary is from current HEAD"
else
    echo "MISMATCH — binary is stale! Rebuild needed."
    exit 1
fi
```

- [x] **Step 4: Verify key features in binary**

```bash
# Has implementer tasks (not just coordinator tasks)
strings serf-linux-amd64 | grep "Understand requirements" | head -1
# Has coordinator YAML tasks
strings serf-linux-amd64 | grep "title: Inventory" | head -1
# Has h/m/m effort levels (not xhigh)
strings serf-linux-amd64 | grep -c "reasoning_effort: xhigh"
# Should be 0 (Plan was changed from xhigh to high in h/m/m config)
```

- [x] **Step 5: Launch full 89-task baseline**

```bash
export $(cat .env 2>/dev/null | xargs)
./tools/run_eval.sh --reps 3 --instance-type c6i.xlarge
```

This auto-generates a new run-id from the git SHA, ensuring a fresh S3 tarball path.

- [x] **Step 6: Verify deployed binary after first instance launches**

Wait ~60 seconds for the first instance to launch, then:

```bash
export $(cat .env 2>/dev/null | xargs)
# Get the run-id from the wave_launcher process
RUN_ID=$(ps aux | grep wave_launcher | grep -v grep | sed -n 's/.*--run-id \([^ ]*\).*/\1/p')
echo "Run ID: $RUN_ID"

# Download and verify the S3 tarball
SHA_IN_PATH=$(aws s3 ls "s3://harbor-eval-results-526275945504/agents/$RUN_ID/" --region us-west-1 | head -1 | awk '{print $2}' | tr -d '/')
echo "SHA in S3 path: $SHA_IN_PATH"

aws s3 cp "s3://harbor-eval-results-526275945504/agents/$RUN_ID/$SHA_IN_PATH/agent.tar.gz" /tmp/verify-tarball.tar.gz --region us-west-1
mkdir -p /tmp/verify-tarball
tar xzf /tmp/verify-tarball.tar.gz -C /tmp/verify-tarball/
DEPLOYED_SHA=$(strings /tmp/verify-tarball/serf-linux-amd64 | sed -n 's/.*GitSHA=\([a-f0-9]*\).*/\1/p' | head -1)
echo "Deployed binary SHA: $DEPLOYED_SHA"
echo "Expected SHA: $(git rev-parse --short HEAD)"

if [[ "$DEPLOYED_SHA" == "$(git rev-parse --short HEAD)" ]]; then
    echo "VERIFIED — correct binary deployed"
else
    echo "ERROR — wrong binary! Kill the run and investigate."
fi
rm -rf /tmp/verify-tarball /tmp/verify-tarball.tar.gz
```

- [x] **Step 7: Commit run metadata**

```bash
git add docs/experiments/
git commit -m "docs: launch verified full baseline (correct binary confirmed)"
```

---

## Verification Checklist

1. `go test ./agent/... -count=1` — all tests pass after rebase
2. `grep "modelResponses" agent/session.go` — turn_count fix present
3. `sed -n 's/.*GitSHA=\([a-f0-9]*\).*/\1/p'` extracts SHA from binary — macOS compatible
4. `grep "AGENT_GIT_SHA" ~/prime-radiant/harbor-runner/launch.sh` — tarball SHA path present
5. New baseline run-id has SHA subdirectory in S3 agents path
6. Deployed binary SHA matches `git rev-parse --short HEAD`
7. No experiment branches remain (all deleted)
8. wave-b622a6c documented as INVALID in experiment-log.md
