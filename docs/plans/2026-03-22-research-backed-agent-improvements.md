# Research-Backed Agent Improvements

> **Status**: DRAFT — awaiting approval
> **Date**: 2026-03-22
> **Source**: "From Code Foundation Models to Agents and Applications" (arXiv 2511.18538, Dec 2025, 71 authors, 160 pages)

## Problem

Serf's best result is 69/89 (77.5%) on terminal-bench with lace harness + gate v4. The dominant failure modes are well-characterized:

| Failure Mode | Frequency | Description |
|---|---|---|
| Reviewer-approved-but-wrong | 52-60% | Agent produces something that looks correct but doesn't meet requirements |
| Timeouts | 33% | Agent enters futile iteration loops consuming the entire budget |
| Never-submitted | 23% | Agent works but never calls communicate |

A Dec 2025 survey of 1300+ papers on code intelligence catalogs techniques that directly address each failure mode. This spec maps 10 techniques from the literature to serf's architecture, organized into three tiers by the failure mode they target.

## Goals

- Move from 77.5% toward 85%+ by addressing the three failure modes structurally
- Each improvement implementable and testable independently
- No changes to the underlying LLM or training — these are harness/prompt improvements

## Non-Goals

- RL training (we use commercial APIs, not our own models)
- Agent-to-Agent protocol (our subagent model is simpler and works)
- Fine-tuning (same reason as RL)

## Overview

| # | Improvement | Tier | Type | Key Files |
|---|---|---|---|---|
| 1 | Repair cycle cap | Loop Discipline | Go + config | `repair_tracker.go`, `session.go` |
| 2 | Error caching / approach diversification | Loop Discipline | Go | `approach_log.go`, `session.go` |
| 3 | Adaptive stopping / stagnation detection | Loop Discipline | Go | `progress_tracker.go`, `session.go` |
| 4 | Specification inference | Correctness | Prompt | `coordinator.md`, `verifier.md` |
| 5 | Multi-dimensional review | Correctness | Prompt + Go | `reviewer.md`, `session.go` |
| 6 | Parallel candidate generation | Correctness | Go + prompt | `workspace.go`, `coordinator.md` |
| 7 | Hierarchical fault localization | Efficiency | Prompt | `coordinator.md`, `explorer.md` |
| 8 | Codebase structure indexing | Efficiency | Go | `tool_codebase_index.go` |
| 9 | Context compression improvements | Efficiency | Go | `context_manager.go` |
| 10 | Experience repository | Efficiency | Python + Go | `build_experience_index.py`, `experience.go` |

---

# Tier 1 — Loop Discipline

These improvements address timeouts and futile iteration (33% of failures).

---

## 1. Repair Cycle Cap

### Motivation (from literature)

The survey (Section 5.1.2.1, p.107) presents strong evidence that **1-3 iterative refinement rounds yield the largest performance gains**, after which improvements plateau or regress due to noisy/misleading feedback. PyCapsule, Commit0, and similar systems all converge on the same finding: a small number of self-debug attempts corrects most fixable errors, and additional iterations provide diminishing or negative returns.

In serf, 33% of benchmark failures are timeouts. Transcript analysis shows subagents entering test→fail→edit→test loops that consume the entire 200-round budget without converging.

### Current Behavior

- `MaxToolRoundsPerInput` (default 200) limits total tool calls per input but does not track repair cycles.
- A "repair cycle" — agent runs tests, tests fail, agent edits code, agent runs tests again — has no dedicated counter.
- The agent has no visibility into its own iteration count.
- Identical test failures can repeat 10+ times without the agent changing strategy.

### Proposed Design

Add a `RepairTracker` to Session that counts test→fail→edit→test cycles and injects steering when the agent is stuck.

```go
// agent/repair_tracker.go

type RepairTracker struct {
    mu            sync.Mutex
    cycles        int       // number of test→fail→edit→test iterations
    lastTestHash  uint64    // hash of most recent failing test output
    identicalRuns int       // consecutive test runs with identical failure output
    editSinceLast bool      // whether an edit occurred since the last test run
    MaxCycles     int       // from config, default 3
}

// RecordTestRun is called after each shell tool execution that matches a test pattern.
func (rt *RepairTracker) RecordTestRun(output string, failed bool) {
    rt.mu.Lock()
    defer rt.mu.Unlock()

    if !failed {
        rt.cycles = 0
        rt.identicalRuns = 0
        rt.editSinceLast = false
        return
    }

    h := hashTestOutput(output)
    if h == rt.lastTestHash {
        rt.identicalRuns++
    } else {
        rt.identicalRuns = 0
    }
    rt.lastTestHash = h

    if rt.editSinceLast {
        rt.cycles++
        rt.editSinceLast = false
    }
}

// RecordEdit is called after each edit_file or write_file tool execution.
func (rt *RepairTracker) RecordEdit() {
    rt.mu.Lock()
    defer rt.mu.Unlock()
    rt.editSinceLast = true
}

// SteeringNeeded returns a steering message and true if the agent should be redirected.
func (rt *RepairTracker) SteeringNeeded() (string, bool) {
    rt.mu.Lock()
    defer rt.mu.Unlock()

    if rt.identicalRuns >= 2 {
        return "You have run the same test 3+ times with identical failure output. " +
            "Your edits are not changing the behavior. You MUST try a fundamentally " +
            "different approach — not a variation of what you have been doing.", true
    }
    if rt.cycles >= rt.MaxCycles {
        return fmt.Sprintf("You have attempted %d repair cycles without success. "+
            "You MUST either: (1) try a fundamentally different approach, or "+
            "(2) submit your best-effort result now. Do NOT repeat the same strategy.",
            rt.cycles), true
    }
    return "", false
}

// MustSubmit returns true when the agent has exhausted its repair budget entirely
// (MaxCycles + 2 grace cycles).
func (rt *RepairTracker) MustSubmit() bool {
    rt.mu.Lock()
    defer rt.mu.Unlock()
    return rt.cycles >= rt.MaxCycles+2
}
```

**Test output hashing** — normalize away timestamps so functionally identical failures hash the same:

