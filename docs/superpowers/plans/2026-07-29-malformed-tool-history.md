# Malformed Tool History Recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Keep malformed provider tool-call JSON replayable without executing it, fail closed when an assistant turn cannot be persisted, and repair only session `033wtttaNuBna9dXsZMO34`.

**Architecture:** The session loop continues to retain the provider's original tool calls for validation and dispatch, while `appendAssistantTurn` creates a copy-on-write semantic-history message that replaces only non-empty syntactically invalid tool arguments with `{}`. Assistant persistence becomes durable and precedes the live-history append; `emitAssistantResponse` propagates a persistence failure, and its existing position before tool dispatch makes the round stop safely. The one historical orphan result is converted in place to a semantic steering note after a byte-for-byte backup and an open-writer check.

**Tech Stack:** Go, `encoding/json`, Serf's `llm`/`schema`/`transcript` packages, deterministic scripted provider and filesystem boundaries, `serf-doctor`, JSONL transcript v2.

## Global Constraints

- Make the smallest coherent change on `wip/malformed-tool-history-runtime`, based directly on `webui-workspace-shell`.
- Keep raw malformed provider bytes in the canonical API log; do not add them to semantic transcript history.
- Preserve the original `llm.Response` and the `resp.ToolCalls()` values used for pre-validation and dispatch.
- Sanitize only non-empty `ToolCallData.Arguments` for which `json.Valid` is false; leave empty and syntactically valid values unchanged.
- Preserve call ID, item ID, name, type, parsed arguments, thought signature, text, thinking, usage, and response-attempt metadata.
- Do not change provider serializers, transcript schemas, `repairOrphanedToolResults`, `serf-doctor`, the doctoring skill, or any other historical session.
- Use deterministic provider/filesystem boundaries; no provider credentials, network model behavior, sleeps, or ambient machine state in default tests.
- Repair transcript `seq=3070` only after confirming no process has the file open and making a byte-for-byte backup beside it.
- The existing `webui-workspace-shell` baseline is not globally green: identifier-audit, AppWire protocol/client-mutation, and dependent TUI tests already fail. Scoped agent tests must pass, and the final full-suite run must add no failures outside that recorded baseline.

---

### Task 1: Store a safe semantic copy of malformed assistant tool calls

**Files:**
- Modify: `agent/session_openai_malformed_tool_call_test.go:18-286`
- Modify: `agent/session.go:1125-1154`

**Interfaces:**
- Consumes: `llm.Message`, `llm.ContentPart`, `llm.ToolCallData`, and the original `resp.ToolCalls()` extraction in `agent/session_lifecycle.go`.
- Produces: `assistantHistoryMessage(message llm.Message) llm.Message`, a copy-on-write semantic-history projection that never mutates `message`.

- [ ] **Step 1: Extend the malformed-call regression with corrected-call, durable, and restore assertions**

Replace the current second reply with a corrected invocation of the rejected tool, move the existing `communicate` response to case 3, and add case 4 for the restored session:

```go
case 2:
	args := mustJSON(t, map[string]any{"value": "fixed"})
	writeResponsesFunctionCall(t, w, flusher, "resp_fixed", "call_fixed", "my_strict_tool", args)
case 3:
	args := mustJSON(t, map[string]any{
		"message":  "recovered",
		"end_turn": true,
		"output": map[string]any{
			"message":   "",
			"data":      map[string]any{},
			"artifacts": []string{},
		},
	})
	writeResponsesFunctionCall(t, w, flusher, "resp_done", "call_done", "communicate", args)
case 4:
	args := mustJSON(t, map[string]any{
		"message":  "restored",
		"end_turn": true,
		"output": map[string]any{
			"message":   "",
			"data":      map[string]any{},
			"artifacts": []string{},
		},
	})
	writeResponsesFunctionCall(t, w, flusher, "resp_restored", "call_restored", "communicate", args)
```

Replace the integer-only tool counter with captured inputs:

```go
var toolInputs []any
sess.RegisterTool("my_strict_tool", "requires valid JSON arguments", map[string]any{
	"type": "object",
	"properties": map[string]any{
		"value": map[string]any{"type": "string"},
	},
	"required": []string{"value"},
}, func(_ context.Context, input any) (any, error) {
	toolInputs = append(toolInputs, input)
	return "corrected call ran", nil
})
```

After the first input completes, prove exactly the corrected call ran:

