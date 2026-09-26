package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/doctor"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func TestSession_OpenAIResponsesMalformedToolCallRecoveryUsesSafeReplay(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	const malformedArgs = `{"value": broken`

	var mu sync.Mutex
	var requestBodies [][]byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		mu.Lock()
		requestBodies = append(requestBodies, append([]byte(nil), body...))
		requestIndex := len(requestBodies)
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)

		switch requestIndex {
		case 1:
			writeResponsesFunctionCall(t, w, flusher, "resp_bad", "call_bad", "my_strict_tool", malformedArgs)
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
		default:
			t.Errorf("unexpected request %d body: %s", requestIndex, string(body))
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)

	client := registryClientAt(t, dir, map[string]registry.Provider{"openai": openaiInstance(srv.URL)}, []string{"openai"})
	profile := resolveClientProfile(t, client, "openai/gpt-5.4")

	sess, err := NewSession(client, withTestSessionNamer(client, profile), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir: dir,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

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

	eventsDone := make(chan struct{})
	go func() {
		defer close(eventsDone)
		for range sess.Events() {
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: loopback httptest server, scripted responses, no real I/O; only fires on a genuine hang.
	defer cancel()
	got, err := sess.ProcessInput(ctx, "trigger malformed tool call", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if !strings.Contains(got, "recovered") {
		t.Fatalf("ProcessInput output = %q, want recovered", got)
	}
	if len(toolInputs) != 1 {
		t.Fatalf("strict tool executions = %d, want only the corrected call", len(toolInputs))
	}
	toolInput, ok := toolInputs[0].(map[string]any)
	if !ok || toolInput["value"] != "fixed" {
		t.Fatalf("strict tool input = %#v, want corrected value", toolInputs[0])
	}

	meta := sess.Meta()
	transcriptPath := sess.TranscriptPath()
	sess.Close()
	<-eventsDone

	storedCall, ok := findToolCallInHistory(sess.history, "call_bad")
	if !ok {
		t.Fatalf("missing assistant tool call in session history: %s", turnKinds(sess.history))
	}
	if got := string(storedCall.Arguments); got != "{}" {
		t.Fatalf("stored tool-call arguments = %q, want {}", got)
	}
	if got := storedCall.RawArguments; got != malformedArgs {
		t.Fatalf("stored tool-call raw arguments = %q, want the malformed bytes verbatim %q", got, malformedArgs)
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
	// The coaching must quote the region of the model's own arguments that
	// failed parsing; `{"value": broken` fails at a byte offset, so the
	// excerpt marks it with >>>.
	if got := fmt.Sprint(result.Content); !strings.Contains(got, "near byte") || !strings.Contains(got, ">>>") {
		t.Fatalf("tool result content = %q, want failing-input excerpt", got)
	}

	_, entries, skipped, err := readTranscript(transcriptPath, "")
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
	if ok && durableCall.RawArguments != malformedArgs {
		t.Fatalf("durable call_bad raw arguments = %q, want the malformed bytes verbatim %q", durableCall.RawArguments, malformedArgs)
	}
	// The durable record must preserve the model's raw argument bytes
	// verbatim — the post-mortem can see what was actually sent, not just
	// the empty object the replay-safe form carries.
	transcriptLines := readTranscriptLines(t, transcriptPath)
	requireTranscriptRawArguments(t, transcriptLines, malformedArgs)
	durableResult, ok := findToolResultInHistory(durableHistory, "call_bad")
	if !ok || !durableResult.IsError || !durableResult.PrevalOnly {
		t.Fatalf("durable call_bad result = %+v, want pre-validation error", durableResult)
	}
	callIndex := turnIndexWithToolCall(durableHistory, "call_bad")
	resultIndex := turnIndexWithToolResult(durableHistory, "call_bad")
	if callIndex < 0 || resultIndex <= callIndex {
		t.Fatalf("durable call/result order = call:%d result:%d, want call before result", callIndex, resultIndex)
	}
	// The doctor transcript render must show the raw text for such a call.
	requireDoctorShowsRawArguments(t, dir, meta.ID, malformedArgs)

	restored, err := RestoreSessionFromMetaWithConfig(
		client,
		profile,
		execenv.NewLocalExecutionEnvironment(dir),
		meta,
		RestoreSessionConfig{
			StateDir: dir,
			testOnly: testConfig{
				skipGitSnapshot:     true,
				minimalSystemPrompt: true,
				noSyncJobStore:      true,
			},
		},
	)
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	restoredEventsDone := make(chan struct{})
	go func() {
		defer close(restoredEventsDone)
		for range restored.Events() {
		}
	}()
	defer func() {
		restored.Close()
		<-restoredEventsDone
	}()

	restoreCtx, cancelRestore := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: loopback httptest server, scripted responses, no real I/O; only fires on a genuine hang.
	defer cancelRestore()
	restoredOutput, err := restored.ProcessInput(restoreCtx, "continue after restore", nil)
	if err != nil {
		t.Fatalf("restored ProcessInput: %v", err)
	}
	if !strings.Contains(restoredOutput, "restored") {
		t.Fatalf("restored ProcessInput output = %q, want restored", restoredOutput)
	}

	mu.Lock()
	bodies := append([][]byte(nil), requestBodies...)
	mu.Unlock()
	if len(bodies) != 4 {
		t.Fatalf("OpenAI Responses request count = %d, want 4", len(bodies))
	}
	// raw_arguments is a transcript diagnostic; it must never reach a provider.
	for i, body := range bodies {
		if strings.Contains(string(body), "raw_arguments") {
			t.Fatalf("request %d serialized raw_arguments to the provider: %s", i+1, body)
		}
	}

	second := decodeResponsesRequest(t, bodies[1])
	if _, ok := second["previous_response_id"]; ok {
		t.Fatalf("minimal recovery slice must use full-history replay, got previous_response_id in %s", string(bodies[1]))
	}

	input := responsesInputItems(t, second)
	replayedCall := findResponsesItem(t, input, "function_call", "call_id", "call_bad")
	if replayedCall == nil {
		t.Fatalf("second request missing replayed function_call for call_bad: %#v", input)
	}
	if gotArgs, _ := replayedCall["arguments"].(string); gotArgs != "{}" {
		t.Fatalf("replayed malformed function_call arguments = %q, want {}", gotArgs)
	}

	errorOutput := findResponsesItem(t, input, "function_call_output", "call_id", "call_bad")
	if errorOutput == nil {
		t.Fatalf("second request missing function_call_output for call_bad: %#v", input)
	}
	if _, exists := errorOutput["is_error"]; exists {
		t.Fatalf("function_call_output carried rejected top-level is_error field: %#v", errorOutput)
	}
	output, ok := errorOutput["output"].(string)
	if !ok {
		t.Fatalf("function_call_output.output = %#v, want string", errorOutput["output"])
	}
	if !strings.Contains(output, `"is_error":true`) || !strings.Contains(output, "arguments were not valid JSON") {
		t.Fatalf("function_call_output.output = %q, want wrapped error content", output)
	}
	if !strings.Contains(output, "near byte") {
		t.Fatalf("function_call_output.output = %q, want failing-input excerpt", output)
	}
}

// TestSession_OpenAIUnquotedKeyToolCallRecoversAndExecutes drives the real
// OpenAI Responses wire with a tool call whose arguments hold a bare
// identifier object key — the observed session-034TMrIEL8VtHauQnvpW0I failure
// ("invalid character 'i' looking for beginning of object key string"). The
// unquoted-key repair must quote the key, execute the call with the recovered
// arguments, and keep the model's raw bytes in the durable record.
func TestSession_OpenAIUnquotedKeyToolCallRecoversAndExecutes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	const unquotedArgs = `{value: "recovered"}`

	var mu sync.Mutex
	var requestBodies [][]byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		mu.Lock()
		requestBodies = append(requestBodies, append([]byte(nil), body...))
		requestIndex := len(requestBodies)
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)

		switch requestIndex {
		case 1:
			writeResponsesFunctionCall(t, w, flusher, "resp_unq", "call_unq", "my_strict_tool", unquotedArgs)
		case 2:
			args := mustJSON(t, map[string]any{
				"message":  "done after recovery",
				"end_turn": true,
				"output": map[string]any{
					"message":   "",
					"data":      map[string]any{},
					"artifacts": []string{},
				},
			})
			writeResponsesFunctionCall(t, w, flusher, "resp_done", "call_done", "communicate", args)
		default:
			t.Errorf("unexpected request %d body: %s", requestIndex, string(body))
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)

	client := registryClientAt(t, dir, map[string]registry.Provider{"openai": openaiInstance(srv.URL)}, []string{"openai"})
	profile := resolveClientProfile(t, client, "openai/gpt-5.4")

	sess, err := NewSession(client, withTestSessionNamer(client, profile), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir: dir,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	var toolInputs []any
	sess.RegisterTool("my_strict_tool", "requires valid JSON arguments", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"value": map[string]any{"type": "string"},
		},
		"required": []string{"value"},
	}, func(_ context.Context, input any) (any, error) {
		toolInputs = append(toolInputs, input)
		return "recovered call ran", nil
	})

	repairedCh := drainRepairedEvents(sess)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: loopback httptest server, scripted responses, no real I/O; only fires on a genuine hang.
	defer cancel()
	got, err := sess.ProcessInput(ctx, "trigger unquoted-key tool call", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if !strings.Contains(got, "done after recovery") {
		t.Fatalf("ProcessInput output = %q, want the recovered round's final message", got)
	}

	// The recovered call must have executed with the repaired arguments —
	// no wasted round trip, no JSON-parse error shown to the model.
	if len(toolInputs) != 1 {
		t.Fatalf("strict tool executions = %d, want the recovered call to execute", len(toolInputs))
	}
	toolInput, ok := toolInputs[0].(map[string]any)
	if !ok || toolInput["value"] != "recovered" {
		t.Fatalf("strict tool input = %#v, want recovered value", toolInputs[0])
	}
	result, ok := findToolResultInHistory(sess.history, "call_unq")
	if !ok {
		t.Fatalf("missing tool result for call_unq: %s", turnKinds(sess.history))
	}
	if result.IsError || result.PrevalOnly {
		t.Fatalf("tool result = %+v, want a successful execution result", result)
	}
	storedCall, ok := findToolCallInHistory(sess.history, "call_unq")
	if !ok {
		t.Fatalf("missing assistant tool call in session history: %s", turnKinds(sess.history))
	}
	// The record keeps both truths: the replay-safe {} arguments and the raw
	// unquoted bytes the model actually sent.
	if got := string(storedCall.Arguments); got != "{}" {
		t.Fatalf("stored tool-call arguments = %q, want the replay-safe {}", got)
	}
	if got := storedCall.RawArguments; got != unquotedArgs {
		t.Fatalf("stored tool-call raw arguments = %q, want the unquoted bytes verbatim %q", got, unquotedArgs)
	}

	sess.Close()
	repaired := <-repairedCh
	if len(repaired) != 1 || repaired[0].ToolName != "my_strict_tool" {
		t.Fatalf("EventToolCallRepaired = %+v, want one my_strict_tool repair", repaired)
	}
	joined := strings.Join(repaired[0].Changes, ";")
	if !strings.Contains(joined, "quote_object_key") {
		t.Fatalf("repair changes = %q, want the unquoted-key repair recorded", joined)
	}

	meta := sess.Meta()
	transcriptPath := sess.TranscriptPath()
	lines := readTranscriptLines(t, transcriptPath)
	requireTranscriptRawArguments(t, lines, unquotedArgs)

	// The doctor transcript render must show the raw text for such a call.
	requireDoctorShowsRawArguments(t, dir, meta.ID, unquotedArgs)

	mu.Lock()
	bodies := append([][]byte(nil), requestBodies...)
	mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("OpenAI Responses request count = %d, want 2", len(bodies))
	}
	// raw_arguments is a transcript diagnostic; it must never reach a provider.
	for i, body := range bodies {
		if strings.Contains(string(body), "raw_arguments") {
			t.Fatalf("request %d serialized raw_arguments to the provider: %s", i+1, body)
		}
	}
	second := decodeResponsesRequest(t, bodies[1])
	input := responsesInputItems(t, second)
	replayedCall := findResponsesItem(t, input, "function_call", "call_id", "call_unq")
	if replayedCall == nil {
		t.Fatalf("second request missing replayed function_call for call_unq: %#v", input)
	}
	// The recorded assistant message keeps the replay-safe {} form: the
	// repaired bytes executed, and the raw text lives in raw_arguments.
	if gotArgs, _ := replayedCall["arguments"].(string); gotArgs != "{}" {
		t.Fatalf("replayed recovered function_call arguments = %q, want the replay-safe {}", gotArgs)
	}
}

func writeResponsesFunctionCall(t *testing.T, w io.Writer, flusher http.Flusher, responseID, callID, name, args string) {
	t.Helper()

	item := map[string]any{
		"id":        "item_" + callID,
		"type":      "function_call",
		"status":    "completed",
		"call_id":   callID,
		"name":      name,
		"arguments": args,
	}
	writeSSE(t, w, flusher, "response.output_item.done", map[string]any{
		"type": "response.output_item.done",
		"item": item,
	})
	writeSSE(t, w, flusher, "response.completed", map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"id":     responseID,
			"model":  "gpt-5.2",
			"status": "completed",
			"output": []any{item},
			"usage": map[string]any{
				"input_tokens":  1,
				"output_tokens": 1,
				"total_tokens":  2,
			},
		},
	})
}

