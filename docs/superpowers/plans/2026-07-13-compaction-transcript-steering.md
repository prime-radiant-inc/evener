# Exactly-Once Compaction Transcript Steering Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Queue the transcript-recovery steering reminder exactly once per logical compaction operation, including compactions that produce both a checkpoint and a summary.

**Architecture:** Keep checkpoint and summary artifact handling in `handleCompactionTurn`. Move reminder ownership into the existing operation-scoped `compactionEmitFunc` closure: observe history-folding compaction events during the operation, then queue the unchanged reminder once from the single flush call. This preserves later reminders because every compaction operation creates a fresh closure.

**Tech Stack:** Go, Serf session/context-manager lifecycle, scripted `llm.ProviderAdapter` tests, standard `testing` package.

## Global Constraints

- Preserve the current transcript-recovery reminder text and persistent-session guard.
- Emit the reminder once per logical compaction operation, not once per checkpoint or summary artifact.
- Emit a new reminder for each later, distinct compaction operation.
- Do not deduplicate generic steering messages or change context-manager callback semantics.
- Keep default tests deterministic and offline, following `docs/testing.md`.

---

## File Structure

- Modify `agent/session_compaction.go`: own operation-scoped reminder detection and delivery in `compactionEmitFunc`; add a focused helper for the unchanged reminder text and persistence guard.
- Modify `agent/session_namer.go`: remove operation-scoped reminder delivery from artifact-scoped `handleCompactionTurn`.
- Modify `agent/session_config_test.go`: add the failing end-to-end session regression and retain focused reminder-content coverage through the helper.

### Task 1: Reproduce the Duplicate Through `Session.Compact`

**Files:**
- Test: `agent/session_config_test.go:677-767`

**Interfaces:**
- Consumes: `func (s *Session) Compact(context.Context) error`, `func collectEvents(*Session) (*[]events.Event, *sync.Mutex, <-chan struct{})`, `type fakeAdapter`.
- Produces: `func TestSession_CompactQueuesOneTranscriptReminder(t *testing.T)` proving the expected operation-level contract.

- [ ] **Step 1: Write the failing regression test**

Add this test after `TestSession_CompactEmitsCompactionTurnEvent`:

```go
func TestSession_CompactQueuesOneTranscriptReminder(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			return finalResponse("forced summary")
		},
	}})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	sess.contextMgr.PreserveRecentTurns = 1
	sess.history = []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("first task")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("I will inspect the project and report enough detail to summarize.")),
		schema.NewTurn(schema.TurnUserInput, llm.User("second task")),
	}

	if err := sess.Compact(context.Background()); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	sess.mu.Lock()
	queue := append([]steeringMessage(nil), sess.steeringQueue...)
	sess.mu.Unlock()

	const reminderNeedle = "If you need the exact transcript of this session before compaction"
	var reminders []string
	for _, message := range queue {
		if strings.Contains(message.Text, reminderNeedle) {
			reminders = append(reminders, message.Text)
		}
	}
	if len(reminders) != 1 {
		t.Fatalf("compaction transcript reminders = %d, want 1; queue=%q", len(reminders), queue)
	}
	wantCall := `read_session_transcript({"transcript_ref": "local:` + sess.ID() + `", "format": "markdown"})`
	if !strings.Contains(reminders[0], wantCall) {
		t.Fatalf("compaction transcript reminder missing %q; got:\n%s", wantCall, reminders[0])
	}
}
```

- [ ] **Step 2: Run the regression test and verify the duplicate**

Run:

```bash
go test ./agent -run '^TestSession_CompactQueuesOneTranscriptReminder$' -count=1 -v
```

Expected: FAIL with `compaction transcript reminders = 2, want 1`.

- [ ] **Step 3: Commit the failing test**

```bash
git add agent/session_config_test.go
git commit -m "test(agent): reproduce duplicate compaction transcript steering"
```

Expected: one commit containing only the failing regression test.

### Task 2: Move Reminder Delivery to the Compaction Operation

**Files:**
- Modify: `agent/session_compaction.go:67-89`
- Modify: `agent/session_namer.go:283-301`
- Test: `agent/session_config_test.go:725-767`

**Interfaces:**
- Consumes: `events.EventContextCompaction`, `events.ContextCompactionData`, `func encodeRef(string, string) string`, `func (s *Session) Steer(string)`.
- Produces: `func (s *Session) steerCompactionTranscriptReminder()`; `compactionEmitFunc` invokes it no more than once from each returned flush closure.

- [ ] **Step 1: Add a focused reminder helper**

Add this method near `compactionEmitFunc` in `agent/session_compaction.go`:

```go
func (s *Session) steerCompactionTranscriptReminder() {
	if s.stateDir == "" || s.id == "" {
		return
	}
	ref := encodeRef("", s.id)
	s.Steer("<SYSTEM-REMINDER>If you need the exact transcript of this session before compaction, use the transcript tool instead of reading raw transcript files directly. Default read: read_session_transcript({\"transcript_ref\": \"" + ref + "\", \"format\": \"markdown\"}). For long sessions, first get a turn map with read_session_transcript({\"transcript_ref\": \"" + ref + "\", \"format\": \"outline\"}), then read a focused range with read_session_transcript({\"transcript_ref\": \"" + ref + "\", \"range\": \"A-B\"}).</SYSTEM-REMINDER>")
}
```

