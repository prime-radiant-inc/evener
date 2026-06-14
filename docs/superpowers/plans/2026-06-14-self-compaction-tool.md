# Self-Compaction Tool Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an agent-invoked `compact` tool that pins an agent-authored note verbatim across compactions and steers the summary of the rest, layered over serf's existing automatic compactor.

**Architecture:** The capability is strategy-independent. Agent-triggered compaction runs through `Manager.ForceCompact` (the shared `Manager` seam) at the tool-round tail. An agent-authored `note_to_self` is persisted in `SessionMeta` and re-stamped verbatim into history at every compaction via `runPreCompactHook` (the same once-per-compaction callback that preserves the goal objective). Optional `compaction_instructions` steer the summarizer prompt. A best-effort warning nudge and a raised auto-summary threshold cover the "agent never compacts" case.

**Tech Stack:** Go. Packages: `agent`, `agent/internal/contextmgr`, `agent/schema`, `agent/internal/tool`, `cmd/serf-tui`. Test with `go test`.

**Spec:** `docs/superpowers/specs/2026-06-14-self-compaction-tool-design.md` (read it; this plan implements it).

**Working directory:** the worktree `.claude/worktrees/self-compaction-tool` (branch `self-compaction-tool`). Run all commands there.

**Key existing code to read before starting:**
- `agent/internal/contextmgr/context_manager.go` — `Manager` struct (`:40-82`), `NewManager` (`:72`), `MaybeCompact` (`:281`), `ForceCompact` (`:368`), `summarizeWithLLM` (`:1006`, prompt prefix `:1081`).
- `agent/session_compaction.go` — `Compact` (`:19`), `compactionEmitFunc` + `preCompactRan` latch (`:47`), `runPreCompactHook` (`:70`), `appendSteeringMessagesToHistory` (`:88`).
- `agent/session_lifecycle.go` — the round loop (`:536-706`); the seam is after `injectPostToolSteering` (`:693`), before `deliverIfCommunicated` (`:703`).
- `agent/session_tool_registry.go` — `toolDeps` (`:24`), `newToolDeps` (`:138`), `registerCoreTools` (`:219`).
- `agent/session_tools_goal.go` — the canonical tool definition + registration idiom (`:16-69`).
- `agent/session_state.go:33` (`Meta()` builder, Goal at `:65`); `agent/session_init.go:394` (goal restore); `agent/schema/snapshot.go:15` (`SessionMeta`).

**Conventions:** name by domain behavior; comments explain WHAT/WHY; match surrounding style; no backward-compat shims; commit after each green step. NEVER skip a pre-commit hook.

---

## Phase A — Manager: steered summarization, ForceCompact signature, thresholds

### Task 1: New `WarnThreshold` field + raise `SummarizeThreshold` to 0.95

**Files:**
- Modify: `agent/internal/contextmgr/context_manager.go` (`Manager` struct `:55-58`, `NewManager` `:72-82`)
- Test: `agent/context_strategy_test.go` (scaled-defaults assertion `:153`)

- [ ] **Step 1: Update the failing default-threshold test first**

In `agent/context_strategy_test.go` the scaled-defaults test asserts `SummarizeThreshold == 0.45` (`0.90 * 0.5`). Change that expectation to `0.475` (`0.95 * 0.5`). Add an assertion that `WarnThreshold` defaults to `0.75`. Find the `check("SummarizeThreshold", ...)` line and the surrounding block; add `check("WarnThreshold", cm.WarnThreshold, ...)` mirroring the others (raw default 0.75; under the 0.5 scale it is 0.375).

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./agent/ -run TestContextStrategy -v` (use the actual test name at `context_strategy_test.go:153`)
Expected: FAIL — `WarnThreshold` field does not exist; `SummarizeThreshold` is still 0.90.

- [ ] **Step 3: Add the field and defaults**

In the `Manager` struct, alongside `CheckpointThreshold` / `SummarizeThreshold`:

```go
	CheckpointThreshold      float64
	SummarizeThreshold       float64
	// WarnThreshold is the pressure fraction at which the Session nudges the
	// agent to self-compact at its next clean seam (best-effort; the checkpoint
	// and summary layers remain the guarantee).
	WarnThreshold float64
```

In `NewManager`:

```go
		CheckpointThreshold:      0.80,
		SummarizeThreshold:       0.95,
		WarnThreshold:            0.75,
		PreserveRecentTurns:      6,
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./agent/ -run TestContextStrategy -v`
Expected: PASS

- [ ] **Step 5: Confirm `ApplyThresholdScale` covers the new field**

Read `ApplyThresholdScale` (`context_manager.go:205`). If it scales each threshold explicitly, add `cm.WarnThreshold *= scale`. Run `go test ./agent/internal/contextmgr/ ./agent/ -run Threshold -v`; expected PASS.

- [ ] **Step 6: Commit**

```bash
git add agent/internal/contextmgr/context_manager.go agent/context_strategy_test.go
git commit -m "feat(contextmgr): add WarnThreshold, raise SummarizeThreshold default to 0.95"
```

---

### Task 2: Steered summarizer — `summarizeWithLLMSteered` + instruction-led prompt

**Files:**
- Modify: `agent/internal/contextmgr/context_manager.go` (`summarizeWithLLM` `:1006`, prompt prefix `:1081`)
- Test: `agent/internal/contextmgr/context_manager_test.go`

- [ ] **Step 1: Write the failing test**

The summarizer prompt must change shape when instructions are present. Add a test that builds a `Manager` (no client needed — we test prompt assembly via a small extracted helper). Extract the prompt construction into a pure function `buildSummaryPrompt(historyText, instructions string) string` and test it directly:

```go
func TestBuildSummaryPrompt_NoInstructions(t *testing.T) {
	p := buildSummaryPrompt("User: hi\n", "")
	if !strings.Contains(p, "Your summary MUST include ALL of the following sections") {
		t.Fatal("default prompt should mandate the standard sections")
	}
	if strings.Contains(p, "CALLER INSTRUCTIONS") {
		t.Fatal("no caller-instruction block expected when instructions empty")
	}
}