func writeSSE(t *testing.T, w io.Writer, flusher http.Flusher, event string, payload any) {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal SSE payload: %v", err)
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, body); err != nil {
		t.Fatalf("write SSE payload: %v", err)
	}
	if flusher != nil {
		flusher.Flush()
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}
	return string(body)
}

// requireTranscriptRawArguments asserts the durable transcript preserves the
// model's raw argument bytes verbatim in the raw_arguments field — the
// post-mortem record of what was actually sent.
func requireTranscriptRawArguments(t *testing.T, lines []string, rawArgs string) {
	t.Helper()
	if !strings.Contains(strings.Join(lines, "\n"), `"raw_arguments":`+mustJSON(t, rawArgs)) {
		t.Fatalf("durable transcript does not preserve the raw arguments %q verbatim:\n%s", rawArgs, strings.Join(lines, "\n"))
	}
}

// requireDoctorShowsRawArguments asserts the doctor transcript render shows
// the raw argument text for a call whose arguments were not valid JSON.
func requireDoctorShowsRawArguments(t *testing.T, stateBase, sessionID, rawArgs string) {
	t.Helper()
	doc, err := doctor.Transcript(stateBase, sessionID, doctor.TranscriptOpts{})
	if err != nil {
		t.Fatalf("doctor.Transcript: %v", err)
	}
	if rendered := doctor.RenderTranscript(doc, "markdown"); !strings.Contains(rendered, rawArgs) {
		t.Fatalf("doctor transcript render hides the raw arguments:\n%s", rendered)
	}
}

func decodeResponsesRequest(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("decode Responses request: %v\n%s", err, string(body))
	}
	return req
}

func responsesInputItems(t *testing.T, req map[string]any) []any {
	t.Helper()
	input, ok := req["input"].([]any)
	if !ok {
		t.Fatalf("Responses request input = %#v, want []any", req["input"])
	}
	return input
}

func findResponsesItem(t *testing.T, items []any, itemType, key, value string) map[string]any {
	t.Helper()
	for _, itemAny := range items {
		item, ok := itemAny.(map[string]any)
		if !ok {
			continue
		}
		if item["type"] == itemType && item[key] == value {
			return item
		}
	}
	return nil
}

func findToolCallInHistory(history []schema.Turn, callID string) (*llm.ToolCallData, bool) {
	for i := range history {
		for j := range history[i].Message.Content {
			part := history[i].Message.Content[j]
			if part.Kind == llm.ContentToolCall && part.ToolCall != nil && part.ToolCall.ID == callID {
				return part.ToolCall, true
			}
		}
	}
	return nil, false
}

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