```go
if len(toolInputs) != 1 {
	t.Fatalf("strict tool executions = %d, want only the corrected call", len(toolInputs))
}
input, ok := toolInputs[0].(map[string]any)
if !ok || input["value"] != "fixed" {
	t.Fatalf("strict tool input = %#v, want corrected value", toolInputs[0])
}
```

Before closing the first session, retain its metadata and transcript path:

```go
meta := sess.Meta()
transcriptPath := sess.TranscriptPath()
sess.Close()
<-eventsDone
```

Replace the live-history malformed-byte expectation with the semantic-history contract and assert the pre-validation marker:

```go
storedCall, ok := findToolCallInHistory(sess.history, "call_bad")
if !ok {
	t.Fatalf("missing assistant tool call in session history: %s", turnKinds(sess.history))
}
if got := string(storedCall.Arguments); got != "{}" {
	t.Fatalf("stored tool-call arguments = %q, want {}", got)
}

result, ok := findToolResultInHistory(sess.history, "call_bad")
if !ok {
	t.Fatalf("missing error tool result for call_bad: %s", turnKinds(sess.history))
}
if !result.IsError || !result.PrevalOnly {
	t.Fatalf("tool result = %+v, want pre-validation error", result)
}
if !strings.Contains(fmt.Sprint(result.Content), "arguments were not valid JSON") {
	t.Fatalf("tool result content = %q, want invalid-JSON diagnostic", fmt.Sprint(result.Content))
}
```

Read the real transcript through Serf's canonical decoder, prove the call precedes its result, and restore from that same file:

```go
_, entries, skipped, err := readTranscript(transcriptPath)
if err != nil {
	t.Fatalf("readTranscript: %v", err)
}
if skipped != 0 {
	t.Fatalf("readTranscript skipped %d records, want 0", skipped)
}
durableHistory := ResumeHistory(entries)
durableCall, ok := findToolCallInHistory(durableHistory, "call_bad")
if !ok || string(durableCall.Arguments) != "{}" {
	t.Fatalf("durable call_bad = %+v, want arguments {}", durableCall)
}
durableResult, ok := findToolResultInHistory(durableHistory, "call_bad")
if !ok || !durableResult.IsError || !durableResult.PrevalOnly {
	t.Fatalf("durable call_bad result = %+v, want pre-validation error", durableResult)
}
callIndex := turnIndexWithToolCall(durableHistory, "call_bad")
resultIndex := turnIndexWithToolResult(durableHistory, "call_bad")
if callIndex < 0 || resultIndex <= callIndex {
	t.Fatalf("durable call/result order = call:%d result:%d, want call before result", callIndex, resultIndex)
}

restored, err := RestoreSessionFromMetaWithConfig(
	client,
	NewOpenAIProfile("gpt-5.2"),
	execenv.NewLocalExecutionEnvironment(dir),
	meta,
	RestoreSessionConfig{
		StateDir: dir,
		testOnly: testConfig{
			skipGitSnapshot:    true,
			minimalSystemPrompt: true,
			noSyncJobStore:     true,
		},
	},
)
if err != nil {
	t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
}
defer restored.Close()
restoreCtx, cancelRestore := context.WithTimeout(context.Background(), 5*time.Second)
defer cancelRestore()
restoredOutput, err := restored.ProcessInput(restoreCtx, "continue after restore", nil)
if err != nil {
	t.Fatalf("restored ProcessInput: %v", err)
}
if !strings.Contains(restoredOutput, "restored") {
	t.Fatalf("restored ProcessInput output = %q, want restored", restoredOutput)
}
```

Update the request-count assertion after the restored input:

```go
if len(bodies) != 4 {
	t.Fatalf("OpenAI Responses request count = %d, want 4", len(bodies))
}
```

Keep the existing second-request assertions against `bodies[1]`; that is the malformed-call recovery request which receives the corrected call. The fourth request is the successful post-restore continuation.

Add the literal helper used by the ordering assertion:

```go
func turnIndexWithToolCall(history []schema.Turn, callID string) int {
	for i, turn := range history {
		for _, part := range turn.Message.Content {
			if part.Kind == llm.ContentToolCall && part.ToolCall != nil && part.ToolCall.ID == callID {
				return i
			}
		}
	}
	return -1
}
```

- [ ] **Step 2: Run the focused test and verify a behavioral RED**

Run:

```bash
go test ./agent -run '^TestSession_OpenAIResponsesMalformedToolCallRecoveryUsesSafeReplay$' -count=1
```