```go
var timestampRe = regexp.MustCompile(`\d{4}[-/]\d{2}[-/]\d{2}[T ]\d{2}:\d{2}:\d{2}`)
var durationRe  = regexp.MustCompile(`\d+(\.\d+)?(ms|s|m|ns)`)

func hashTestOutput(s string) uint64 {
    s = timestampRe.ReplaceAllString(s, "")
    s = durationRe.ReplaceAllString(s, "")
    if len(s) > 1000 {
        s = s[:500] + s[len(s)-500:]
    }
    h := fnv.New64a()
    h.Write([]byte(s))
    return h.Sum64()
}
```

**Test command detection:**

```go
var testPatterns = []*regexp.Regexp{
    regexp.MustCompile(`\bgo\s+test\b`),
    regexp.MustCompile(`\bpytest\b`),
    regexp.MustCompile(`\bpython\s+-m\s+(pytest|unittest)\b`),
    regexp.MustCompile(`\bnpm\s+test\b`),
    regexp.MustCompile(`\bmake\s+test\b`),
    regexp.MustCompile(`\bcargo\s+test\b`),
    regexp.MustCompile(`\bjest\b`),
    regexp.MustCompile(`\bmocha\b`),
}

func isTestCommand(cmd string) bool {
    for _, p := range testPatterns {
        if p.MatchString(cmd) {
            return true
        }
    }
    return false
}
```

**Force submission:** When `MustSubmit()` returns true (MaxCycles + 2 = 5 total), inject a final steering message and set a `forceSubmit` flag on the session. The reviewer still runs (its feedback is logged), but its rejection does not trigger a retry loop.

### Wiring Points

| File | Change |
|------|--------|
| `agent/repair_tracker.go` | New file |
| `agent/session.go` | Add `repairTracker *RepairTracker` field. Initialize in `NewSession`. After `ExecuteCall` for `shell`: if `isTestCommand(cmd)`, call `RecordTestRun()`. After `edit_file`/`write_file`: call `RecordEdit()`. Before LLM call: check `SteeringNeeded()`. Add `forceSubmit bool` field. |
| `agent/session.go` — `SessionConfig` | Add `MaxRepairCycles int` |
| `cmd/serf/main.go` | Add `--max-repair-cycles` flag |

### Configuration

| Field | CLI Flag | Default | Description |
|-------|----------|---------|-------------|
| `MaxRepairCycles` | `--max-repair-cycles` | 3 | Test→fail→edit→test loops before steering. 0 disables. |

### Testing

- Unit: cycle counting — edit→testfail→edit→testfail = 2 cycles
- Unit: identical-failure detection — 3 identical outputs → `SteeringNeeded` true
- Unit: timestamp normalization — outputs differing only in timestamps hash the same
- Unit: reset on success — passing test resets cycles and identicalRuns
- Unit: `MustSubmit` at MaxCycles+2
- Integration: mock session with repeated test failures, verify steering injected

### Interactions

