# Hook Telemetry Tool-Result Repair Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prevent presentational hook-completion turns from causing duplicate synthetic and real tool results in the next model request.

**Architecture:** Keep `HOOK_COMPLETED` turns durable and in their original transcript order, but classify them as transparent while `repairOrphanedToolResults` searches for results belonging to pending assistant tool calls. Prove the behavior at three levels before changing production code: the pure repair function, a real session with a `PreToolUse` hook and scripted provider, and the existing repair fuzzer's seed corpus.

**Tech Stack:** Go, Serf session history (`agent/schema.Turn`), scripted `llm.ProviderAdapter`, plugin hook runner, standard `testing`, Serf fuzz seed replay.

**Tracking:** kata `jk3q` — “Do not orphan-repair tool calls across hook telemetry”

## Global Constraints

- Follow `AGENTS.md` and `docs/testing.md`.
- Use strict RED → GREEN TDD: all three regression cases must fail for the diagnosed duplicate-result reason before `agent/history_repair.go` changes.
- Keep default tests deterministic and offline. Do not call Kimi or any other provider.
- Preserve every `HOOK_COMPLETED` turn and its original ordering in durable history.
- Continue synthesizing interrupted-call errors at true conversational boundaries and at end-of-history.
- Do not change the Kimi adapter, Anthropic request serializer, hook persistence, or transcript projection.
- Do not add provider-specific deduplication; malformed history must be prevented at its source.
- Do not add backward-compatibility aliases or broaden which turn kinds are transparent.
- Commit only after the focused suite is GREEN; never commit a failing test state.

---

## Failure Contract

The captured session produced this durable history:

```text
ASSISTANT(tool_call id=tool_fd7X...)
HOOK_COMPLETED(PreToolUse)
TOOL_RESULTS(id=tool_fd7X..., success)
```

`repairOrphanedToolResults` currently flushes pending calls in its `default` switch arm when it reaches `HOOK_COMPLETED`. It inserts an error result before the hook and leaves the later success result intact. `expandHistory` then emits two `tool_result` messages with the same ID. Kimi's Anthropic endpoint rejects that continuation with HTTP 400 `Invalid request: tokenization failed`.

A controlled replay removed only the synthetic result from the otherwise identical 101,641-byte request. K3 returned HTTP 200 with 21,717 input tokens. The correction therefore belongs in history repair, not provider configuration or token budgeting.

## Rejected Approaches

1. **Deduplicate in the Anthropic/Kimi serializer.** This hides corrupt session history, forces the serializer to guess which result is authoritative, and leaves other strict providers exposed.
2. **Stop persisting hook completions.** Hook telemetry is an intentional reload-visible transcript contract and is already excluded from model-visible history.
3. **Make every presentational turn transparent.** `TURN_FAILURE` is a real recovery boundary, and broad classification would risk pairing results across terminal failures. This fix is intentionally limited to `HOOK_COMPLETED`.

## File Structure

- Modify `agent/history_repair.go`: keep pending tool calls open across `schema.TurnHookCompleted`.
- Modify `agent/session_orphaned_tool_repair_test.go`: add the smallest pure regression for hook telemetry between a call and its real result.
- Modify `agent/session_hook_turn_test.go`: add a real `PreToolUse` lifecycle regression with a scripted provider and successful test tool.
- Modify `agent/fuzz_fc1_history_repair_test.go`: generate hook-completion turns and require exactly one later result per tool-call ID.

---

### Task 1: Establish all RED cases before production changes

**Files:**
- Modify: `agent/session_orphaned_tool_repair_test.go`
- Modify: `agent/session_hook_turn_test.go`
- Modify: `agent/fuzz_fc1_history_repair_test.go`
- Test: `agent/session_orphaned_tool_repair_test.go`
- Test: `agent/session_hook_turn_test.go`
- Test: `agent/fuzz_fc1_history_repair_test.go`