Expected: FAIL because live history still contains `{"value": broken` instead of `{}`. A compile failure does not count as RED.

- [ ] **Step 3: Add the copy-on-write semantic-history projection**

Add `encoding/json` to `agent/session.go`, then add this helper immediately before `appendAssistantTurn`:

```go
func assistantHistoryMessage(message llm.Message) llm.Message {
	var content []llm.ContentPart
	for i, part := range message.Content {
		if part.ToolCall == nil || len(part.ToolCall.Arguments) == 0 || json.Valid(part.ToolCall.Arguments) {
			continue
		}
		if content == nil {
			content = append([]llm.ContentPart(nil), message.Content...)
		}
		call := *part.ToolCall
		call.Arguments = json.RawMessage(`{}`)
		content[i].ToolCall = &call
	}
	if content != nil {
		message.Content = content
	}
	return message
}
```

Change only the assistant turn's stored message:

```go
Message: assistantHistoryMessage(resp.Message),
```

Do not change `resp`, do not sanitize the `calls := resp.ToolCalls()` slice, and do not move that extraction in `session_lifecycle.go`.

- [ ] **Step 4: Run the focused test and verify GREEN**

Run:

```bash
go test ./agent -run '^TestSession_OpenAIResponsesMalformedToolCallRecoveryUsesSafeReplay$' -count=1
```

Expected: PASS. The malformed invocation never reaches the tool handler, exactly one corrected invocation does, the linked rejected result has `PrevalOnly=true`, the durable call arguments are `{}`, and the restored session accepts the fourth scripted input.

- [ ] **Step 5: Mutation-check the sanitizer**

Temporarily return `message` unchanged from `assistantHistoryMessage`, rerun the focused test, and confirm it fails on the `{}` live/durable-history contract. Restore the helper with `apply_patch`, rerun the test, and confirm PASS.

- [ ] **Step 6: Commit the safe-history slice**

```bash
git status --short
git add agent/session.go agent/session_openai_malformed_tool_call_test.go
git commit -m "fix: keep malformed tool calls replayable

Project malformed provider arguments into valid semantic history without
mutating the response used for pre-validation. Prove the rejected call remains
linked to its PrevalOnly result in live history, on disk, and after restore."
```

---

### Task 2: Fail closed when an assistant turn cannot be persisted

**Files:**
- Create: `agent/session_assistant_persistence_test.go`
- Modify: `agent/session.go:1125-1158`
- Modify: `agent/session_model_call.go:636-671`
- Modify: `agent/session_go_tail_coverage_fuzz_test.go:57-108,224-240`

**Interfaces:**
- Consumes: `Session.writeTranscriptDurable(schema.Turn) error`, the transcript writer's rollback-capable `AppendDurable`, and the existing pre-dispatch call to `emitAssistantResponse`.
- Produces: `(*Session).appendAssistantTurn(llm.Response, ModelAttemptMetadata) error`; `emitAssistantResponse` returns that error before tool canonicalization or execution.

- [ ] **Step 1: Add a deterministic write-failure filesystem at the real transcript boundary**

Create `agent/session_assistant_persistence_test.go` with the filesystem fake and sentinel:

```go
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/spf13/afero"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

var errInjectedTranscriptWrite = errors.New("injected transcript write failure")

type transcriptWriteFailFS struct {
	afero.Fs
	fail bool
}

func (fs *transcriptWriteFailFS) Create(name string) (afero.File, error) {
	file, err := fs.Fs.Create(name)
	if err != nil {
		return nil, err
	}
	return &transcriptWriteFailFile{File: file, fs: fs}, nil
}

type transcriptWriteFailFile struct {
	afero.File
	fs *transcriptWriteFailFS
}

func (file *transcriptWriteFailFile) Write(p []byte) (int, error) {
	if file.fs.fail {
		return 0, errInjectedTranscriptWrite
	}
	return file.File.Write(p)
}
```

Move the identical `tailCoverageFailFS`/`tailCoverageFailFile` behavior out of `session_go_tail_coverage_fuzz_test.go`: delete those two old type definitions and their `Write` method, then change its construction site to `transcriptWriteFailFS`. This keeps one package-level filesystem fake.

- [ ] **Step 2: Add the no-dispatch regression**

Add this test to the new file:

