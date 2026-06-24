# Read-only Nudge Removal Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the agent-side read-only streak nudge that injects `SYSTEM-REMINDER` steering after 5 and 10 consecutive all-read-only tool rounds.

**Architecture:** This is a focused deletion in the session post-tool steering path. The implementation removes the read-only streak detector from `injectPostToolSteering`, deletes its loop-private session state, and keeps all adjacent steering mechanisms in place: loop detection, watch/delegate callback handoff, queued steering drain, and task reminders.

**Tech Stack:** Go, standard `go test`, existing Serf agent package and session lifecycle code.

## Global Constraints

- Work in the isolated worktree: `/home/jesse/git/prime-radiant/serf/.worktrees/remove-read-only-nudge`.
- Remove only the read-only streak nudge in `agent/session_tool_round.go`.
- Do not remove `ReadOnly` metadata from tools.
- Do not remove loop detection or stuck escalation.
- Do not remove task-list reminders or task steering.
- Do not remove `/goal` no-progress behavior.
- Do not remove other `SYSTEM-REMINDER` sources.
- Do not change TUI read-only composer capability UX.
- The user explicitly said negative tests proving this feature was removed are not required.
- Existing adjacent steering tests must keep passing; do not weaken unrelated coverage.

---

## File Structure

- Modify: `agent/session_tool_round.go`
  - Responsibility: post-tool-round steering. Remove only the read-only streak block and update the function comment.
- Modify: `agent/session.go`
  - Responsibility: `Session` state and lock-discipline comments. Remove `readOnlyStreak` and stale comment text.
- No new files.
- No production API changes.
- No config flags.

---

### Task 1: Remove read-only streak nudge implementation

**Files:**
- Modify: `agent/session_tool_round.go:267-327`
- Modify: `agent/session.go:71-80,247-249`

**Interfaces:**
- Consumes: existing `Session.injectPostToolSteering(ctx context.Context, calls []llm.ToolCallData, toolSigs *[]string) (bool, error)`.
- Produces: same method signature and same return semantics. Adjacent features remain in order: loop detection → watch/delegate drain → queued steering drain → task reminders.

- [ ] **Step 1: Inspect the target block**

Run:

```bash
cd /home/jesse/git/prime-radiant/serf/.worktrees/remove-read-only-nudge
sed -n '267,360p' agent/session_tool_round.go
sed -n '71,82p' agent/session.go
sed -n '247,250p' agent/session.go
```

Expected: output shows the read-only streak function comment, the `allReadOnly` block, the two nudge strings, the `readOnlyStreak` comment in the lock-discipline block, and the `readOnlyStreak` field.

- [ ] **Step 2: Edit `agent/session_tool_round.go` comment**

Replace the current `injectPostToolSteering` comment:

```go
// injectPostToolSteering runs the post-tool-round bookkeeping that may inject
// steering before the next model call: it appends the round's tool-call signatures
// to toolSigs and runs loop detection, tracks the read-only streak (nudging at 5 and
// 10 consecutive read-only rounds), drains queued steering messages, and injects any
// task reminder. The bool return asks the turn loop to yield after a watch frame is
// handed to an observer and no callback steering is ready yet.
```

with:

```go
// injectPostToolSteering runs the post-tool-round bookkeeping that may inject
// steering before the next model call: it appends the round's tool-call signatures
// to toolSigs and runs loop detection, drains queued steering messages, and injects
// any task reminder. The bool return asks the turn loop to yield after a watch frame
// is handed to an observer and no callback steering is ready yet.
```

- [ ] **Step 3: Delete the read-only streak block**

In `agent/session_tool_round.go`, delete exactly this block from `injectPostToolSteering`:

```go
	// Read-only streak detection: nudge agent if stuck in analysis paralysis.
	allReadOnly := len(calls) > 0
	for _, call := range calls {
		t := s.reg.Get(call.Name)
		if t == nil || !t.ReadOnly {
			allReadOnly = false
			break
		}
	}
	if allReadOnly {
		s.readOnlyStreak++
	} else {
		s.readOnlyStreak = 0
	}
	switch s.readOnlyStreak {
	case 5:
		nudge := "<SYSTEM-REMINDER>You have spent several turns reading without writing or running anything. Review your current task. If you have enough context to make progress, write code or run a command now. A first attempt you can test and fix is more valuable than more reading.</SYSTEM-REMINDER>"
		if abortErr := s.withResponseSideEffects(ctx, func() {
			s.appendTurn(schema.TurnSteering, llm.User(nudge))
			s.emit(events.EventSteeringInjected, events.SteeringInjectedData{Text: nudge})
		}); abortErr != nil {
			return false, abortErr
		}
	case 10:
		nudge := "<SYSTEM-REMINDER>You have been reading for 10 turns without acting. Stop reading. Write the deliverable file now, even if incomplete. You can iterate after you have something to test.</SYSTEM-REMINDER>"
		if abortErr := s.withResponseSideEffects(ctx, func() {
			s.appendTurn(schema.TurnSteering, llm.User(nudge))
			s.emit(events.EventSteeringInjected, events.SteeringInjectedData{Text: nudge})
		}); abortErr != nil {
			return false, abortErr
		}
	}

```

After deletion, the `drained, err := s.drainPendingWatchSendsReport(ctx)` block must immediately follow loop detection.

- [ ] **Step 4: Edit `agent/session.go` lock-discipline comment**