- [ ] **Step 2: Record history folding and steer once from flush**

Update `compactionEmitFunc` in `agent/session_compaction.go`:

```go
func (s *Session) compactionEmitFunc(ctx context.Context, history *[]schema.Turn) (func(events.EventKind, events.EventData), func()) {
	preCompactRan := false
	historyFolded := false
	var pendingSteering []steeringTurnRecord
	emitFn := func(kind events.EventKind, data events.EventData) {
		if kind == events.EventContextCompaction {
			if compaction, ok := data.(events.ContextCompactionData); ok {
				switch compaction.Layer {
				case "checkpoint", "checkpoint_pred", "session_log_checkpoint", "summarize":
					historyFolded = true
				}
			}
			if !preCompactRan {
				preCompactRan = true
				pendingSteering = append(pendingSteering, s.runPreCompactHook(ctx, history)...)
				s.mu.Lock()
				s.nudgedSinceCompact = false // reset nudge latch on ANY compaction
				s.mu.Unlock()
			}
		}
		s.emit(kind, data)
	}
	flush := func() {
		s.flushSteeringTurnRecords(pendingSteering)
		if historyFolded {
			s.steerCompactionTranscriptReminder()
		}
	}
	return emitFn, flush
}
```

This explicit layer set excludes `observation_mask`, `thinking_clear`, and `aggressive_obs_mask`, which do not create checkpoint or summary artifacts.

- [ ] **Step 3: Remove artifact-scoped reminder delivery**

Delete this block from `handleCompactionTurn` in `agent/session_namer.go`:

```go
	// Tell the agent how to inspect the full pre-compaction transcript.
	if s.stateDir != "" && s.id != "" {
		ref := encodeRef("", s.id)
		s.Steer("<SYSTEM-REMINDER>If you need the exact transcript of this session before compaction, use the transcript tool instead of reading raw transcript files directly. Default read: read_session_transcript({\"transcript_ref\": \"" + ref + "\", \"format\": \"markdown\"}). For long sessions, first get a turn map with read_session_transcript({\"transcript_ref\": \"" + ref + "\", \"format\": \"outline\"}), then read a focused range with read_session_transcript({\"transcript_ref\": \"" + ref + "\", \"range\": \"A-B\"}).</SYSTEM-REMINDER>")
	}
```

- [ ] **Step 4: Point the focused content test at the new helper**

In `TestSession_CompactionReminderUsesTranscriptTool`, replace:

```go
	sess.handleCompactionTurn(schema.NewTurn(schema.TurnCheckpoint, llm.User("[CONTEXT CHECKPOINT]\nsummary")))
```

with:

```go
	sess.steerCompactionTranscriptReminder()
```

Keep all existing assertions. They verify the helper preserves the exact tool guidance and does not expose raw transcript paths.

- [ ] **Step 5: Run focused tests and verify they pass**

Run:

```bash
go test ./agent -run '^(TestSession_CompactQueuesOneTranscriptReminder|TestSession_CompactionReminderUsesTranscriptTool|TestSession_CompactEmitsCompactionTurnEvent)$' -count=1 -v
go test ./agent/internal/contextmgr -run '^TestForceCompact_FiresOnCompactionTurn_Summary$' -count=1 -v
```

Expected: all named tests PASS. The context-manager test must continue to prove that both checkpoint and summary callbacks occur.

- [ ] **Step 6: Commit the implementation**

```bash
git add agent/session_compaction.go agent/session_namer.go agent/session_config_test.go
git commit -m "fix(agent): steer once after each compaction"
```

Expected: a second commit containing the operation-scoped implementation and updated focused test.

### Task 3: Verify the Complete Change

**Files:**
- Verify: `agent/session_compaction.go`
- Verify: `agent/session_namer.go`
- Verify: `agent/session_config_test.go`

**Interfaces:**
- Consumes: the implementation and regression test from Tasks 1-2.
- Produces: test and repository-state evidence that the approved design is complete.

- [ ] **Step 1: Format and inspect the exact diff**

Run:

```bash
gofmt -w agent/session_compaction.go agent/session_namer.go agent/session_config_test.go
git diff --check
git diff HEAD~2 -- agent/session_compaction.go agent/session_namer.go agent/session_config_test.go
```

Expected: `git diff --check` prints nothing. The diff contains no generic queue deduplication, callback semantic changes, or reminder copy changes.

If `gofmt` changes tracked files after Task 2's commit, commit only those formatting changes:

```bash
git add agent/session_compaction.go agent/session_namer.go agent/session_config_test.go
git commit -m "style(agent): format compaction steering fix"
```

- [ ] **Step 2: Run the focused package tests**

Run:

```bash
go test ./agent -run 'Compaction|CompactQueuesOneTranscriptReminder' -count=1
go test ./agent/internal/contextmgr -run 'Compaction|ForceCompact' -count=1
```

Expected: both commands exit 0 with `ok` for their packages.

- [ ] **Step 3: Run the full deterministic suite**

Run:

```bash
make test
```

Expected: exit 0 and `PASS` for every reported module.

- [ ] **Step 4: Review repository state**

Run:

```bash
git status --short
git log -3 --oneline
```

Expected: clean status. Recent commits include the design, failing regression test, and implementation commit (plus an optional formatting-only commit only if `gofmt` required it).