```go
func TestSession_AssistantTranscriptFailureStopsBeforeToolDispatch(t *testing.T) {
	fs := &transcriptWriteFailFS{Fs: afero.NewMemMapFs()}
	const transcriptPath = "/session.jsonl"
	writer, err := transcript.NewWriterWithFS(fs, transcriptPath, transcript.Header{SessionID: "persist-failure"})
	if err != nil {
		t.Fatalf("NewWriterWithFS: %v", err)
	}
	t.Cleanup(func() { _ = writer.Close() })

	adapter := &fakeAdapter{name: "openai"}
	adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			fs.fail = true
			return agenttest.ToolCallResponse(llm.ToolCallData{
				ID:        "must_not_run",
				Name:      "my_tool",
				Arguments: json.RawMessage(`{}`),
				Type:      "function",
			})
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	sess, err := NewSession(
		client,
		NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(t.TempDir()),
		SessionConfig{testOnly: testConfig{
			skipGitSnapshot:    true,
			minimalSystemPrompt: true,
			noSyncJobStore:     true,
		}},
	)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	sess.transcript = writer

	var toolRuns int
	sess.RegisterTool("my_tool", "must not run after persistence failure", map[string]any{
		"type":                 "object",
		"additionalProperties": false,
	}, func(context.Context, any) (any, error) {
		toolRuns++
		return "ran", nil
	})

	output, err := sess.ProcessInput(context.Background(), "trigger persistence failure", nil)
	if !errors.Is(err, errInjectedTranscriptWrite) {
		t.Fatalf("ProcessInput error = %v, want injected transcript write failure", err)
	}
	if output != "" {
		t.Fatalf("ProcessInput output = %q, want empty", output)
	}
	if toolRuns != 0 {
		t.Fatalf("tool executed %d time(s), want 0", toolRuns)
	}
	if _, ok := findToolCallInHistory(sess.history, "must_not_run"); ok {
		t.Fatal("failed assistant turn entered live history")
	}
	if _, ok := findToolResultInHistory(sess.history, "must_not_run"); ok {
		t.Fatal("tool result entered live history after assistant persistence failure")
	}

	data, readErr := afero.ReadFile(fs, transcriptPath)
	if readErr != nil {
		t.Fatalf("read transcript: %v", readErr)
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
	if len(lines) != 2 {
		t.Fatalf("transcript lines = %d, want header plus user input", len(lines))
	}
	entry, decodeErr := transcript.DecodeEntry(lines[1])
	if decodeErr != nil {
		t.Fatalf("decode persisted user input: %v", decodeErr)
	}
	if entry.Turn.Kind != schema.TurnUserInput {
		t.Fatalf("persisted turn kind = %s, want %s", entry.Turn.Kind, schema.TurnUserInput)
	}
}
```

Include `encoding/json` in the import block; it supplies the literal valid arguments without borrowing production logic.

- [ ] **Step 3: Run the new test and verify a behavioral RED**

Run:

```bash
go test ./agent -run '^TestSession_AssistantTranscriptFailureStopsBeforeToolDispatch$' -count=1
```

Expected: FAIL because the current warning-only assistant write continues into tool dispatch and does not return `errInjectedTranscriptWrite`. A compile failure does not count as RED.

- [ ] **Step 4: Persist the assistant turn durably before adding it to live history**

Change `appendAssistantTurn` to return `error` and replace its history-first/warning-only tail with:

```go
func (s *Session) appendAssistantTurn(resp llm.Response, finalAttempt ModelAttemptMetadata) error {
	t := schema.Turn{
		Kind:                            schema.TurnAssistant,
		Message:                         assistantHistoryMessage(resp.Message),
		Timestamp:                       s.sclock().Now().UTC(),
		Usage:                           resp.Usage,
		ResponseID:                      resp.ID,
		ResponseIDHash:                  finalAttempt.ResponseIDHash,
		ResponseProvider:                resp.Provider,
		ResponseModel:                   resp.Model,
		ResponseRequestModel:            finalAttempt.RequestModel,
		AttemptGroupID:                  finalAttempt.AttemptGroupID,
		ResponseEndpointFamily:          finalAttempt.EndpointFamily,
		ResponseEndpoint:                finalAttempt.EndpointURL,
		ResponseStorageScopeFingerprint: finalAttempt.StorageScopeFingerprint,
		ResponseRequestFingerprint:      finalAttempt.RequestFingerprint,
		ResponseContextMarker:           finalAttempt.ContextMarker,
	}
	if err := s.writeTranscriptDurable(t); err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("transcript write failed: %v", err)})
		return err
	}
	s.mu.Lock()
	s.history = append(s.history, t)
	s.mu.Unlock()
	return nil
}
```