**Interfaces:**
- Consumes: `repairOrphanedToolResults([]schema.Turn) ([]schema.Turn, int)`, `fakeAdapter`, `toolCallResponse`, `finalResponse`, `Session.ProcessInput`.
- Produces: three tests that fail because one real result becomes two results with the same tool-call ID.

- [ ] **Step 1: Add the pure repair regression**

Add this test beside the existing orphan-repair tests in `agent/session_orphaned_tool_repair_test.go`:

```go
func TestRepairOrphanedToolResultsPreservesHookBeforeRealResult(t *testing.T) {
	t.Parallel()
	const callID = "call_with_hook"
	history := []schema.Turn{
		schema.NewTurn(schema.TurnAssistant, llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{{
				Kind: llm.ContentToolCall,
				ToolCall: &llm.ToolCallData{
					ID:        callID,
					Name:      "probe",
					Type:      "function",
					Arguments: json.RawMessage(`{}`),
				},
			}},
		}),
		schema.NewTurn(schema.TurnHookCompleted, llm.System("PreToolUse hook exit 0")),
		schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed(callID, "probe", "ok", false)),
	}

	got, repairs := repairOrphanedToolResults(history)

	if repairs != 0 {
		t.Fatalf("repairs = %d, want 0; history=%s", repairs, turnKinds(got))
	}
	if gotKinds := turnKinds(got); gotKinds != "ASSISTANT,HOOK_COMPLETED,TOOL_RESULTS" {
		t.Fatalf("history = %s, want original order", gotKinds)
	}
	if results := countToolResultsInHistory(got, callID); results != 1 {
		t.Fatalf("tool results for %s = %d, want exactly 1", callID, results)
	}
}
```

This test catches the exact wrong branch: `schema.TurnHookCompleted` reaching the `default` arm and flushing `pending`.

- [ ] **Step 2: Run the pure test and record RED**

Run:

```bash
go test ./agent -run '^TestRepairOrphanedToolResultsPreservesHookBeforeRealResult$' -count=1
```

Expected: FAIL with `repairs = 1, want 0`; the rendered history should include an inserted `TOOL_RESULTS` turn and two results for `call_with_hook`.

- [ ] **Step 3: Generalize the hook test fixture without changing production behavior**

In `agent/session_hook_turn_test.go`, replace the fixed SessionStart fixture constructor with an event-aware helper and retain the original wrapper:

```go
func hookPluginDirForEvent(t *testing.T, event, command string) string {
	t.Helper()
	dir := t.TempDir()
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	metaDir := filepath.Join(dir, ".claude-plugin")
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		t.Fatalf("mkdir .claude-plugin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(metaDir, "plugin.json"),
		[]byte(`{"name": "hook-turn-plugin", "version": "1.0.0"}`), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	hooksDir := filepath.Join(dir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	hooksJSON := `{"hooks":{"` + event + `":[{"matcher":"*","hooks":[{"type":"command","command":"` + command + `"}]}]}}`
	if err := os.WriteFile(filepath.Join(hooksDir, "hooks.json"), []byte(hooksJSON), 0o644); err != nil {
		t.Fatalf("write hooks: %v", err)
	}
	return dir
}

func hookPluginDir(t *testing.T, command string) string {
	t.Helper()
	return hookPluginDirForEvent(t, "SessionStart", command)
}
```

Run the existing hook tests after this test-only refactor:

```bash
go test ./agent -run '^(TestHookEndPersistsHookCompletedTurn|TestNonZeroHookExitIsPersistedVerbatim|TestMidSessionHookPersistsHookCompletedTurn|TestHookCompletedTurnIsNeverSentToModel)$' -count=1
```

Expected: PASS.

- [ ] **Step 4: Add the real PreToolUse lifecycle regression**

Add `encoding/json`, `fmt`, and `primeradiant.com/serf/agent/internal/tool` to `agent/session_hook_turn_test.go`, then add:

```go
func TestPreToolUseHookDoesNotDuplicateResultInNextModelRequest(t *testing.T) {
	t.Parallel()
	const callID = "call_with_pre_tool_hook"
	dir := t.TempDir()
	var requestErr error
	adapter := &fakeAdapter{
		name: "anthropic",
		steps: []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response {
				return toolCallResponse(llm.ToolCallData{
					ID:        callID,
					Name:      "hook_probe",
					Type:      "function",
					Arguments: json.RawMessage(`{}`),
				})
			},
			func(req llm.Request) llm.Response {
				requestErr = validateSingleSuccessfulToolResult(req.Messages, callID)
				return finalResponse("done")
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	sess, err := NewSession(
		client,
		newAnthropicProfile("k3"),
		execenv.NewLocalExecutionEnvironment(dir),
		SessionConfig{
			StateDir:   dir,
			PluginDirs: []string{hookPluginDirForEvent(t, "PreToolUse", "exit 0")},
		},
	)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)
	go func() {
		for range sess.Events() {
		}
	}()
	if err := sess.reg.Register(tool.RegisteredTool{
		Tool: llm.Tool{Definition: llm.ToolDefinition{
			Name:        "hook_probe",
			Description: "return a deterministic successful result",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		}},
		Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
			return "probe succeeded", nil
		},
	}); err != nil {
		t.Fatalf("register hook_probe: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "run the probe", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if out != "done" {
		t.Fatalf("ProcessInput output = %q, want done", out)
	}
	if requestErr != nil {
		t.Fatal(requestErr)
	}

	transcriptPath := sess.TranscriptPath()
	sess.Close()
	data, err := readTranscriptFull(transcriptPath)
	if err != nil {
		t.Fatalf("readTranscriptFull: %v", err)
	}
	assistantIdx, hookIdx, resultIdx := -1, -1, -1
	for i, entry := range data.Entries {
		switch entry.Turn.Kind {
		case schema.TurnAssistant:
			for _, call := range assistantToolCalls(entry.Turn.Message) {
				if call.ID == callID {
					assistantIdx = i
				}
			}
		case schema.TurnHookCompleted:
			if entry.Turn.Hook != nil && entry.Turn.Hook.Event == "PreToolUse" {
				hookIdx = i
			}
		case schema.TurnToolResults:
			if countToolResultsInHistory([]schema.Turn{entry.Turn}, callID) == 1 {
				resultIdx = i
			}
		}
	}
	if !(assistantIdx >= 0 && assistantIdx < hookIdx && hookIdx < resultIdx) {
		t.Fatalf("transcript order assistant=%d hook=%d result=%d", assistantIdx, hookIdx, resultIdx)
	}
}

func validateSingleSuccessfulToolResult(messages []llm.Message, callID string) error {
	results := 0
	for _, message := range messages {
		for _, part := range message.Content {
			if part.Kind != llm.ContentToolResult || part.ToolResult == nil || part.ToolResult.ToolCallID != callID {
				continue
			}
			results++
			if part.ToolResult.IsError {
				return fmt.Errorf("tool result %s is synthetic error: %v", callID, part.ToolResult.Content)
			}
		}
	}
	if results != 1 {
		return fmt.Errorf("tool results for %s = %d, want exactly 1", callID, results)
	}
	return nil
}
```

`sess.Close()` is guarded by `closeOnce`, so the explicit close flushes the transcript before it is read while `t.Cleanup` still protects early-failure paths.

- [ ] **Step 5: Run the lifecycle test and record RED**

Run:

```bash
go test ./agent -run '^TestPreToolUseHookDoesNotDuplicateResultInNextModelRequest$' -count=1
```

Expected: FAIL from `validateSingleSuccessfulToolResult`: the current repair inserts an error result for `call_with_pre_tool_hook` before the real successful result.

- [ ] **Step 6: Extend the repair fuzzer to cover transparent hook turns**

In `agent/fuzz_fc1_history_repair_test.go`:

1. Add a seed encoding assistant call → hook completion → matching result:

```go
f.Add([]byte{0x05, 0x07, 0x0a})
```

2. Update the decoder comment so op `3` may produce steering or hook telemetry.
3. Change the existing `default` decoder arm to:

```go
default:
	if param%2 == 0 {
		history = append(history, schema.Turn{
			Kind:    schema.TurnSteering,
			Message: llm.User("steer " + strconv.Itoa(counter)),
		})
	} else {
		history = append(history, schema.NewTurn(
			schema.TurnHookCompleted,
			llm.System("PreToolUse hook exit 0"),
		))
	}
	counter++
```

4. Strengthen the orphan oracle from “at least one later result” to exactly one. Replace `fc1HasLaterResult` with:

```go
func fc1LaterResultCount(rest []schema.Turn, callID string) int {
	count := 0
	for _, turn := range rest {
		if turn.Kind != schema.TurnTool && turn.Kind != schema.TurnToolResults {
			continue
		}
		for _, part := range turn.Message.Content {
			if part.Kind == llm.ContentToolResult && part.ToolResult != nil && part.ToolResult.ToolCallID == callID {
				count++
			}
		}
	}
	return count
}
```

Update `fc1AssertNoOrphanedCall` to fail unless `fc1LaterResultCount(history[i+1:], p.ToolCall.ID) == 1`, and name the failure as the observed count.

- [ ] **Step 7: Run the fuzz seed corpus and record RED**

Run:

```bash
go test -tags serffuzz ./agent -run '^FuzzFc1RepairOrphanedToolResults$' -count=1
```

Expected: FAIL on the new seed because repair produces two later results for the call separated from its real result by `HOOK_COMPLETED`.

---

### Task 2: Make hook telemetry transparent to orphan repair

**Files:**
- Modify: `agent/history_repair.go`
- Test: `agent/session_orphaned_tool_repair_test.go`
- Test: `agent/session_hook_turn_test.go`
- Test: `agent/fuzz_fc1_history_repair_test.go`

**Interfaces:**
- Consumes: the three RED contracts from Task 1.
- Produces: `repairOrphanedToolResults` retains pending calls across `schema.TurnHookCompleted` while preserving the hook turn in-place.

- [ ] **Step 1: Add the minimal production classification**

In `repairOrphanedToolResults`, add one explicit switch case before `default`:

```go
		case schema.TurnHookCompleted:
			// Hook completion is presentational telemetry that can legally be
			// recorded between a tool call and its result. Preserve it in-place
			// without treating the pending call as interrupted.
			out = append(out, turn)
```

Do not add other turn kinds to this case. Do not change `flushPending`, `removePending`, end-of-history flushing, or synthetic-result contents.

- [ ] **Step 2: Run all three focused regressions and verify GREEN**

Run:

```bash
go test ./agent -run '^(TestRepairOrphanedToolResultsPreservesHookBeforeRealResult|TestPreToolUseHookDoesNotDuplicateResultInNextModelRequest)$' -count=1
go test -tags serffuzz ./agent -run '^FuzzFc1RepairOrphanedToolResults$' -count=1
```

Expected: PASS. The lifecycle request contains one successful result for `call_with_pre_tool_hook`, and the transcript still orders assistant → hook → result.

- [ ] **Step 3: Prove true recovery boundaries still synthesize results**

Run the existing recovery contracts:

```bash
go test ./agent -run '^(TestSession_ProcessInputRepairsOrphanedAssistantToolCallsBeforeModelRequest|TestResumeHistoryRepairsOrphanedAssistantToolCallsBeforeLaterUserInput)$' -count=1
```

Expected: PASS. A later user input and end-of-history still produce synthetic error results for genuinely missing calls.

- [ ] **Step 4: Format and inspect the focused diff**

Run:

```bash
gofmt -w agent/history_repair.go agent/session_orphaned_tool_repair_test.go agent/session_hook_turn_test.go agent/fuzz_fc1_history_repair_test.go
git diff --check
git diff -- agent/history_repair.go agent/session_orphaned_tool_repair_test.go agent/session_hook_turn_test.go agent/fuzz_fc1_history_repair_test.go
```

Expected: only the explicit hook classification and the three regression layers are present.

- [ ] **Step 5: Commit the GREEN implementation**

Run `git status --short`, then stage only the four files:

```bash
git add agent/history_repair.go agent/session_orphaned_tool_repair_test.go agent/session_hook_turn_test.go agent/fuzz_fc1_history_repair_test.go
git commit -m "fix(agent): keep hook telemetry inside tool rounds" \
  -m "PreToolUse hook completion turns can be persisted between an assistant tool call and its real result. Orphan repair treated that presentational marker as a recovery boundary, inserted an interrupted-call error, and sent strict providers duplicate results for one call ID." \
  -m "Keep HOOK_COMPLETED transparent while matching pending calls, retain the hook in its original transcript order, and cover the behavior through pure repair, real hook lifecycle, and fuzz-seed contracts."
```

Expected: commit hooks run normally and the commit succeeds.

---

### Task 3: Mutation-proof and verify the completed change

**Files:**
- Verify: `agent/history_repair.go`
- Verify: `agent/session_orphaned_tool_repair_test.go`
- Verify: `agent/session_hook_turn_test.go`
- Verify: `agent/fuzz_fc1_history_repair_test.go`

**Interfaces:**
- Consumes: the committed GREEN change from Task 2.
- Produces: fresh evidence that the tests catch the original regression and that the repository remains healthy.

- [ ] **Step 1: Mutation-prove both deterministic regressions**

Temporarily remove only the new `case schema.TurnHookCompleted` arm from `agent/history_repair.go`, leaving the tests unchanged.

Run:

```bash
go test ./agent -run '^(TestRepairOrphanedToolResultsPreservesHookBeforeRealResult|TestPreToolUseHookDoesNotDuplicateResultInNextModelRequest)$' -count=1
```

Expected: FAIL in both tests for the duplicate synthetic result.

Restore the exact committed case, then rerun the same command.

Expected: PASS.

Confirm the temporary mutation left no diff:

```bash
git diff --exit-code -- agent/history_repair.go
```

- [ ] **Step 2: Run the complete agent package and scoped lint**

Run:

```bash
go test ./agent -count=1
golangci-lint run ./agent/...
```

Expected: all tests pass and lint reports `0 issues`.

- [ ] **Step 3: Run repository-wide tests and the runtime build**

Run:

```bash
go test ./... -count=1
make build-runtime
```

Expected: both commands exit 0. No live-provider opt-in variable is set.

- [ ] **Step 4: Verify final repository state**

Run:

```bash
git status --short --branch
git log -1 --oneline
git show --stat --oneline HEAD
```

Expected: the worktree is clean and the latest implementation commit changes only the four planned files.

- [ ] **Step 5: Record implementation evidence on kata `jk3q`**

Append a kata comment containing:

- implementation commit SHA;
- the exact RED failures observed before the production change;
- focused GREEN commands;
- mutation failure and restored GREEN result;
- full `go test ./... -count=1`, `make build-runtime`, and lint results;
- any limitation encountered.

Leave the kata open for human review; do not close it.

## Completion Criteria

- A real `PreToolUse` hook may be persisted between an assistant tool call and its result without causing repair.
- The next provider request contains exactly one successful tool result for that call ID.
- The `HOOK_COMPLETED` turn remains durable, presentational, and in its original transcript order.
- Genuine orphaned calls still receive one synthetic error at user/recovery boundaries and end-of-history.
- The fuzz seed corpus asserts exactly one later result per non-empty tool-call ID.
- Both focused regression tests are mutation-proven.
- Agent tests, repository tests, lint, and runtime build all pass offline.