func TestBuildSummaryPrompt_WithInstructions(t *testing.T) {
	p := buildSummaryPrompt("User: hi\n", "Drop the vendored build logs; keep the migration plan verbatim.")
	if !strings.Contains(p, "Drop the vendored build logs") {
		t.Fatal("caller instructions must appear in the prompt")
	}
	// Instruction-led variant governs: the agent's keep/drop directive is the
	// governing instruction, not an addendum after a MUST-include-all block.
	if strings.Contains(p, "Your summary MUST include ALL of the following sections") {
		t.Fatal("the mandatory-7-sections block must be replaced, not retained, when instructions are present")
	}
	if !strings.Contains(p, "CALLER INSTRUCTIONS (these take precedence)") {
		t.Fatal("expected the instruction-led header")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./agent/internal/contextmgr/ -run TestBuildSummaryPrompt -v`
Expected: FAIL — `buildSummaryPrompt` undefined.

- [ ] **Step 3: Extract `buildSummaryPrompt` and add the instruction-led variant**

Pull the existing `prefix` string (`:1081-1122`) into a function. When `instructions != ""`, emit an instruction-led prompt that makes the caller directive governing and drops the "MUST include ALL sections / include too much" mandate:

```go
func buildSummaryPrompt(historyText, instructions string) string {
	if instructions != "" {
		return `You are performing a CONTEXT CHECKPOINT COMPACTION for an agent continuing its own work.

## CALLER INSTRUCTIONS (these take precedence)
` + instructions + `

Follow the caller instructions above when deciding what to preserve verbatim and what to drop or condense. Where they conflict with the general guidance below, the caller instructions win. Still produce a coherent handoff: keep decisions, current state, and actionable next steps. Do not invent content.

` + historyText
	}
	return defaultSummaryPrefix + historyText
}
```

Move the original prefix string to a package var `defaultSummaryPrefix`. Replace `prompt := prefix + b.String()` in the summarizer with `prompt := buildSummaryPrompt(b.String(), instructions)`.

- [ ] **Step 4: Add the steered signature; keep `summarizeWithLLM` as a wrapper**

Rename `summarizeWithLLM` to `summarizeWithLLMSteered(ctx, history, preserveRecent int, instructions string)`. Add a thin wrapper so existing callers (`MaybeCompact:334`, `strategy_session_log.go:134`, `strategy_checkpoint_pred.go:137`, and tests) are unchanged:

```go
func (cm *Manager) summarizeWithLLM(ctx context.Context, history []schema.Turn, preserveRecent int) ([]schema.Turn, error) {
	return cm.summarizeWithLLMSteered(ctx, history, preserveRecent, "")
}
```

- [ ] **Step 5: Run to verify all green**

Run: `go test ./agent/internal/contextmgr/ -v`
Expected: PASS (the new tests and all existing summarizer tests).

- [ ] **Step 6: Commit**

```bash
git add agent/internal/contextmgr/context_manager.go agent/internal/contextmgr/context_manager_test.go
git commit -m "feat(contextmgr): instruction-led summarizer prompt via summarizeWithLLMSteered"
```

---

### Task 3: `ForceCompact(instructions)` returns whether a summary ran

**Files:**
- Modify: `agent/internal/contextmgr/context_manager.go` (`ForceCompact` `:368`)
- Modify callers: `agent/session_compaction.go:31`, `agent/session_model_call.go` (content-filter recovery), and `ForceCompact` test callsites in `agent/internal/contextmgr/context_manager_test.go` / `compaction_kind_test.go`
- Test: `agent/internal/contextmgr/context_manager_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestForceCompact_ReportsSummarized(t *testing.T) {
	cm := NewManager(testProfile(t), nil) // nil client → no summary layer
	hist := makeTurns(t, 3)               // shorter than PreserveRecentTurns
	summarized := cm.ForceCompact(context.Background(), &hist, "", func(events.EventKind, events.EventData) {})
	if summarized {
		t.Fatal("no client and short history → no summary; should report false")
	}
}
```

(Use the test helpers already in the package for `testProfile`/`makeTurns`; if names differ, match the file's existing helpers.)

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./agent/internal/contextmgr/ -run TestForceCompact_ReportsSummarized -v`
Expected: FAIL — `ForceCompact` does not take `instructions` and returns nothing.

- [ ] **Step 3: Change the signature and return**

```go
func (cm *Manager) ForceCompact(
	ctx context.Context,
	history *[]schema.Turn,
	instructions string,
	emitFn func(events.EventKind, events.EventData),
) (summarized bool) {
```

In Layer 2, call `cm.summarizeWithLLMSteered(ctx, *history, cm.PreserveRecentTurns, instructions)`. Set `summarized = true` only when the summary layer actually replaced history (i.e. `err == nil` AND the returned slice differs in length from the input — because `summarizeWithLLMSteered` returns the input unchanged on the `len(history) <= preserveRecent` / `safeCutoff < 0` no-op paths). Capture the pre-call length and compare:

```go
	if cm.client != nil {
		before := len(*history)
		result, err := cm.summarizeWithLLMSteered(ctx, *history, cm.PreserveRecentTurns, instructions)
		if err != nil {
			emitFn(events.EventWarning, events.WarningData{Message: "LLM summarization failed: " + err.Error()})
		} else {
			summarized = len(result) != before
			*history = result
			// ... existing emit + OnCompactionTurn ...
		}
	}
	return summarized
```

- [ ] **Step 4: Update the three production/`test` callers**

- `agent/session_compaction.go:31`: `s.contextMgr.ForceCompact(ctx, &histCopy, "", emitFn)` (capture or ignore the bool; `Compact` keeps returning `error`).
- The content-filter recovery in `agent/session_model_call.go` (the `handleModelError` path that calls `ForceCompact`): pass `""`.
- All `ForceCompact(` callsites in `agent/internal/contextmgr/context_manager_test.go` and `compaction_kind_test.go`: add the `""` argument. `grep -rn "ForceCompact(" agent/` to find them all.

- [ ] **Step 5: Run to verify green**

Run: `go test ./agent/... -run Compact -v && go build ./...`
Expected: PASS + clean build.

- [ ] **Step 6: Commit**

```bash
git add agent/internal/contextmgr/context_manager.go agent/session_compaction.go agent/session_model_call.go agent/internal/contextmgr/*_test.go
git commit -m "feat(contextmgr): ForceCompact takes instructions, returns whether a summary ran"
```

---

## Phase B — Session state and the `compact` tool

### Task 4: Session self-compaction state + writers + one-per-round guard

**Files:**
- Create: `agent/session_self_compact.go`
- Modify: `agent/session.go` (struct fields, near `history`/`mu` `:70-82`)
- Test: `agent/session_self_compact_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestSetPinnedNote_AndClear(t *testing.T) {
	s := newTestSession(t)
	s.setPinnedNote("remember the API signature")
	if got := s.PinnedNote(); got != "remember the API signature" {
		t.Fatalf("note not stored: %q", got)
	}
	s.setPinnedNote("") // empty clears
	if got := s.PinnedNote(); got != "" {
		t.Fatalf("empty note should clear: %q", got)
	}
}

func TestRequestForceCompact_OnePerRound(t *testing.T) {
	s := newTestSession(t)
	if err := s.requestForceCompact("drop logs"); err != nil {
		t.Fatalf("first request should succeed: %v", err)
	}
	if err := s.requestForceCompact("drop more"); err == nil {
		t.Fatal("second request in the same round must error")
	}
	instr, ok := s.takeForceRequest() // consumes + resets the per-round guard
	if !ok || instr != "drop logs" {
		t.Fatalf("takeForceRequest = %q,%v", instr, ok)
	}
	if err := s.requestForceCompact("next round"); err != nil {
		t.Fatalf("after consume, a new request should succeed: %v", err)
	}
}
```

(Use the package's existing `newTestSession` helper; if it does not exist, build a minimal `Session` via the same path other `agent` unit tests use.)

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./agent/ -run 'TestSetPinnedNote|TestRequestForceCompact' -v`
Expected: FAIL — methods undefined.

- [ ] **Step 3: Add fields and methods**

In `agent/session.go`, near the history/mu fields, add (all guarded by `s.mu`):

```go
	pinnedNote          string // agent-authored note, re-stamped verbatim at every compaction
	pendingInstructions string // compaction_instructions awaiting the round-tail force
	forceRequested      bool   // a compact tool call is pending this round
	nudgedSinceCompact  bool   // warning-nudge latch; reset on any compaction
```

In `agent/session_self_compact.go`:

```go
package agent

import "errors"

func (s *Session) setPinnedNote(note string) {
	s.mu.Lock()
	s.pinnedNote = note
	s.mu.Unlock()
}

// PinnedNote returns the current agent-authored note (empty if none).
func (s *Session) PinnedNote() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pinnedNote
}

// requestForceCompact records that the compact tool asked for a compaction at the
// round tail. One per round: a second request before takeForceRequest consumes the
// first is an error so distinct intents are never silently clobbered.
func (s *Session) requestForceCompact(instructions string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.forceRequested {
		return errors.New("a compaction is already pending this round")
	}
	s.forceRequested = true
	s.pendingInstructions = instructions
	return nil
}

// takeForceRequest consumes a pending force request (called once at the round tail).
func (s *Session) takeForceRequest() (instructions string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.forceRequested {
		return "", false
	}
	instructions = s.pendingInstructions
	s.forceRequested = false
	s.pendingInstructions = ""
	return instructions, true
}
```

- [ ] **Step 4: Run to verify green**

Run: `go test ./agent/ -run 'TestSetPinnedNote|TestRequestForceCompact' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add agent/session.go agent/session_self_compact.go agent/session_self_compact_test.go
git commit -m "feat(agent): session pinned-note state + one-per-round force-compact request"
```

---

### Task 5: The `compact` core tool

**Files:**
- Create: `agent/session_tools_compact.go`
- Modify: `agent/session_tool_registry.go` (`toolDeps` `:24`, `newToolDeps` `:138`, `registerCoreTools` `:219`)
- Test: `agent/session_tools_compact_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestCompactTool_PinsAndRequests(t *testing.T) {
	s := newTestSession(t)
	reg := s.reg // or however the test session exposes its registry
	rt, ok := reg.Lookup("compact")
	if !ok {
		t.Fatal("compact tool not registered")
	}
	_, err := rt.Exec(context.Background(), nil, map[string]any{
		"note_to_self":            "keep the migration plan",
		"compaction_instructions": "drop the build logs",
	})
	if err != nil {
		t.Fatalf("compact exec: %v", err)
	}
	if s.PinnedNote() != "keep the migration plan" {
		t.Fatal("note not pinned")
	}
	instr, ok := s.takeForceRequest()
	if !ok || instr != "drop the build logs" {
		t.Fatalf("force not requested with instructions: %q %v", instr, ok)
	}
}

func TestCompactTool_ClearNote_NoForce(t *testing.T) {
	s := newTestSession(t)
	s.setPinnedNote("old")
	rt, _ := s.reg.Lookup("compact")
	if _, err := rt.Exec(context.Background(), nil, map[string]any{"note_to_self": ""}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if s.PinnedNote() != "" {
		t.Fatal("empty note should clear")
	}
	if _, ok := s.takeForceRequest(); ok {
		t.Fatal("clearing a note must not force a compaction")
	}
}
```

(Match the test session's real registry accessor and `Registry.Lookup` name — read `agent/internal/tool/registry.go`.)

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./agent/ -run TestCompactTool -v`
Expected: FAIL — tool not registered.

- [ ] **Step 3: Add `toolDeps` forwarders**

In `toolDeps` (`session_tool_registry.go:24`), add:

```go
	// setPinnedNote stores/clears the agent's note_to_self (empty clears).
	setPinnedNote func(note string)
	// requestForceCompact schedules a round-tail forced compaction; errors on a
	// second request in the same round.
	requestForceCompact func(instructions string) error
	// pressure returns the current context-pressure fraction for the tool's
	// synchronous prediction. Forwards to the Session's pressure estimate.
	pressure func() float64
```

In `newToolDeps` (`:138`):

```go
		setPinnedNote:       s.setPinnedNote,
		requestForceCompact: s.requestForceCompact,
		pressure:            s.currentPressure, // see note below
```

If the Session has no `currentPressure` helper, add one in `session_self_compact.go` that calls `s.contextMgr.Pressure(historyCopy, sysPromptChars)` consistent with how `prepareModelRequest` computes it; for the tool's prediction a rough estimate is fine — read how `EstimatePressure`/`Pressure` are called at `session_model_call.go:145`.

- [ ] **Step 4: Define and register the tool**

`agent/session_tools_compact.go`:

```go
package agent

import (
	"context"
	"fmt"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/llm"
)

func defCompact() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name: "compact",
		Description: "Compact your own context at a clean stopping point — between tasks, " +
			"after extracting results from a large context, before consuming substantial new " +
			"input, or before a complex multi-step operation. Your note_to_self is preserved " +
			"verbatim across compactions; pass an empty note_to_self to clear a stale note. " +
			"compaction_instructions (optional) steer what the summary keeps vs. drops. " +
			"In sessions without persistence, dropped detail is NOT recoverable, so be " +
			"conservative about what you instruct to drop.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"note_to_self": map[string]any{
					"type":        "string",
					"description": "Durable structured keep, preserved verbatim. Empty string clears the note.",
				},
				"compaction_instructions": map[string]any{
					"type":        "string",
					"description": "Optional: what the summary should preserve vs. drop.",
				},
			},
			"required": []string{"note_to_self"},
		},
	}
}

func registerCompactTool(reg *tool.Registry, deps *toolDeps) {
	_ = reg.Register(tool.RegisteredTool{
		Tool: llm.Tool{Definition: defCompact()},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			_ = env
			note, _ := args["note_to_self"].(string)
			instructions, _ := args["compaction_instructions"].(string)

			deps.setPinnedNote(note)

			// Clearing a note does not force a compaction.
			if note == "" && instructions == "" {
				return tool.StateResult{Output: "Note cleared. No compaction requested."}, nil
			}

			if err := deps.requestForceCompact(instructions); err != nil {
				return nil, fmt.Errorf("compact: %w", err)
			}

			// The compaction runs at the round tail, AFTER this returns — so the
			// confirmation is a prediction from the current pressure, not an
			// outcome report. Never phrase it past-tense.
			return tool.StateResult{Output: predictionMessage(deps.pressure())}, nil
		},
	})
}

func predictionMessage(pressure float64) string {
	if pressure < 0.30 {
		return "Note pinned. Context is light, so little or nothing will be condensed at the seam; your note carries forward."
	}
	return "Note pinned. A compaction will run at the seam, honoring your instructions; your note is preserved verbatim."
}
```

Note: the `0.30` boundary is a heuristic for the prediction text only; it does not gate the actual compaction. Keep the message honest ("will run at the seam"), never past-tense ("ran").

In `registerCoreTools` (`:219`), add after `registerGoalTools(reg, deps)`:

```go
	registerCompactTool(reg, deps)
```

- [ ] **Step 5: Run to verify green**

Run: `go test ./agent/ -run TestCompactTool -v && go build ./...`
Expected: PASS + clean build.

- [ ] **Step 6: Commit**

```bash
git add agent/session_tools_compact.go agent/session_tool_registry.go agent/session_tools_compact_test.go
git commit -m "feat(agent): register the agent-invoked compact tool"
```

---

### Task 6: Round-tail force hook

**Files:**
- Modify: `agent/session_lifecycle.go` (after `injectPostToolSteering` `:693`, before `deliverIfCommunicated` `:703`)
- Create the hook: `agent/session_self_compact.go` (add `applyPendingForceCompact`)
- Test: `agent/session_self_compact_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestApplyPendingForceCompact_CompactsWithNoteAndInstructions(t *testing.T) {
	s := newTestSession(t)
	// seed enough history that a compaction is meaningful
	seedHistory(t, s, 12)
	s.setPinnedNote("REMEMBER: API is Foo(ctx, id)")
	if err := s.requestForceCompact("drop the file dumps"); err != nil {
		t.Fatal(err)
	}
	s.applyPendingForceCompact(context.Background())
	if _, ok := s.takeForceRequest(); ok {
		t.Fatal("force request should be consumed")
	}
	// the note must be present verbatim after compaction
	if !historyContainsSteering(s, "[NOTE TO SELF]") || !historyContainsSteering(s, "REMEMBER: API is Foo(ctx, id)") {
		t.Fatal("pinned note not re-stamped into history after force compaction")
	}
}
```

(`seedHistory`/`historyContainsSteering` are small test helpers — write them in the test file, iterating `s.history` under `s.mu`.)

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./agent/ -run TestApplyPendingForceCompact -v`
Expected: FAIL — `applyPendingForceCompact` undefined.

- [ ] **Step 3: Implement the hook (mirrors `Session.Compact`)**

In `agent/session_self_compact.go`:

```go
// applyPendingForceCompact runs an agent-requested compaction at the tool-round
// tail. It mirrors Session.Compact but threads the agent's instructions and runs
// only when a request is pending. The pinned note is re-stamped inside the
// compaction via runPreCompactHook (see Task 7), so no post-call append is needed.
func (s *Session) applyPendingForceCompact(ctx context.Context) {
	instructions, ok := s.takeForceRequest()
	if !ok || s.contextMgr == nil {
		return
	}
	s.contextMgr.Meta = s.buildCompactionMeta()

	s.mu.Lock()
	histCopy := append([]schema.Turn{}, s.history...)
	s.mu.Unlock()

	emitFn, flush := s.compactionEmitFunc(ctx, &histCopy)
	s.contextMgr.ForceCompact(ctx, &histCopy, instructions, emitFn)
	flush()

	s.mu.Lock()
	s.history = histCopy
	s.nudgedSinceCompact = false // reset the nudge latch on any compaction
	s.mu.Unlock()

	s.maybeAutoSave()
}
```

Add the `schema` import.

- [ ] **Step 4: Wire it into the round loop**

In `agent/session_lifecycle.go`, between `injectPostToolSteering` (`:693-695`) and the `deliverIfCommunicated` check (`:703`):

```go
		if steerErr := s.injectPostToolSteering(ctx, calls, &toolSigs); steerErr != nil {
			return "", progressed, steerErr
		}

		// Agent-requested self-compaction runs here: after AfterAction/steering
		// (so a strategy's AfterAction observes pre-compaction history) and before
		// delivery (so a compact+communicate round does not defer to the next user turn).
		s.applyPendingForceCompact(ctx)

		// ... existing round-timings emit ...

		if done, text := s.deliverIfCommunicated(ctx); done {
			return text, progressed, nil
		}
```

- [ ] **Step 5: Run to verify green**

Run: `go test ./agent/ -run 'TestApplyPendingForceCompact|TestCompactTool' -v && go build ./...`
Expected: PASS (the note assertion may still fail until Task 7 lands the re-stamp — if so, mark this test `t.Skip` with a TODO referencing Task 7, OR sequence Task 7 before this step's note assertion. Recommended: land Task 7 first, then this assertion passes.)

- [ ] **Step 6: Commit**

```bash
git add agent/session_lifecycle.go agent/session_self_compact.go agent/session_self_compact_test.go
git commit -m "feat(agent): run agent-requested compaction at the tool-round tail"
```

---

## Phase C — Note survival across compaction

### Task 7: Re-stamp the pinned note inside `runPreCompactHook`

**Files:**
- Modify: `agent/session_compaction.go` (`runPreCompactHook` `:70`)
- Test: `agent/session_compaction_test.go` (or the file holding compaction tests)

- [ ] **Step 1: Write the failing test**

```go
func TestRunPreCompactHook_StampsNoteBeforeObjective(t *testing.T) {
	s := newTestSession(t)
	s.setPinnedNote("REMEMBER: do X")
	setActiveGoal(t, s, "Ship the feature") // helper that sets an active goal

	hist := seedTurns(t, 10)
	s.runPreCompactHook(context.Background(), &hist)

	noteIdx := indexOfSteering(hist, "[NOTE TO SELF]")
	goalIdx := indexOfSteering(hist, "Ship the feature")
	if noteIdx < 0 {
		t.Fatal("note not stamped")
	}
	if goalIdx >= 0 && noteIdx > goalIdx {
		t.Fatal("note must precede the goal objective (objective stays trailing)")
	}
}

func TestRunPreCompactHook_NoDuplicateNote(t *testing.T) {
	s := newTestSession(t)
	s.setPinnedNote("REMEMBER: do X")
	hist := seedTurns(t, 10)
	s.runPreCompactHook(context.Background(), &hist) // first stamp
	s.runPreCompactHook(context.Background(), &hist) // second stamp must not duplicate
	if n := countSteering(hist, "[NOTE TO SELF]"); n != 1 {
		t.Fatalf("expected exactly one note turn, got %d", n)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./agent/ -run TestRunPreCompactHook -v`
Expected: FAIL — note not stamped.

- [ ] **Step 3: Strip any existing note, then insert it before the goal**

Add a marker helper and modify `runPreCompactHook`:

```go
const (
	pinnedNoteOpen  = "[NOTE TO SELF]"
	pinnedNoteClose = "[END NOTE TO SELF]"
)

func renderPinnedNote(note string) string {
	return pinnedNoteOpen + "\n" + note + "\n" + pinnedNoteClose
}

// stripPinnedNoteTurns removes any existing pinned-note steering turn so a fresh
// copy can be re-stamped without accumulation.
func stripPinnedNoteTurns(history *[]schema.Turn) {
	filtered := (*history)[:0]
	for _, t := range *history {
		if t.Kind == schema.TurnSteering && strings.Contains(t.Message.Text(), pinnedNoteOpen) {
			continue
		}
		filtered = append(filtered, t)
	}
	*history = filtered
}
```

In `runPreCompactHook`, after the plugin messages and before `goalCompactionSteering`, slot the note into `messages` and strip the old turn first:

```go
func (s *Session) runPreCompactHook(ctx context.Context, history *[]schema.Turn) []steeringTurnRecord {
	if history == nil {
		return nil
	}
	var messages []string
	if s.hookRunner != nil {
		// ... existing plugin block unchanged ...
	}
	if note := s.PinnedNote(); note != "" {
		stripPinnedNoteTurns(history)           // remove a prior copy
		messages = append(messages, renderPinnedNote(note))
	}
	messages = append(messages, s.goalCompactionSteering()...) // objective stays last
	return appendSteeringMessagesToHistory(history, messages)
}
```

Add `"strings"` to imports if not present (it is — `session_compaction.go` already imports `strings`).

- [ ] **Step 4: Run to verify green**

Run: `go test ./agent/ -run 'TestRunPreCompactHook|TestApplyPendingForceCompact' -v`
Expected: PASS. Re-run Task 6's note assertion — it now passes; remove any `t.Skip`.

- [ ] **Step 5: Add a verbatim-survival-through-summarize test**

```go
func TestNote_SurvivesForceCompactVerbatim(t *testing.T) {
	s := newTestSession(t) // with a real cheap summarization client configured, OR nil client (checkpoint only)
	seedHistory(t, s, 14)
	s.setPinnedNote("REMEMBER: API is Foo(ctx, id)")
	_ = s.requestForceCompact("")
	s.applyPendingForceCompact(context.Background())
	if countSteering(currentHistory(s), "REMEMBER: API is Foo(ctx, id)") != 1 {
		t.Fatal("note must appear exactly once, verbatim, after compaction")
	}
}
```

Run: `go test ./agent/ -run TestNote_Survives -v`; Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add agent/session_compaction.go agent/session_compaction_test.go
git commit -m "feat(agent): re-stamp pinned note (before goal objective) at every compaction"
```

---

## Phase D — Persistence and resume

### Task 8: Persist `pinnedNote` in `SessionMeta`; restore on resume

**Files:**
- Modify: `agent/schema/snapshot.go` (`SessionMeta` `:15-53`)
- Modify: `agent/session_state.go` (`Meta()` builder `:33-65`)
- Modify: `agent/session_init.go` (restore path near goal restore `:394`)
- Test: `agent/session_self_compact_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestPinnedNote_PersistsAndRestores(t *testing.T) {
	s := newTestSession(t)
	s.setPinnedNote("REMEMBER: resume me")
	meta := s.Meta()
	if meta.PinnedNote != "REMEMBER: resume me" {
		t.Fatalf("meta.PinnedNote = %q", meta.PinnedNote)
	}
	// round-trip through JSON to prove the tag works
	b, _ := json.Marshal(meta)
	var back schema.SessionMeta
	_ = json.Unmarshal(b, &back)
	if back.PinnedNote != "REMEMBER: resume me" {
		t.Fatalf("round-trip lost the note: %q", back.PinnedNote)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./agent/ -run TestPinnedNote_PersistsAndRestores -v`
Expected: FAIL — `SessionMeta.PinnedNote` does not exist.

- [ ] **Step 3: Add the field**

In `agent/schema/snapshot.go`, in `SessionMeta` after `Goal`:

```go
	// PinnedNote is the agent's self-compaction note_to_self, persisted so it
	// survives daemon restart and serf resume (mirrors Goal).
	PinnedNote string `json:"pinned_note,omitempty"`
```

- [ ] **Step 4: Populate it in `Meta()` and restore it on resume**

In `agent/session_state.go` `Meta()` builder (near `Goal: s.goalSnapshotForMeta()` `:65`):

```go
		Goal:            s.goalSnapshotForMeta(),
		PinnedNote:      s.PinnedNote(),
```

In `agent/session_init.go`, next to the goal restore (`:394`), restore the note directly (no history scan):

```go
	if meta.Goal != nil {
		g := meta.Goal
		s.getOrCreateGoalStore().Restore(/* ...existing... */)
	}
	s.pinnedNote = meta.PinnedNote
```

(Read the surrounding restore function to place this where `meta` is in scope and `s` is the session being initialised.)

- [ ] **Step 5: Run to verify green; add a resume round-trip test**

```go
func TestPinnedNote_SurvivesResume(t *testing.T) {
	dir := t.TempDir()
	s1 := newPersistentTestSession(t, dir)
	s1.setPinnedNote("REMEMBER: across restart")
	seedHistory(t, s1, 14)
	_ = s1.requestForceCompact("")
	s1.applyPendingForceCompact(context.Background()) // persists meta via maybeAutoSave
	s1.Close()

	s2 := resumeTestSession(t, dir, s1.ID())
	if s2.PinnedNote() != "REMEMBER: across restart" {
		t.Fatalf("note not restored: %q", s2.PinnedNote())
	}
	// a fresh compaction re-stamps the restored note
	_ = s2.requestForceCompact("")
	s2.applyPendingForceCompact(context.Background())
	if countSteering(currentHistory(s2), "across restart") != 1 {
		t.Fatal("restored note not re-stamped on next compaction")
	}
}
```

(Use the package's existing persistent-session / resume test helpers; read an existing resume test for the exact helper names.)

Run: `go test ./agent/ ./agent/schema/ -run 'PinnedNote' -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add agent/schema/snapshot.go agent/session_state.go agent/session_init.go agent/session_self_compact_test.go
git commit -m "feat(agent): persist pinnedNote in SessionMeta and restore on resume"
```

---

## Phase E — Warning nudge and unified pressure

### Task 9: Best-effort warning nudge at `WarnThreshold`

**Files:**
- Modify: `agent/session_model_call.go` (pressure evaluation site `:145`; `maybeWarnContextUsage` `:19-47`)
- Modify: `agent/session_self_compact.go` (nudge emit helper)
- Test: `agent/session_self_compact_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestNudge_FiresOnceUntilCompaction(t *testing.T) {
	s := newTestSession(t)
	// drive pressure above WarnThreshold (helper sets lastInputTokens near the window)
	setPressureAbove(t, s, s.contextMgr.WarnThreshold)

	if !s.maybeNudgeSelfCompact(0) {
		t.Fatal("nudge should fire when pressure crosses WarnThreshold and latch is clear")
	}
	if s.maybeNudgeSelfCompact(0) {
		t.Fatal("nudge must not re-fire until after a compaction")
	}
	// a compaction resets the latch
	_ = s.requestForceCompact("")
	s.applyPendingForceCompact(context.Background())
	setPressureAbove(t, s, s.contextMgr.WarnThreshold)
	if !s.maybeNudgeSelfCompact(0) {
		t.Fatal("nudge should fire again after a compaction reset the latch")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./agent/ -run TestNudge -v`
Expected: FAIL — `maybeNudgeSelfCompact` undefined.

- [ ] **Step 3: Implement the nudge**

In `agent/session_self_compact.go`:

```go
const selfCompactNudge = "Context is filling up. If you are at or near a clean stopping " +
	"point, call `compact` now with a note_to_self (and optional compaction_instructions) " +
	"to compact at a clean seam before the automatic fallback fires."

// maybeNudgeSelfCompact injects a one-time steering nudge when pressure crosses
// WarnThreshold. Best-effort: a single large tool result can jump past the
// checkpoint threshold in one round before this fires; the checkpoint/summary
// fallback is the guarantee. Returns true if it nudged.
func (s *Session) maybeNudgeSelfCompact(sysPromptChars int) bool {
	if s.contextMgr == nil {
		return false
	}
	s.mu.Lock()
	if s.nudgedSinceCompact {
		s.mu.Unlock()
		return false
	}
	hist := append([]schema.Turn{}, s.history...)
	s.mu.Unlock()

	if s.contextMgr.Pressure(hist, sysPromptChars) < s.contextMgr.WarnThreshold {
		return false
	}
	s.mu.Lock()
	s.nudgedSinceCompact = true
	s.mu.Unlock()
	s.Steer(selfCompactNudge) // enqueue; drained into history at the next turn boundary
	return true
}
```

(Confirm `s.Steer` is the right injection: read `Session.Steer` and `drainSteering`. The nudge should reach the model as steering before its next decision.)

- [ ] **Step 4: Call it once per round and unify the user warning on `Pressure()`**

In `agent/session_model_call.go`, at the existing context-warning site (`:587-591`), call the nudge alongside, and change `maybeWarnContextUsage` to compute pressure from `s.contextMgr.Pressure(...)` instead of the char/4 estimate over `req.Messages` (keep its `ctxWarned` per-input latch and its user-facing `EventWarning`; only the *estimate source* changes — document the basis change in a comment). Add:

```go
		s.maybeNudgeSelfCompact(len(sys))
```

(`sys` is in scope at the round-loop level; pass the system-prompt char length consistent with `prepareModelRequest`.)

- [ ] **Step 5: Run to verify green**

Run: `go test ./agent/ -run 'TestNudge|Warn' -v && go build ./...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add agent/session_self_compact.go agent/session_model_call.go agent/session_self_compact_test.go
git commit -m "feat(agent): best-effort self-compact nudge; unify user warning on Manager.Pressure"
```

---

## Phase F — TUI, obedience eval, docs

### Task 10: TUI thresholds track config (no hardcoded 0.90)

**Files:**
- Modify: `cmd/serf-tui/statusbar.go` (`:13`, `:63`, `:65`)
- Modify: `cmd/serf-tui/hub_status.go` (the `compactAt` computation)
- Test: `cmd/serf-tui/statusbar_test.go` (if present) or add one

- [ ] **Step 1: Write the failing test**

```go
func TestStatusBar_CompactColorTracksThreshold(t *testing.T) {
	// With the summarize threshold at 0.95, a ratio of 0.92 must NOT show the
	// danger/compact color (it shows warning); 0.96 must show danger.
	if colorBandFor(0.92) == bandCompact {
		t.Fatal("0.92 should not be in the compact band when threshold is 0.95")
	}
	if colorBandFor(0.96) != bandCompact {
		t.Fatal("0.96 should be in the compact band")
	}
}
```

(Introduce a small testable `colorBandFor(ratio float64) band` that both the renderer and the test use, parameterised by the configured thresholds — read how the TUI receives config/threshold values; if the status bar has no access to the agent config, thread the two thresholds through `statusBarInfo`.)

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./cmd/serf-tui/ -run TestStatusBar_CompactColor -v`
Expected: FAIL — color band hardcoded at 0.90.

- [ ] **Step 3: Source both thresholds from config/info, replace the literals**

- Remove `const compactThreshold = 0.90`.
- Add `CompactThreshold float64` and `WarnThreshold float64` to `statusBarInfo`, populated by the caller from the agent's live thresholds (read where `statusBarInfo` is constructed; thread the values from the hub status payload).
- Replace `case ratio >= 0.90:` with `case ratio >= info.CompactThreshold:` and `case ratio >= 0.75:` with `case ratio >= info.WarnThreshold:`.
- In `hub_status.go`, replace the `compactThreshold` use in the `compactAt` computation with the threshold carried in the status payload.

- [ ] **Step 4: Run to verify green**

Run: `go test ./cmd/serf-tui/ -v && go build ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-tui/statusbar.go cmd/serf-tui/hub_status.go cmd/serf-tui/statusbar_test.go
git commit -m "fix(serf-tui): source compaction thresholds from config, drop hardcoded 0.90"
```

---

### Task 11: Obedience eval gate for `compaction_instructions`

**Files:**
- Create: `agent/internal/contextmgr/summarize_obedience_eval_test.go`
- Test data: inline in the test (≥20 cases)

> This is a real eval against the configured cheap summarization model — NOT mocked. It is the gate the spec requires: if the model will not honor drop/keep directives, the instruction path does not ship (the tool still ships note-pin-only, since `compaction_instructions` is optional). Mark it with a build tag or `testing.Short()` skip so the default `go test` run does not require network, but it MUST be runnable in CI / on demand.

- [ ] **Step 1: Write the eval**

```go
//go:build eval

package contextmgr

// Run with: go test -tags eval ./agent/internal/contextmgr/ -run TestSummarizeObedience -v
func TestSummarizeObedience(t *testing.T) {
	cm := newRealSummarizerManager(t) // configured cheap model from env, like other live tests
	cases := obedienceCases()         // ≥20 {history, instruction, mustDrop, mustKeep}
	honored := 0
	for _, c := range cases {
		hist := turnsFromText(t, c.history)
		out, err := cm.summarizeWithLLMSteered(context.Background(), hist, 2, c.instruction)
		if err != nil { t.Fatalf("case %q: %v", c.name, err) }
		summary := summaryText(out)
		if c.mustKeep != "" && !strings.Contains(summary, c.mustKeep) {
			t.Errorf("case %q DROPPED a must-keep: %q", c.name, c.mustKeep)
			continue
		}
		if c.mustDrop != "" && strings.Contains(summary, c.mustDrop) {
			t.Logf("case %q failed to drop %q", c.name, c.mustDrop)
			continue
		}
		honored++
	}
	rate := float64(honored) / float64(len(cases))
	if rate < 0.90 {
		t.Fatalf("obedience rate %.2f < 0.90 — instruction path must NOT ship; gate failed", rate)
	}
}
```

Write ≥20 realistic cases (a big droppable block + a must-keep fact, varied: build logs, vendored code, repetitive tool output, with a keep-the-decision/keep-the-signature counterpart). Any `mustKeep` dropped is an immediate failure (never sacrifice a keep).

- [ ] **Step 2: Run the eval**

Run: `go test -tags eval ./agent/internal/contextmgr/ -run TestSummarizeObedience -v`
Expected: report the honor rate. If ≥0.90 with zero must-keep drops → instruction path is GO. If not → record the result; the tool still ships (note-pin-only), and the steered prompt needs iteration.

- [ ] **Step 3: Record the gate result**

Add a short note to the spec's §3 (or a `docs/design/` note) with the measured honor rate and the model used, so the decision is auditable.

- [ ] **Step 4: Commit**

```bash
git add agent/internal/contextmgr/summarize_obedience_eval_test.go docs/
git commit -m "test(contextmgr): obedience eval gating the instruction-steered summary"
```

---

### Task 12: Document the feature in `context.md`

**Files:**
- Modify: `docs/design/context.md`

- [ ] **Step 1: Add a "Self-compaction (agent-invoked)" section**

Document: the `compact` tool and its two args; that the note is pinned in `SessionMeta` and re-stamped at every compaction via `runPreCompactHook` (before the goal objective); that agent-force runs through `Manager.ForceCompact` at the round tail; the `WarnThreshold` nudge; and the revised threshold table (Warn 0.75 / Checkpoint 0.80 / Summarize 0.95). Update the existing "Compaction Layers" / "ForceCompact" / "Configuration" sections to match.

- [ ] **Step 2: Verify the doc against the code (no drift)**

Re-read the sections you touched against the final code. Fix any mismatch.

- [ ] **Step 3: Commit**

```bash
git add docs/design/context.md
git commit -m "docs(context): document the agent-invoked self-compaction tool"
```

---

## Final verification

- [ ] **Full build + test + race**

```bash
go build ./...
go test ./agent/... ./cmd/serf-tui/...
go test -race ./agent/ -run 'Compact|Nudge|PinnedNote'
```
Expected: all PASS, clean build, no race. Test output PRISTINE (no stray error logs; intentional error paths assert their output).

- [ ] **Lint**

```bash
make lint   # or the repo's golangci-lint target (.golangci.yml present)
```

- [ ] **On-demand obedience eval** (network): `go test -tags eval ./agent/internal/contextmgr/ -run TestSummarizeObedience -v` — confirm the instruction-path GO/NO-GO decision is recorded.

---

## Spec coverage check (self-review)

| Spec section | Task |
|---|---|
| §1 compact tool (args, clear-on-empty, prediction return, one-per-round) | 4, 5 |
| §2 pinned note re-stamp, before objective, no churn | 7 |
| §2 resume via `SessionMeta` | 8 |
| §3 steered summarizer, optional instructions, ripple-minimizing wrapper | 2, 3 |
| §3 obedience eval gate | 11 |
| §4 force via `ForceCompact` at round tail; AfterAction ordering | 3, 6 |
| §5 best-effort nudge; unify user warning on `Pressure()` | 9 |
| §6 thresholds (0.75/0.80/0.95) + scaled-defaults test + TUI both literals + strategy consumers | 1, 10 |
| §Recoverability (persistent vs ephemeral) | 5 (tool description), 8 |
| §Subagents (restrictable) | inherent (core tool) — covered by a subagent test in Task 5 if the helper supports it |
| docs | 12 |

> **Strategy-consumer note (spec §6):** `session-log`/`ooda`/`checkpoint-pred` read `cm.SummarizeThreshold`; raising the default moves their gate to 0.95 too. Intended — no code change needed beyond the default. Their default-asserting tests set the threshold explicitly, so none break (verify with `go test ./agent/internal/contextmgr/ -run Strategy`).