`writeTranscriptDurable` is required here: it uses `AppendDurable`, which rolls a failed assistant append back before returning and makes the call durable before any tool can execute.

- [ ] **Step 5: Propagate the persistence failure out of `emitAssistantResponse`**

Keep the shared `withResponseSideEffects(ctx, func()) error` interface unchanged. Capture the assistant error locally, stop the remaining response side effects, and join it with any lifecycle abort:

```go
func (s *Session) emitAssistantResponse(ctx context.Context, resp llm.Response, modelResp sessionModelResponse, txt string, skipHistory bool, finalAttempt ModelAttemptMetadata) error {
	var persistErr error
	responseErr := s.withResponseSideEffects(ctx, func() {
		if !modelResp.StreamedAssistant {
			s.emit(events.EventAssistantTextStart, events.AssistantTextStartData{
				Model: resp.Model,
			})
		}
		if !skipHistory {
			persistErr = s.appendAssistantTurn(resp, finalAttempt)
			if persistErr != nil {
				return
			}
		}
		if !modelResp.StreamedAssistant && strings.TrimSpace(txt) != "" {
			s.emit(events.EventAssistantTextDelta, events.AssistantTextDeltaData{Delta: txt})
		}
		textEndData := events.AssistantTextEndData{
			Text:         txt,
			Usage:        resp.Usage,
			FinishReason: resp.Finish.Reason,
			Model:        resp.Model,
		}
		if reasoning := resp.ReasoningText(); reasoning != "" {
			textEndData.Reasoning = reasoning
		}
		s.emit(events.EventAssistantTextEnd, textEndData)
		s.mu.Lock()
		s.modelResponses++
		s.mu.Unlock()
	})
	return errors.Join(responseErr, persistErr)
}
```

The session loop already returns immediately when `emitAssistantResponse` returns an error, before name canonicalization, `execToolBatch`, or result persistence.

- [ ] **Step 6: Update the existing direct-call coverage assertion**

In `session_go_tail_coverage_fuzz_test.go`, replace the void call with:

```go
if err := s.appendAssistantTurn(llm.Response{Message: llm.Assistant("assistant")}, ModelAttemptMetadata{}); !errors.Is(err, errInjectedTranscriptWrite) {
	t.Fatalf("assistant append error = %v, want injected transcript write failure", err)
}
```

Keep the existing `len(s.events) == 3` warning assertion.

- [ ] **Step 7: Run focused tests and verify GREEN**

Run:

```bash
go test ./agent -run '^(TestSession_OpenAIResponsesMalformedToolCallRecoveryUsesSafeReplay|TestSession_AssistantTranscriptFailureStopsBeforeToolDispatch)$' -count=1
go test ./agent -run '^FuzzSessionGoTailCoverage/seed#0$' -count=1
```

Expected: PASS. The persistence test returns the injected write error, executes zero tools, and leaves neither an assistant call nor result in live/durable history.

- [ ] **Step 8: Mutation-check fail-closed ordering**

Temporarily restore warning-only behavior by ignoring the error from `writeTranscriptDurable` and appending the turn to history. Run:

```bash
go test ./agent -run '^TestSession_AssistantTranscriptFailureStopsBeforeToolDispatch$' -count=1
```

Expected: FAIL because the requested tool executes or the injected persistence error is not returned. Restore the fail-closed implementation with `apply_patch`, rerun, and confirm PASS.

- [ ] **Step 9: Commit the fail-closed slice**

```bash
git status --short
git add agent/session.go agent/session_model_call.go agent/session_assistant_persistence_test.go agent/session_go_tail_coverage_fuzz_test.go
git commit -m "fix: stop tool dispatch on assistant persistence failure

Write assistant turns durably before exposing them to live history and propagate
write failures through the existing pre-dispatch response boundary. Exercise
the real transcript writer against a deterministic filesystem fault and prove
no tool or result can follow a failed assistant append."
```

---

### Task 3: Repair only session `033wtttaNuBna9dXsZMO34`