- **Feeds Approach Log (#2)**: Each repair cycle that triggers steering also logs the approach
- **Complements Stagnation Detection (#3)**: RepairTracker is semantic (test-edit loops); ProgressTracker is mechanical (any repetition). Both coexist.

---

## 2. Error Caching / Approach Diversification

### Motivation (from literature)

Reflexion (Section 5.3.2, p.135) turns trial-and-error into informed iteration by **caching past mistakes** to prevent oscillation. OpenHands (Section 5.1.2.1, p.108) improves refinement by **diversifying repair strategies instead of repeating previous fixes**. Multiple frameworks cited on p.107 explicitly cache previous attempts to prevent repeated patches.

In serf, when context compaction runs (checkpoint at 80%, LLM summarize at 90%), the agent's memory of failed approaches degrades. The agent then re-discovers and re-attempts strategies it already tried.

### Current Behavior

- Failed approaches exist only as free-text in conversation history
- Context compaction may compress or drop failure details
- After compaction, the agent may not remember what it tried
- When coordinator retries with a new subagent, no structured failure context is passed
- The reviewer rejection reason is free-text, subject to the same compaction

### Proposed Design

Add an `ApproachLog` — a compaction-resistant record of failed strategies. It lives outside conversation history and is re-injected as steering before each LLM call.

```go
// agent/approach_log.go

type ApproachEntry struct {
    Round       int       `json:"round"`
    Description string    `json:"description"`
    ErrorType   string    `json:"error_type"`   // compile_error, test_failure, runtime_error, timeout, reviewer_rejection
    ErrorOutput string    `json:"error_output"`  // truncated to 200 chars
    Timestamp   time.Time `json:"timestamp"`
}

type ApproachLog struct {
    mu         sync.Mutex
    entries    []ApproachEntry
    maxEntries int // default 10
}

func (al *ApproachLog) Add(entry ApproachEntry) {
    al.mu.Lock()
    defer al.mu.Unlock()
    if len(al.entries) >= al.maxEntries {
        al.entries = al.entries[1:]
    }
    al.entries = append(al.entries, entry)
}

func (al *ApproachLog) Format() string {
    al.mu.Lock()
    defer al.mu.Unlock()
    if len(al.entries) == 0 {
        return ""
    }
    var b strings.Builder
    b.WriteString("## Previous Failed Approaches\n")
    b.WriteString("These approaches have already been tried and failed. Do NOT repeat them.\n\n")
    for i, e := range al.entries {
        fmt.Fprintf(&b, "%d. [Round %d, %s] %s", i+1, e.Round, e.ErrorType, e.Description)
        if e.ErrorOutput != "" {
            fmt.Fprintf(&b, "\n   Error: `%s`", e.ErrorOutput)
        }
        b.WriteString("\n")
    }
    b.WriteString("\nYou must try a different strategy than any listed above.\n")
    return b.String()
}
```

**Automatic collection after test failure:**

When RepairTracker detects a test failure, extract a summary from recent tool calls:

```go
func ExtractApproachSummary(recentCalls []ToolCallRecord) string {
    var files []string
    seen := make(map[string]bool)
    for _, call := range recentCalls {
        if call.ToolName == "edit_file" || call.ToolName == "write_file" {
            path := extractFilePath(call.Args)
            if path != "" && !seen[path] {
                files = append(files, filepath.Base(path))
                seen[path] = true
            }
        }
    }
    if len(files) == 0 {
        return "Ran tests without modifying any files"
    }
    return fmt.Sprintf("Modified %s", strings.Join(files, ", "))
}

func ClassifyError(output string, exitCode int) string {
    lower := strings.ToLower(output)
    switch {
    case strings.Contains(lower, "compile") || strings.Contains(lower, "syntax error"):
        return "compile_error"
    case strings.Contains(lower, "timeout") || strings.Contains(lower, "timed out"):
        return "timeout"
    case strings.Contains(lower, "panic") || strings.Contains(lower, "segfault"):
        return "runtime_error"
    default:
        return "test_failure"
    }
}
```

**Key design property**: The approach log is NOT part of conversation history. It survives all four layers of context compaction because it is re-injected as a `TurnSteering` from its own data structure every round.

**Reviewer rejection integration:**

```go
// In handleReviewerVerdict, when reviewer rejects:
if !verdict.Approved {
    s.approachLog.Add(ApproachEntry{
        Round:       s.rounds,
        Description: "Reviewer rejected: " + verdict.Reason,
        ErrorType:   "reviewer_rejection",
        ErrorOutput: truncateError(verdict.Reason, 200),
    })
}
```

**Cross-subagent propagation:**

When coordinator spawns a retry subagent, format the approach log and append to the task:

```go
func FormatForSubagent(entries []ApproachEntry) string {
    if len(entries) == 0 {
        return ""
    }
    var b strings.Builder
    b.WriteString("\n---\nPrevious attempts at this task failed:\n")
    for i, e := range entries {
        fmt.Fprintf(&b, "%d. [%s] %s", i+1, e.ErrorType, e.Description)
        if e.ErrorOutput != "" {
            fmt.Fprintf(&b, " — Error: %s", e.ErrorOutput)
        }
        b.WriteString("\n")
    }
    b.WriteString("You must use a different approach.\n---\n")
    return b.String()
}
```

### Wiring Points

| File | Change |
|------|--------|
| `agent/approach_log.go` | New file |
| `agent/session.go` | Add `approachLog *ApproachLog` field. Initialize in `NewSession`. Inject as steering before LLM calls when non-empty. Add entries on test failure and reviewer rejection. |
| `agent/subagents.go` | Add `FailedApproaches string` to spawn config, prepend to task |

### Configuration

| Field | Default | Description |
|-------|---------|-------------|
| `MaxApproachLogEntries` | 10 | Max entries before oldest dropped |

Always on. No CLI flag needed.

### Testing

- Unit: add/format/overflow behavior
- Unit: `ExtractApproachSummary` from mock tool calls
- Unit: `ClassifyError` for various output patterns
- Integration: verify approach log survives simulated compaction
- Integration: verify reviewer rejection appears in log

### Interactions

- **Fed by Repair Tracker (#1)**: Repair cycle detection triggers approach logging
- **Survives Context Compression (#9)**: Structurally immune to compaction
- **Feeds Parallel Candidates (#6)**: Each candidate gets the full log to avoid repeating failures

---

## 3. Adaptive Stopping / Stagnation Detection

### Motivation (from literature)

The survey (Section 5.1.2.1, p.107-108) repeatedly emphasizes "the need for adaptive stopping criteria." Multiple studies find that excessive iterations lead to diminishing returns and even regression. Prior work notes that resource overheads increase while gains plateau.

This addresses a different failure mode than #1. RepairTracker detects test→edit→test loops specifically. ProgressTracker detects ANY form of spinning — re-reading the same files, running the same commands, making edits that are immediately reverted.

### Current Behavior

- Fixed budget consumed without progress tracking
- No detection of repetitive action patterns
- Agent may read the same file 5+ times, each consuming context
- No early warning before hitting the hard ceiling at round 200

### Proposed Design

Add a `ProgressTracker` that monitors action novelty in a sliding window:

```go
// agent/progress_tracker.go

type ActionFingerprint struct {
    ToolName string
    ArgsHash uint64
}

type ProgressTracker struct {
    mu              sync.Mutex
    windowSize      int             // default 10
    noveltyFloor    float64         // default 0.3
    maxStagnant     int             // default 3
    history         []ActionFingerprint
    stagnantStreak  int
}

func (pt *ProgressTracker) Record(toolName string, args json.RawMessage) {
    pt.mu.Lock()
    defer pt.mu.Unlock()
    pt.history = append(pt.history, ActionFingerprint{
        ToolName: toolName,
        ArgsHash: hashBytes(args),
    })
}

// Check evaluates stagnation. Returns (steeringMessage, shouldForceSubmit).
func (pt *ProgressTracker) Check() (string, bool) {
    pt.mu.Lock()
    defer pt.mu.Unlock()

    if len(pt.history) < pt.windowSize {
        return "", false
    }

    windowStart := len(pt.history) - pt.windowSize
    recent := pt.history[windowStart:]

    // Count actions in this window not seen in any previous window
    prior := make(map[ActionFingerprint]bool)
    for _, fp := range pt.history[:windowStart] {
        prior[fp] = true
    }
    novel := 0
    for _, fp := range recent {
        if !prior[fp] {
            novel++
        }
    }

    noveltyRatio := float64(novel) / float64(pt.windowSize)
    if noveltyRatio >= pt.noveltyFloor {
        pt.stagnantStreak = 0
        return "", false
    }

    pt.stagnantStreak++
    forceSubmit := pt.stagnantStreak >= pt.maxStagnant

    var msg string
    switch pt.stagnantStreak {
    case 1:
        msg = fmt.Sprintf("%.0f%% of your last %d actions are repeats. "+
            "Step back and consider a fundamentally different approach.",
            (1-noveltyRatio)*100, pt.windowSize)
    case 2:
        msg = fmt.Sprintf("WARNING: Continued stagnation. %.0f%% of your last %d actions "+
            "are repeats. Change strategy NOW or submit best-effort.",
            (1-noveltyRatio)*100, pt.windowSize)
    default:
        msg = "CRITICAL: Stagnant for 3+ consecutive windows. Submit immediately."
    }
    return msg, forceSubmit
}
```

**Novelty definition**: An action is "novel" if the `(toolName, argsHash)` pair hasn't appeared in any prior window. Reading a new file = novel. Re-reading the same file = not novel. Editing a file = always novel (args differ). Running the same test command = not novel.

**Escalation**: soft warning → hard warning → force submit.

### Wiring Points

| File | Change |
|------|--------|
| `agent/progress_tracker.go` | New file |
| `agent/session.go` | Add `progressTracker` field. After each `ExecuteCall`: `Record()`. Before LLM call: `Check()`, inject steering. |
| `agent/session.go` — `SessionConfig` | Add `StagnationWindow int` |

### Configuration

| Field | CLI Flag | Default | Description |
|-------|----------|---------|-------------|
| `StagnationWindow` | `--stagnation-window` | 10 | Sliding window for novelty check |
| `StagnationNoveltyFloor` | — | 0.3 | Novelty fraction below which = stagnant |
| `MaxStagnantWindows` | — | 3 | Force submit after N consecutive stagnant windows |

### Testing

- Unit: novelty detection (10 unique = not stagnant, 8 repeats = stagnant)
- Unit: threshold edge case (3/10 novel = not stagnant, 2/10 = stagnant)
- Unit: streak escalation and reset
- Integration: mock session reading same file 15 times → steering at action 10

### Interactions

- **Complements Repair Tracker (#1)**: ProgressTracker catches non-test spinning; RepairTracker catches test loops
- **Feeds Approach Log (#2)**: Stagnation triggers approach logging
- **Reduces context pressure (#9)**: Fewer wasted rounds = less junk to compact

---

# Tier 2 — Correctness

These improvements address reviewer-approved-but-wrong (52-60% of failures).

---

## 4. Specification Inference Before Coding

### Motivation (from literature)

SpecRover (Section 5.1.2.1, p.121) centers on **specification inference** — integrating program structure, behavior, and tests to infer developer intent before generating patches. The key insight: patches generated without an explicit specification tend to be syntactically correct but semantically wrong.

In serf, 52-60% of failures are "reviewer-approved-but-wrong." The implementer, verifier, and reviewer each independently re-interpret the task description, extracting different subsets of requirements. The configure-git-webserver failure class exemplifies this: scripts were written correctly, but nobody extracted the implicit requirement that services must be *running* at evaluation time.

### Current Behavior

The coordinator's "Plan" task already does significant analysis: it reads the task spec, inventories the workspace, identifies acceptance criteria, and writes a delegation prompt. But it has three gaps:

1. **No test-driven spec extraction.** The coordinator inventories files but doesn't read test content. Test files contain the most precise specification — exact inputs, outputs, error conditions.
2. **Implicit requirements are missed.** The Plan task focuses on explicit requirements. Implicit ones (e.g., "deploy a server" implies running) depend on model intuition.
3. **No shared specification artifact.** The coordinator's analysis lives in its conversation context. The verifier re-derives acceptance criteria from the task description.

### Proposed Design

Extend the coordinator's Plan task to produce a structured, checkable specification that flows through to both implementer and verifier/reviewer.

Update `coordinator.md` Plan task to include:

```markdown
  - title: Plan
    prompt: >
      Analyze the task requirements and workspace inventory.

      STEP 1 — Extract test specifications:
      From the inventory, identify test files (test_*, *_test.*, *.test.*,
      Makefile test targets, pytest configs). Read each test file. For each
      test case, extract: what behavior it validates, what inputs it
      provides, what outputs it expects.

      STEP 2 — Derive implicit requirements:
      - "set up/deploy/configure X" → X must be RUNNING and accessible
      - "create/build X" → X must exist AND be functional
      - "fix X" → original bug must not reproduce AND nothing else breaks
      - "install X" → X must be importable/callable

      STEP 3 — Build the SPECIFICATION block:

      SPECIFICATION:
      REQUIREMENTS:
      1. [requirement] — Source: [spec line / test file:function / implicit]

      CONSTRAINTS:
      1. [thing the solution must NOT do or break]

      ACCEPTANCE_CHECKS:
      1. [command to run] → [expected result]

      TEST_COMMANDS:
      - [exact command to run all relevant tests]

      STEP 4 — Include the SPECIFICATION block in the delegation prompt
      and in the verifier's task description.
```

**Verifier receives the specification** via the coordinator's Verify task:

```markdown
  - title: Verify
    prompt: >
      Spawn a verifier. The verifier's task must include the SPECIFICATION
      block from your plan — especially the ACCEPTANCE_CHECKS. Instruct the
      verifier to run every ACCEPTANCE_CHECK and report pass/fail per check.
```

**Reviewer receives the specification** via the coordinator's Submit task:

```markdown
  - title: Submit
    prompt: >
      Include the SPECIFICATION block in your communicate message — the
      reviewer gate uses it to check your work.
```

This is prompt-only. No Go changes needed. The coordinator already has `spawn_agent` capability and passes task descriptions to subagents.

### Wiring Points

| File | Change |
|------|--------|
| `agent/agents/coordinator.md` | Rewrite Plan task to include test-reading and spec extraction. Update Verify and Submit tasks. |
| `agent/agents/verifier.md` | Add instruction to use ACCEPTANCE_CHECKS when provided |
| `agent/agents/reviewer.md` | Add instruction to check against SPECIFICATION block when present |

### Configuration

No new configuration — always on. The specification extraction makes the Plan task slightly longer (~5-10 more tool rounds for reading test files) but should reduce downstream iterations.

### Testing

- A/B eval: 20-task subset with/without spec extraction. Measure reviewer-approved-but-wrong rate.
- Spec quality: For 5 tasks with known test suites, verify the SPECIFICATION block correctly captures test requirements, especially implicit ones.
- Overhead: Measure Plan-step tool rounds with/without spec extraction.

### Interactions

- **Feeds Multi-Dimensional Review (#5)**: SPECIFICATION gives the reviewer concrete ACCEPTANCE_CHECKS
- **Feeds Parallel Candidates (#6)**: Both candidates get the same spec
- **Works with Localization (#7)**: Localization finds files; spec inference reads them

---

## 5. Multi-Dimensional Review

### Motivation (from literature)

HydraReviewer (Table 19, p.113) uses **parallel reviewers per dimension** (logic, readability, security). CodeAgent prevents prompt drift via QA consistency. DeputyDev scales review with expert-level agents per dimension.

The key finding: single-reviewer architectures anchor on one dimension and miss others. In serf, the reviewer gate and the verifier both perform holistic review. The configure-git-webserver class shows the failure: the verifier confirmed scripts parse correctly (code dimension), the reviewer approved (general impression), but neither checked services were running (operational dimension).

### Current Behavior

Serf has two review mechanisms:

1. **Verifier agent** (spawned by coordinator during "Verify" task): Structured VERIFICATION REPORT with PASS/FAIL. Read-only, evidence-gathering. Coordinator reads and decides.
2. **Reviewer gate** (`spawnReviewer`, session.go): Triggered at `communicate`. approve/reject tools. `MaxToolRoundsPerInput=30`, `MaxTurns=20`. Last line of defense.

Both are single-pass, single-dimension. Each anchors on "do tests pass?" and may miss completeness issues.

### Proposed Design

Don't add more reviewer agents. Instead, restructure the reviewer gate's prompt into a mandatory multi-dimensional checklist:

```markdown
# agent/agents/reviewer.md — revised
---
name: reviewer
description: "Verify work against requirements."
tasks:
  - title: Dimension 1 — Test correctness
    prompt: >
      Find and run ALL test suites. Record which passed, which failed,
      exact error output. If ANY test fails, note it.
    reasoning_effort: low
  - title: Dimension 2 — Output correctness
    prompt: >
      Check expected outputs exist and are correct. If task deploys a
      service, verify it is RUNNING NOW — curl endpoints, check ports
      with ss/netstat, verify processes with ps. If ACCEPTANCE_CHECKS
      exist in a SPECIFICATION block, execute each one.
      A script that COULD work is not the same as a service that IS running.
    reasoning_effort: low
  - title: Dimension 3 — Requirement coverage
    prompt: >
      Re-read the task description line by line. For each requirement,
      cite evidence it is satisfied. Check implicit requirements too.
      Produce a checklist:
      - [x] Requirement: [evidence]
      - [ ] Requirement: NOT MET — [what's missing]
    reasoning_effort: low
  - title: Verdict
    prompt: >
      If ALL dimensions pass: approve.
      If ANY dimension fails: reject with ALL issues from ALL dimensions.
    reasoning_effort: low
---
```

**Budget adjustment:**

```go
// In session.go, spawnReviewer:
subCfg.MaxTurns = 25              // was 20
subCfg.MaxToolRoundsPerInput = 40 // was 30
```

~33% increase for the reviewer, but reviewer is a small fraction of total cost.

### Wiring Points

| File | Change |
|------|--------|
| `agent/agents/reviewer.md` | Replace with four-task dimensional checklist |
| `agent/session.go` | Adjust reviewer budget in `spawnReviewer` |

### Configuration

No new configuration. The dimensional checklist is always-on for the reviewer gate (itself gated by `EnableReviewerGate`).

### Testing

- A/B eval: Same tasks with old vs dimensional reviewer. Primary metric: reviewer-approved-but-wrong rate. Target: 60% → below 40%.
- configure-git-webserver: 5 reps, verify Dimension 2 catches "service not running"
- Budget: Verify reviewer rounds stay under 40 on average
- False rejection: Verify currently-passing tasks aren't newly rejected

### Interactions

- **Fed by Spec Inference (#4)**: SPECIFICATION gives the reviewer concrete ACCEPTANCE_CHECKS for Dimension 2
- **Feeds Approach Log (#2)**: Rejection includes which dimension failed
- **Complements verifier**: Verifier catches issues before submit; reviewer gate is the safety net

---

## 6. Parallel Candidate Generation

### Motivation (from literature)

The survey describes a **hybrid search strategy** (Section 5.1.2.1, p.107-108) combining parallel generation with iterative refinement. S* "first samples multiple candidates independently and then improves each one through a few rounds of self-debugging." After refinement, adversarial tests differentiate candidates. Key insight: **breadth + depth outperforms depth alone**.

### Current Behavior

- Coordinator spawns one implementer; if verifier rejects, enters fix loop (up to 3 cycles)
- If reviewer gate rejects, coordinator retries serially
- Each retry operates on the same workspace — no parallel exploration
- `spawnAgent` already supports non-blocking spawns (goroutine in subagents.go:256)
- But concurrent implementers share the same working directory and would clobber each other

### Proposed Design

**After first reviewer-gate rejection**, spawn 2 parallel implementers in isolated git worktrees with diversified approach instructions.

**Workspace isolation:**

```go
// agent/workspace.go

type IsolatedWorkspace struct {
    OriginalDir string
    WorktreeDir string
    BranchName  string
}

func CreateIsolatedWorkspace(originalDir, suffix string) (*IsolatedWorkspace, error) {
    branchName := fmt.Sprintf("serf-attempt-%s-%d", suffix, time.Now().UnixMilli())
    worktreeDir := filepath.Join(originalDir, ".serf-worktrees", branchName)

    cmd := exec.Command("git", "-C", originalDir, "worktree", "add",
        worktreeDir, "-b", branchName, "HEAD")
    if out, err := cmd.CombinedOutput(); err != nil {
        return nil, fmt.Errorf("git worktree add: %w\n%s", err, out)
    }
    return &IsolatedWorkspace{OriginalDir: originalDir, WorktreeDir: worktreeDir, BranchName: branchName}, nil
}

func (ws *IsolatedWorkspace) Cleanup() error {
    exec.Command("git", "-C", ws.OriginalDir, "worktree", "remove", "--force", ws.WorktreeDir).Run()
    exec.Command("git", "-C", ws.OriginalDir, "branch", "-D", ws.BranchName).Run()
    return nil
}

func (ws *IsolatedWorkspace) MergeBack() error {
    exec.Command("git", "-C", ws.WorktreeDir, "add", "-A").Run()
    exec.Command("git", "-C", ws.WorktreeDir, "commit", "--allow-empty", "-m", "serf parallel attempt").Run()
    sha, _ := exec.Command("git", "-C", ws.WorktreeDir, "rev-parse", "HEAD").Output()
    return exec.Command("git", "-C", ws.OriginalDir, "cherry-pick", strings.TrimSpace(string(sha))).Run()
}
```

**New `create_worktree` tool** registered for coordinator:

```go
RegisteredTool{
    Tool: llm.Tool{Definition: llm.ToolDefinition{
        Name:        "create_worktree",
        Description: "Create an isolated git worktree for parallel implementation attempts.",
        Parameters: /* suffix string */,
    }},
    Exec: func(ctx, env, args) (any, error) {
        ws, err := CreateIsolatedWorkspace(env.WorkingDir(), args["suffix"].(string))
        // Track for cleanup on session.worktrees
        return map[string]any{"worktree_dir": ws.WorktreeDir}, nil
    },
}
```

**Coordinator prompt — parallel recovery after rejection:**

```markdown
If your communicate call was rejected AND the approach itself is wrong:
1. Create 2 worktrees with create_worktree
2. Spawn 2 implementers in parallel, each with different working_dir and
   different strategic approach. Both get the SPECIFICATION and approach log.
3. Wait for both. Verify each. Submit the one that passes.
Only use when: (a) approach fundamentally failed, (b) 2 distinct strategies
exist, (c) < 50% of rounds used.
```

**Cleanup:** Worktrees cleaned up in `Session.Close()`.

**Cost model:** 2 parallel attempts × ~75 rounds = 150 rounds. Serial fix loop: ~75 rounds × up to 3 iterations = 225. Parallel is cheaper when the first approach is wrong.

### Wiring Points

| File | Change |
|------|--------|
| `agent/workspace.go` | New file |
| `agent/session.go` | Add `worktrees` field, cleanup in `Close()`, register `create_worktree` tool, add `MaxParallelAttempts` to config |
| `agent/agents/coordinator.md` | Add parallel recovery to Fix task |

### Configuration

| Field | CLI Flag | Default | Description |
|-------|----------|---------|-------------|
| `MaxParallelAttempts` | `--max-parallel-attempts` | 2 | Max worktrees. 0 disables `create_worktree`. |

### Testing

- Unit: CreateIsolatedWorkspace/Cleanup/MergeBack with test git repo
- Integration: two parallel subagents with different working_dir don't interfere
- Eval: 5 tasks that fail first attempt, serial vs parallel retry

### Interactions

- **Depends on Spec Inference (#4)**: Both candidates get the same specification
- **Depends on Approach Log (#2)**: Candidates avoid repeating failed strategies
- **Uses Multi-Dimensional Review (#5)**: Each candidate checked by correctness reviewer

---

# Tier 3 — Efficiency

These improvements reduce wasted rounds and context pressure.

---

## 7. Hierarchical Fault Localization

### Motivation (from literature)

Agentless (Section 5.1.2.1, p.120) demonstrates that **hierarchical localization** — narrowing from repo to file to function to line — achieves competitive results with a simple three-stage flow. No agents needed, just structured narrowing. The key insight: subagents waste rounds re-discovering project structure because the coordinator doesn't give them enough context about where to look.

### Current Behavior

- Coordinator receives task, decomposes into subtasks, spawns subagents
- Each subagent independently discovers project structure, relevant files, test locations
- The coordinator already has an "Inventory" step that lists files, but the information doesn't always flow through to implementers with enough specificity
- The explorer agent exists but its output is sometimes too broad

### Proposed Design

Strengthen the coordinator's localization responsibility and the explorer's output format.

**Update coordinator.md** — make localization explicit in the Plan task:

```markdown
STEP 0 — Localize (for fix/modify tasks, skip for green-field builds):
From the inventory, identify:
- The 1-5 most relevant source files (grep for task keywords)
- Test files and exact test commands
- Config files that may need modification
- Import dependencies of relevant files

Include in the delegation prompt:
RELEVANT_FILES:
- path/to/file.go (purpose: session management, likely contains the bug)
- path/to/file_test.go (run: go test ./path/to/...)
DEPENDENCIES:
- path/to/config.go (imported by file.go, timeout settings)
```

**Update explorer.md** — structured output format:

```markdown
Your output MUST include:
1. PROJECT_STRUCTURE: Top-level directory layout with purpose annotations
2. RELEVANT_FILES: Files related to the task, with one-line descriptions
3. TEST_FILES: Test files and how to run them
4. ENTRY_POINTS: Where to start reading the code
```

**When NOT to localize:** For green-field tasks ("build X from scratch"), localization is less useful. The coordinator prompt should say: "If the task is to BUILD something new, skip STEP 0."

### Wiring Points

| File | Change |
|------|--------|
| `agent/agents/coordinator.md` | Add localization step to Plan task |
| `agent/agents/explorer.md` | Add structured output format |

No Go changes. Pure prompt improvement.

### Configuration

None. Always on.

### Testing

- Measure: average implementer rounds spent on exploration (read_file/glob/grep calls before first edit) with/without localization
- Verify: implementers receive relevant file paths in their task descriptions

### Interactions

- **Feeds Spec Inference (#4)**: Localization finds files; spec inference reads them and extracts requirements
- **Feeds Codebase Index (#8)**: Localization is manual; the index automates it

---

## 8. Codebase Structure Indexing

### Motivation (from literature)

CodexGraph (Section 5.1.2.1, p.121) and KGCompass (p.121) demonstrate that building a structural representation of the codebase improves fault localization and cross-file consistency. Serf navigates codebases purely through grep/glob/read, which is slow and incomplete.

### Current Behavior

- Agents use `grep`, `glob`, `read_file`, `list_dir` to explore
- No persistent representation of codebase structure
- Each subagent re-discovers project layout from scratch
- No understanding of import/dependency relationships

### Proposed Design

Build a **lightweight codebase index** at task start. Not a knowledge graph — a one-shot snapshot computed in <1 second.

```go
// agent/tool_codebase_index.go

type CodebaseIndex struct {
    Language    string              `json:"language"`
    BuildSystem string              `json:"build_system"`
    TestCommand string              `json:"test_command"`
    EntryPoints []string            `json:"entry_points"`
    Directories []DirSummary        `json:"directories"`
    FileCount   int                 `json:"file_count"`
    ImportGraph map[string][]string `json:"import_graph,omitempty"`
}

type DirSummary struct {
    Path      string `json:"path"`
    FileCount int    `json:"file_count"`
    Purpose   string `json:"purpose"` // "tests", "source", "config", "docs"
}
```

**Index construction** (non-LLM, pure heuristics):

```go
func BuildCodebaseIndex(root string) (*CodebaseIndex, error) {
    idx := &CodebaseIndex{}
    idx.Language = detectLanguage(root)       // count .go, .py, .js, .rs files
    idx.BuildSystem = detectBuildSystem(root) // Makefile, package.json, go.mod, etc.
    idx.TestCommand = inferTestCommand(idx.BuildSystem)
    idx.Directories = walkDirs(root, 3)      // max depth 3
    idx.EntryPoints = findEntryPoints(root, idx.Language)
    return idx, nil
}
```

**Registered as a tool:**

```go
tools.Register("codebase_index", ToolDef{
    Description: "Build a structural index of the codebase.",
    Parameters: {"path": string, "include_imports": bool},
    Exec: func(ctx, env, args) (any, error) {
        return BuildCodebaseIndex(args["path"].(string))
    },
})
```

**Auto-injection for non-interactive sessions:**

```go
if s.cfg.NonInteractive && s.cfg.AutoIndex {
    idx, _ := BuildCodebaseIndex(s.env.WorkingDir)
    s.injectSteering(fmt.Sprintf("## Codebase Index\n%s", idx.Format()))
}
```

This saves the coordinator 3-5 rounds of manual exploration at session start.

### Wiring Points

| File | Change |
|------|--------|
| `agent/tool_codebase_index.go` | New file |
| `agent/codebase/` | Language-specific parsers (go.go, python.go, js.go) |
| `agent/session.go` | Auto-injection for non-interactive sessions |
| Tool registration | `registerCoreTools` |

### Configuration

| Field | CLI Flag | Default | Description |
|-------|----------|---------|-------------|
| `AutoIndex` | `--auto-index` / `--no-auto-index` | true (non-interactive), false (interactive) | Auto-inject codebase index at session start |

### Testing

- Unit: `detectLanguage` with various file layouts
- Unit: `detectBuildSystem` identifies Makefile, package.json, go.mod, etc.
- Unit: `inferTestCommand` returns correct command for each build system
- Integration: index a real repo, verify structure is accurate

### Interactions

- **Complements Localization (#7)**: Index provides automated broad context; localization provides task-specific narrowing
- **Reduces context pressure (#9)**: Fewer exploration rounds = less to compact

---

## 9. Context Compression Improvements

### Motivation (from literature)

LongCodeZip (Section 3.3.7, p.59) achieves up to **5.6x compression** through hierarchical, perplexity-based compression. The key insight: different output types deserve different compression strategies.

### Current Behavior

- Layer 3 (80%): Deterministic checkpoint — replaces tool results with per-tool call counts
- Layer 4 (90%): LLM summary — sends history to model for summarization
- `PreserveRecentTurns=6` keeps last 6 turns untouched
- All tool outputs truncated uniformly (head-tail strategy)

### Proposed Design

Three improvements to the existing compaction system:

**A. Smarter tool output truncation (per-tool strategy):**

```go
func smartTruncateToolOutput(toolName, output string) string {
    switch {
    case toolName == "shell" && isTestOutput(output):
        // Keep ONLY summary line + error lines
        return extractTestSummary(output)
    case toolName == "read_file":
        // After processing, keep only path + line count
        return fmt.Sprintf("[Read %s (%d lines)]", extractPath(output), countLines(output))
    case toolName == "grep" || toolName == "glob":
        // Keep file list, drop matched content
        return extractFileList(output)
    case len(output) > 2000:
        return headTail(output, 500, 500)
    default:
        return output
    }
}
```

Wire into checkpoint generation where tool results are summarized.

**B. Progressive output masking at 70% pressure:**

Add a new compaction layer between the current observation masking and checkpoint:

```go
func (cm *ContextManager) maskOldOutputs(history []Turn) {
    recentBoundary := len(history) - cm.PreserveRecentTurns
    for i, turn := range history[:recentBoundary] {
        if turn.Kind == TurnToolResults {
            for j, result := range turn.Results {
                if len(result.Output) > 200 {
                    history[i].Results[j].Output = smartTruncateToolOutput(
                        result.ToolName, result.Output,
                    )
                }
            }
        }
    }
}
```

This reduces pressure earlier, delaying the more aggressive checkpoint compaction.

**C. Relevance-weighted LLM summarization:**

Improve the Layer 4 summarization prompt:

```
Summarize this conversation for an AI coding agent.

PRESERVE with high fidelity:
- Current task and requirements
- Files modified and how
- Test results (which pass/fail and why)
- Error messages and root causes
- Decisions made and rationale

COMPRESS aggressively:
- File contents that were read but not modified
- Successful command outputs (just note "X succeeded")
- Exploratory commands that found nothing useful
- Redundant info (same file read multiple times)

Current task: %s
Active files: %s
```

### Wiring Points

| File | Change |
|------|--------|
| `agent/context_manager.go` | New `smartTruncateToolOutput`, new 70% masking layer, improved summarization prompt |
| `agent/output_classifier.go` | New file: `isTestOutput()`, `extractTestSummary()`, etc. |

### Configuration

| Field | Default | Description |
|-------|---------|-------------|
| `SmartTruncation` | true | Use per-tool truncation strategy |
| `OutputMaskThreshold` | 0.70 | Pressure threshold for output masking |

### Testing

- Unit: `smartTruncateToolOutput` produces correct output for each tool type
- Unit: `isTestOutput` correctly classifies test output vs general output
- Integration: verify masking layer fires at 70% and reduces pressure below checkpoint threshold
- Benchmark: measure token counts before/after smart truncation vs uniform truncation

### Interactions

- **Preserves Approach Log (#2)**: Approach log lives outside history, unaffected
- **Benefits from Loop Discipline (#1-3)**: Fewer wasted rounds = less to compress

---

## 10. Experience Repository

### Motivation (from literature)

SWE-Exp (Section 5.1.2.1, p.121, 137) stores **both successful and failed repair trajectories** in a multi-dimensional experience repository and uses Monte Carlo tree search to learn from them. SE-Agent (p.122) systematically leverages cross-trajectory insights.

We already have the raw material: microtask extraction (`tools/extract_microtasks.py`), replay (`tools/replay_microtask.py`), hundreds of microtask JSON files. But this data never feeds back into the agent at runtime.

### Current Behavior

- Microtask extraction exists — extracts decision points from failures
- Replay exists — replays frozen states with modified prompts
- Prompt-lab runs experiments
- None of this is available to the agent at runtime
- Each eval run starts from zero knowledge about what worked/failed

### Proposed Design

**Step 1: Experience Index (offline, Python)**

Create `tools/build_experience_index.py`:

```json
{
  "task_name": "build-cython-ext",
  "task_category": "compilation",
  "total_attempts": 15,
  "pass_rate": 0.60,
  "common_failure_modes": [
    {
      "mode": "missed_pyx_files",
      "frequency": 4,
      "description": "Agent modified .py but didn't find/compile .pyx Cython extensions",
      "recovery": "grep for *.pyx files, run python setup.py build_ext --inplace"
    }
  ],
  "winning_strategies": [
    "Read setup.py/setup.cfg first",
    "Run existing tests before modifying anything",
    "Check for Cython (.pyx) files alongside Python files"
  ],
  "keywords": ["cython", "build", "extension", "pyx", "setup.py"]
}
```

**Step 2: Experience Injection (runtime, Go)**

```go
// agent/experience.go

type ExperienceEntry struct {
    TaskName       string        `json:"task_name"`
    Category       string        `json:"task_category"`
    CommonFailures []FailureMode `json:"common_failure_modes"`
    Strategies     []string      `json:"winning_strategies"`
    Keywords       []string      `json:"keywords"`
}

type ExperienceIndex struct {
    entries []ExperienceEntry
}

func (idx *ExperienceIndex) Lookup(taskDescription string) []ExperienceEntry {
    // Keyword matching against task description
    // Return top 3 most relevant entries
}
```

Injected as steering at session start:

```markdown
## Relevant Experience

Based on similar past tasks:

**Common failure: missed_pyx_files** (4/15 attempts)
Agent modified .py but didn't find .pyx Cython extensions.
Recovery: grep for *.pyx, run setup.py build_ext --inplace

**Winning strategies for compilation tasks:**
- Read setup.py first
- Run existing tests before modifying
- Check for .pyx files alongside .py files
```

**Step 3: Experience Collection (post-eval)**

After each eval run, update the index:

```bash
python tools/build_experience_index.py --run-dir <archive> --update
```

This creates a feedback loop: eval → failures → experience index → better prompts → eval.

**Contamination risk:** Experience entries reference task *categories* and *patterns*, not specific solutions. "Grep for .pyx files" is generalizable advice. Entries should never contain specific file paths or code from benchmark tasks.

### Wiring Points

| File | Change |
|------|--------|
| `tools/build_experience_index.py` | New Python script |
| `agent/experience.go` | New Go file: index loading and lookup |
| `agent/experience/index.json` | Generated index, committed periodically |
| `agent/session.go` | Load index at startup, inject matching entries as steering |
| `tools/run_eval.py` | Update experience after collection |

### Configuration

| Field | CLI Flag | Default | Description |
|-------|----------|---------|-------------|
| `EnableExperience` | `--enable-experience` | true (eval), false (interactive) | Inject experience entries |
| `ExperienceDir` | `--experience-dir` | `agent/experience/` | Override location |

### Testing

- Unit: keyword matching returns relevant entries
- Unit: index loading from JSON
- Integration: verify experience steering appears in session with matching task
- Eval: compare pass rate on 10 previously-failed tasks with/without experience injection

### Interactions

- **Independent of other improvements**: Experience injection is purely additive
- **Complements Spec Inference (#4)**: Experience provides strategic guidance ("what approach works"), spec inference provides tactical specification ("what the solution must do")

---

# Interaction Map

How improvements feed into each other:

```
                    ┌──────────────────┐
                    │ 10. Experience   │ (independent, additive)
                    └──────────────────┘

 ┌─────────────┐    ┌─────────────────┐    ┌────────────────────┐
 │ 1. Repair   │───▶│ 2. Approach     │───▶│ 6. Parallel        │
 │    Tracker   │    │    Log          │    │    Candidates      │
 └─────────────┘    └─────────────────┘    └────────────────────┘
        │                   ▲                       │
        │                   │                       │
 ┌─────────────┐            │               ┌──────────────┐
 │ 3. Stagnation│───────────┘               │ 4. Spec      │
 │    Detection │                           │    Inference  │
 └─────────────┘                            └──────────────┘
                                                    │
 ┌─────────────┐    ┌─────────────────┐            │
 │ 9. Context  │◀───│ 7. Localization │     ┌──────────────┐
 │    Compress  │    └─────────────────┘     │ 5. Multi-Dim │
 └─────────────┘            ▲               │    Review    │
                            │               └──────────────┘
                    ┌─────────────────┐
                    │ 8. Codebase     │
                    │    Index        │
                    └─────────────────┘
```

Key dependency chains:
- **1 → 2 → 6**: Repair detection feeds approach log feeds parallel diversification
- **4 → 5**: Spec inference feeds dimensional review
- **7 + 8 → 4**: Localization and indexing enable spec inference
- **1 + 3 → 9**: Fewer wasted rounds = less to compress

---

# Implementation Order

Sequenced by dependencies and expected impact:

| Phase | Improvements | Rationale |
|-------|-------------|-----------|
| **Phase 1** | #7 Localization, #4 Spec Inference | Prompt-only changes. Zero Go code. Directly attacks the 52-60% reviewer-approved-but-wrong failure mode. Can A/B test immediately. |
| **Phase 2** | #5 Multi-Dimensional Review, #1 Repair Tracker | Small Go changes + prompt revision. Reviewer improvements pair with spec inference. Repair tracker is self-contained. |
| **Phase 3** | #2 Approach Log, #3 Stagnation Detection | Build on Phase 2's repair tracker. Both are new Go files with session wiring. |
| **Phase 4** | #8 Codebase Index, #9 Context Compression | Efficiency improvements. Independent of correctness tier. Reduce wasted rounds and context pressure. |
| **Phase 5** | #6 Parallel Candidates, #10 Experience Repository | Highest effort. Parallel candidates need worktree infrastructure. Experience needs offline pipeline. Both benefit from all prior improvements being in place. |

Each phase is independently valuable and can be benchmarked before proceeding to the next.