Replace this comment fragment:

```go
		//   loopDetectionCount, the task* reminder counters, depth, the goalInTurn
		//   flag and kickFunc callback, and the naming name-state. It does NOT guard
		//   reg — the tool.Registry self-synchronizes.
		//   (readOnlyStreak is mutated only by the loop and is intentionally
		//   lock-free / loop-private; loopDetectionCount is taken under mu.)
```

with:

```go
		//   loopDetectionCount, the task* reminder counters, depth, the goalInTurn
		//   flag and kickFunc callback, and the naming name-state. It does NOT guard
		//   reg — the tool.Registry self-synchronizes.
```

- [ ] **Step 5: Delete the `readOnlyStreak` field**

In `agent/session.go`, replace:

```go
		// stuck detection
		loopDetectionCount int // how many times loop detection has fired
		readOnlyStreak     int // consecutive rounds with only read-only tool calls
```

with:

```go
		// stuck detection
		loopDetectionCount int // how many times loop detection has fired
```

- [ ] **Step 6: Format touched Go files**

Run:

```bash
gofmt -w agent/session_tool_round.go agent/session.go
```

Expected: command exits 0 and produces no output.

- [ ] **Step 7: Verify removed tokens are gone from production code**

Run:

```bash
rg -n 'readOnlyStreak|You have spent several turns reading without writing or running anything|You have been reading for 10 turns without acting|analysis paralysis' agent/session_tool_round.go agent/session.go
```

Expected: no output, exit code 1 from `rg` because no matches remain.

- [ ] **Step 8: Verify adjacent preserved features are still present**

Run:

```bash
rg -n 'detectLoop|drainPendingWatchSendsReport|drainSteeringForTurn|maybeInjectTaskReminder' agent/session_tool_round.go
```

Expected: output includes all four symbols in `agent/session_tool_round.go`.

- [ ] **Step 9: Run targeted package tests**

Run:

```bash
go test ./agent
```

Expected: `ok   primeradiant.com/serf/agent`.

If this fails, inspect the first failure. Fix only failures caused by removing the read-only nudge. Do not alter unrelated tests or behavior.

- [ ] **Step 10: Commit implementation**

Run:

```bash
git status --short
git add agent/session_tool_round.go agent/session.go
git commit -m "fix(agent): remove read-only streak nudge"
```

Expected: commit succeeds and includes only the two Go files.

---

### Task 2: Final verification and cleanup

**Files:**
- Verify only: repository state and test output
- No source changes expected unless verification reveals a directly related failure

**Interfaces:**
- Consumes: Task 1 commit that removes `readOnlyStreak` and read-only nudge strings.
- Produces: verified branch ready for user review.

- [ ] **Step 1: Confirm branch history**

Run:

```bash
cd /home/jesse/git/prime-radiant/serf/.worktrees/remove-read-only-nudge
git log --oneline -5
```

Expected: output includes these commits near the top:

```text
fix(agent): remove read-only streak nudge
docs: clarify read-only nudge removal spec
docs: design read-only nudge removal
```

- [ ] **Step 2: Confirm no removed feature tokens remain outside the plan/spec docs**

Run:

```bash
rg -n 'readOnlyStreak|You have spent several turns reading without writing or running anything|You have been reading for 10 turns without acting|analysis paralysis' --glob '!docs/superpowers/specs/2026-06-24-read-only-nudge-removal-design.md' --glob '!docs/superpowers/plans/2026-06-24-read-only-nudge-removal.md'
```

Expected: no output, exit code 1 from `rg`.

- [ ] **Step 3: Run full repository tests**

Run:

```bash
go test ./...
```

Expected: all packages pass. If failures occur, determine whether they are related to the read-only nudge removal. Report unrelated or environment-sensitive failures with exact package/test names and keep the passing `go test ./agent` result as targeted coverage.

- [ ] **Step 4: Inspect final worktree state**

Run:

```bash
git status --short
```

Expected: no uncommitted changes. If verification required a directly related fix, commit that fix with a focused message before reporting completion.

- [ ] **Step 5: Report completion evidence**

Report:

```text
Implemented in worktree: /home/jesse/git/prime-radiant/serf/.worktrees/remove-read-only-nudge
Commits:
- <hash> docs: design read-only nudge removal
- <hash> docs: clarify read-only nudge removal spec
- <hash> fix(agent): remove read-only streak nudge
Verification:
- rg removed tokens: no matches outside spec/plan docs
- go test ./agent: PASS
- go test ./...: PASS or exact unrelated failure details
```

---

## Self-Review

Spec coverage:

- Remove read-only streak tracking: Task 1 Steps 2-5.
- Remove both reminder messages: Task 1 Step 3 and Task 1 Step 7.
- Remove dedicated session state: Task 1 Step 5.
- Remove human-facing UX via transcript append and steering event emission: Task 1 Step 3.
- Preserve adjacent mechanisms: Task 1 Step 8 and package tests.
- Verification without mandatory negative tests: Task 1 Step 9 and Task 2 Steps 2-3.

Placeholder scan: no forbidden placeholder markers, no unspecified tests, and no vague implementation steps.

Type consistency: all names match current code: `injectPostToolSteering`, `readOnlyStreak`, `drainPendingWatchSendsReport`, `drainSteeringForTurn`, `maybeInjectTaskReminder`, `schema.TurnSteering`, `events.EventSteeringInjected`.