**Files:**
- Modify operationally: `/Users/jesse/.local/state/serf/projects/Users-jesse-prime-radiant-toil-suite-serf-uo4YId7isa/sessions/033wtttaNuBna9dXsZMO34.transcript.jsonl`
- Create operational backup: `/Users/jesse/.local/state/serf/projects/Users-jesse-prime-radiant-toil-suite-serf-uo4YId7isa/sessions/033wtttaNuBna9dXsZMO34.transcript.jsonl.backup-before-malformed-tool-history-repair-20260729`

**Interfaces:**
- Consumes: transcript v2 `transcript.Entry`/`schema.Turn` format and `serf-doctor` canonical readers.
- Produces: the same transcript sequence and timestamps, with orphan result `seq=3070` replaced by a model-visible semantic steering note.

- [ ] **Step 1: Build the branch's canonical doctor and capture the pre-repair evidence**

Run:

```bash
go build -o /tmp/serf-doctor-malformed-tool-history ./cmd/serf-doctor
/tmp/serf-doctor-malformed-tool-history locate 'proj:Users-jesse-prime-radiant-toil-suite-serf-uo4YId7isa:033wtttaNuBna9dXsZMO34'
/tmp/serf-doctor-malformed-tool-history transcript 'proj:Users-jesse-prime-radiant-toil-suite-serf-uo4YId7isa:033wtttaNuBna9dXsZMO34' --format outline --range 3068-3073
/tmp/serf-doctor-malformed-tool-history apilog 'proj:Users-jesse-prime-radiant-toil-suite-serf-uo4YId7isa:033wtttaNuBna9dXsZMO34' --errors
```

Expected: doctor turn 3071 / transcript `seq=3070` is the `shell` pre-validation result for `tool_v0T1OucHwVb5xYFsyIIfsNm3`, and the API log owns the provider failure evidence.

- [ ] **Step 2: Prove the transcript has no open writer**

Run:

```bash
lsof -- /Users/jesse/.local/state/serf/projects/Users-jesse-prime-radiant-toil-suite-serf-uo4YId7isa/sessions/033wtttaNuBna9dXsZMO34.transcript.jsonl
```

Expected: no output. If any process has the file open, stop this task without editing it.

- [ ] **Step 3: Make and verify the byte-for-byte backup**

Run:

```bash
test ! -e /Users/jesse/.local/state/serf/projects/Users-jesse-prime-radiant-toil-suite-serf-uo4YId7isa/sessions/033wtttaNuBna9dXsZMO34.transcript.jsonl.backup-before-malformed-tool-history-repair-20260729
cp -p /Users/jesse/.local/state/serf/projects/Users-jesse-prime-radiant-toil-suite-serf-uo4YId7isa/sessions/033wtttaNuBna9dXsZMO34.transcript.jsonl /Users/jesse/.local/state/serf/projects/Users-jesse-prime-radiant-toil-suite-serf-uo4YId7isa/sessions/033wtttaNuBna9dXsZMO34.transcript.jsonl.backup-before-malformed-tool-history-repair-20260729
cmp /Users/jesse/.local/state/serf/projects/Users-jesse-prime-radiant-toil-suite-serf-uo4YId7isa/sessions/033wtttaNuBna9dXsZMO34.transcript.jsonl /Users/jesse/.local/state/serf/projects/Users-jesse-prime-radiant-toil-suite-serf-uo4YId7isa/sessions/033wtttaNuBna9dXsZMO34.transcript.jsonl.backup-before-malformed-tool-history-repair-20260729
```

Expected: `cmp` exits 0.

- [ ] **Step 4: Replace exactly transcript `seq=3070` with the approved semantic turn**

Use `apply_patch` to replace only this complete JSONL record:

```json
{"kind":"entry","seq":3070,"turn":{"kind":"TOOL_RESULTS","message":{"role":"tool","content":[{"kind":"tool_result","tool_result":{"tool_call_id":"tool_v0T1OucHwVb5xYFsyIIfsNm3","name":"shell","content":"shell: arguments were not valid JSON (unexpected end of JSON input). Send a single JSON object, e.g. {\"command\": \"...\"}","is_error":true,"preval_only":true}}]},"timestamp":"2026-07-30T01:19:24.216941Z","usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}
```

with:

```json
{"kind":"entry","seq":3070,"turn":{"kind":"STEERING","message":{"role":"user","content":[{"kind":"text","text":"A prior shell tool call had malformed JSON arguments and was rejected before execution. Retry it if still needed."}]},"timestamp":"2026-07-30T01:19:24.216941Z","usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}
```

Do not renumber or rewrite any other entry.

- [ ] **Step 5: Validate framing, sequence count, and canonical rendering**

Run:

```bash
wc -l /Users/jesse/.local/state/serf/projects/Users-jesse-prime-radiant/toil-suite-serf-uo4YId7isa/sessions/033wtttaNuBna9dXsZMO34.transcript.jsonl /Users/jesse/.local/state/serf/projects/Users-jesse-prime-radiant/toil-suite-serf-uo4YId7isa/sessions/033wtttaNuBna9dXsZMO34.transcript.jsonl.backup-before-malformed-tool-history-repair-20260729
/tmp/serf-doctor-malformed-tool-history transcript 'proj:Users-jesse-prime-radiant-toil-suite-serf-uo4YId7isa:033wtttaNuBna9dXsZMO34' --format outline --range 3068-3073
```

Expected: both files have the same line count; doctor parses the complete transcript; turn 3071 is steering rather than an orphan result.

- [ ] **Step 6: Resume the repaired session and send one harmless continuation**

Run the patched branch directly against the repaired bucket:

```bash
go run ./cmd/serf --resume 033wtttaNuBna9dXsZMO34 --state-dir /Users/jesse/.local/state/serf/projects/Users-jesse-prime-radiant-toil-suite-serf-uo4YId7isa --dir /Users/jesse/prime-radiant/toil-suite/serf 'Continue from the latest user request.'
```

Expected: the session accepts the message without `tool_call_id is not found`. Re-run:

```bash
/tmp/serf-doctor-malformed-tool-history apilog 'proj:Users-jesse-prime-radiant-toil-suite-serf-uo4YId7isa:033wtttaNuBna9dXsZMO34' --errors
/tmp/serf-doctor-malformed-tool-history transcript 'proj:Users-jesse-prime-radiant-toil-suite-serf-uo4YId7isa:033wtttaNuBna9dXsZMO34' --format outline --range last:8
```

Expected: a new accepted turn appears and no new missing-tool-call error is recorded.

---

### Task 4: Verify the complete runtime patch

**Files:**
- Verify: `agent/session.go`
- Verify: `agent/session_model_call.go`
- Verify: `agent/session_openai_malformed_tool_call_test.go`
- Verify: `agent/session_assistant_persistence_test.go`
- Verify: repaired session transcript and backup

**Interfaces:**
- Consumes: the two committed runtime slices and repaired transcript.
- Produces: evidence that malformed arguments remain rejected/replayable, persistence failures stop dispatch, and no unrelated tracked changes entered the branch.

- [ ] **Step 1: Run the focused regressions repeatedly**

```bash
go test ./agent -run '^(TestSession_OpenAIResponsesMalformedToolCallRecoveryUsesSafeReplay|TestSession_AssistantTranscriptFailureStopsBeforeToolDispatch)$' -count=10
```

Expected: PASS all ten times.

- [ ] **Step 2: Run the complete agent package**

```bash
go test ./agent -count=1
```

Expected: PASS.

- [ ] **Step 3: Run relevant static checks**

```bash
go vet ./agent/...
golangci-lint run ./agent/...
git diff --check webui-workspace-shell...HEAD
```

Expected: all available checks pass. If `golangci-lint` is unavailable, report that exact limitation rather than silently skipping it.

- [ ] **Step 4: Run the repository-wide suite and compare with the recorded baseline**

```bash
go test ./... -count=1
```

Expected: no failures attributable to this patch. The known `webui-workspace-shell` baseline failures may remain: closed-world identifier audit drift, AppWire v1/v2/client-mutation expectation drift, and dependent TUI/hubstart failures.

- [ ] **Step 5: Inspect final scope and history**

```bash
git status --short --branch
git diff --stat webui-workspace-shell...HEAD
git diff --check webui-workspace-shell...HEAD
git log --oneline --decorate webui-workspace-shell..HEAD
```

Expected: only the design, plan, two runtime changes, focused tests, and test-only filesystem helper are tracked. The repaired state transcript and its backup remain outside the repository.

- [ ] **Step 6: Commit any verification-only plan checkbox updates**

If plan checkboxes were updated during execution:

```bash
git status --short
git add docs/superpowers/plans/2026-07-29-malformed-tool-history.md
git commit -m "docs: record malformed history verification

Mark the executed recovery plan complete and retain the exact test, mutation,
doctor, backup, and resume evidence used to validate the runtime fix."
```

If no plan checkbox content changed, do not create an empty commit.
