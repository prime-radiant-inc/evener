package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// scriptedInteractionResponse is one canned HTTP reply the scripted
// /interactions endpoint hands out in order.
type scriptedInteractionResponse struct {
	status      int
	contentType string
	body        string
}

// scriptedInteractions is a real httptest-backed Google endpoint. It answers
// the live model-listing probe with an empty list (so restore never leaves the
// process) and serves POSTed /interactions requests from a queue, recording
// every request body for later assertion.
type scriptedInteractions struct {
	t        *testing.T
	mu       sync.Mutex
	queue    []scriptedInteractionResponse
	requests [][]byte
}

func (s *scriptedInteractions) enqueueJSON(status int, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queue = append(s.queue, scriptedInteractionResponse{
		status:      status,
		contentType: "application/json",
		body:        body,
	})
}

func (s *scriptedInteractions) enqueueSSE(body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queue = append(s.queue, scriptedInteractionResponse{
		status:      http.StatusOK,
		contentType: "text/event-stream",
		body:        body,
	})
}

func (s *scriptedInteractions) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/models") {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"models":[]}`)
		return
	}

	raw, err := io.ReadAll(r.Body)
	if err != nil {
		s.t.Errorf("read interactions request body: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	s.mu.Lock()
	s.requests = append(s.requests, append([]byte(nil), raw...))
	next := scriptedInteractionResponse{
		status:      http.StatusInternalServerError,
		contentType: "application/json",
		body:        `{"error":{"code":500,"message":"scripted endpoint has no queued reply"}}`,
	}
	if len(s.queue) > 0 {
		next = s.queue[0]
		s.queue = s.queue[1:]
	}
	s.mu.Unlock()

	w.Header().Set("Content-Type", next.contentType)
	w.WriteHeader(next.status)
	_, _ = io.WriteString(w, next.body)
}

func (s *scriptedInteractions) requestBodies() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][]byte, len(s.requests))
	copy(out, s.requests)
	return out
}

func (s *scriptedInteractions) pending() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.queue)
}

// interactionsFunctionCallSSE scripts one completed interaction whose single
// step is a function_call, matching the SSE shape the Google transport decodes.
func interactionsFunctionCallSSE(model, interactionID, callID, name string, args map[string]any) string {
	argsJSON, err := json.Marshal(args)
	if err != nil {
		panic("marshal interactions function_call arguments: " + err.Error())
	}
	return "event: interaction.created\n" +
		`data: {"event_type":"interaction.created","interaction":{"id":"` + interactionID + `","model":"` + model + `","status":"in_progress"}}` + "\n\n" +
		"event: step.start\n" +
		`data: {"event_type":"step.start","index":0,"step":{"type":"function_call","id":"` + callID + `","name":"` + name + `","arguments":` + string(argsJSON) + `}}` + "\n\n" +
		"event: step.stop\n" +
		`data: {"event_type":"step.stop","index":0}` + "\n\n" +
		"event: interaction.completed\n" +
		`data: {"event_type":"interaction.completed","interaction":{"id":"` + interactionID + `","model":"` + model + `","status":"completed","steps":[{"type":"function_call","id":"` + callID + `","name":"` + name + `","arguments":` + string(argsJSON) + `}],"usage":{"total_input_tokens":20,"total_output_tokens":5,"total_tokens":25}}}` + "\n\n" +
		"event: done\ndata: [DONE]\n\n"
}

// interactionsModelOutputSSE scripts one completed interaction carrying a
// single text model_output step.
func interactionsModelOutputSSE(model, interactionID string) string {
	return "event: interaction.created\n" +
		`data: {"event_type":"interaction.created","interaction":{"id":"` + interactionID + `","model":"` + model + `","status":"in_progress"}}` + "\n\n" +
		"event: step.start\n" +
		`data: {"event_type":"step.start","index":0,"step":{"type":"model_output"}}` + "\n\n" +
		"event: step.delta\n" +
		`data: {"event_type":"step.delta","index":0,"delta":{"type":"text","text":"hel"}}` + "\n\n" +
		"event: step.delta\n" +
		`data: {"event_type":"step.delta","index":0,"delta":{"type":"text","text":"lo"}}` + "\n\n" +
		"event: step.stop\n" +
		`data: {"event_type":"step.stop","index":0}` + "\n\n" +
		"event: interaction.completed\n" +
		`data: {"event_type":"interaction.completed","interaction":{"id":"` + interactionID + `","model":"` + model + `","status":"completed","usage":{"total_input_tokens":5,"total_output_tokens":2,"total_tokens":7}}}` + "\n\n" +
		"event: done\ndata: [DONE]\n\n"
}

const googleSignature400 = `{"error":{"code":400,"message":"Function call is missing a thought_signature.","status":"INVALID_ARGUMENT"}}`

const googleInteractionsJSON = `{"id":"int_ok","model":"gemini-3.8-flash","status":"completed","steps":[{"type":"model_output","content":[{"type":"text","text":"hello"}]}],"usage":{"total_input_tokens":5,"total_output_tokens":2,"total_tokens":7}}`

// interactionsSteps decodes the outbound input[] steps of a captured request
// body. The Interactions wire field is "input"; "steps" is the response side.
func interactionsSteps(t *testing.T, body []byte) []any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode interactions request: %v\nbody: %s", err, body)
	}
	steps, ok := decoded["input"].([]any)
	if !ok {
		t.Fatalf("interactions request has no input[] steps: %s", body)
	}
	return steps
}

func assertNoAdjacentUserInputs(t *testing.T, steps []any) {
	t.Helper()
	for i := 1; i < len(steps); i++ {
		if stepType(steps[i-1]) == "user_input" && stepType(steps[i]) == "user_input" {
			t.Fatalf("steps %d and %d are adjacent user_input steps: %s", i-1, i, mustJSON(t, steps))
		}
	}
}

func stepType(step any) string {
	m, _ := step.(map[string]any)
	s, _ := m["type"].(string)
	return s
}

func TestSessionGoogleInteractions_DeepMultiTurnNotificationRolloverRegression(t *testing.T) {
	dir := t.TempDir()
	script := &scriptedInteractions{t: t}
	script.enqueueSSE(interactionsFunctionCallSSE("gemini-4-carbon", "int_rollover", "call_comm", "communicate", map[string]any{
		"message":  "rollover settled",
		"end_turn": true,
		"output": map[string]any{
			"message":   "",
			"data":      map[string]any{},
			"artifacts": []string{},
		},
	}))
	srv := httptest.NewServer(script)
	t.Cleanup(srv.Close)

	client := registryClientAt(t, dir, map[string]registry.Provider{
		"google": {
			Base:   "google",
			APIKey: "test",
			Transport: registry.Transport{
				BaseURL: srv.URL + "/v1beta",
			},
		},
	}, []string{"google"})
	profile := resolveClientProfile(t, client, "google/gemini-4-carbon")
	res := profile.Resolved()

	sess, err := NewSession(client, withTestSessionNamer(client, profile), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir: dir,
		testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })

	// The launch-time environment context is not what this regression pins:
	// suppress it so the outbound steps are exactly the seeded history plus the
	// new user prompt. appendEnvironmentContext treats a nil collector as "no
	// context to report".
	sess.envCollector = nil

	now := time.Now().UTC()
	sess.history = append(sess.history,
		schema.Turn{
			Kind:      schema.TurnUserInput,
			Message:   llm.User("original request"),
			Timestamp: now.Add(-5 * time.Minute),
		},
		schema.Turn{
			Kind: schema.TurnAssistant,
			Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
				{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{
					GoogleInteraction: true,
					GoogleInstance:    profile.ID(),
					GoogleModel:       res.WireID,
					Signature:         "sig_rollover",
					Summary:           []string{"why"},
				}},
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
					ID:                "call_probe",
					Name:              "probe_tool",
					Arguments:         json.RawMessage(`{}`),
					GoogleInteraction: true,
					GoogleInstance:    profile.ID(),
					GoogleModel:       res.WireID,
				}},
			}},
			Timestamp: now.Add(-4 * time.Minute),
		},
		schema.Turn{
			Kind:         schema.TurnSteering,
			Message:      llm.User("<job-notification>job finished</job-notification>"),
			Timestamp:    now.Add(-3 * time.Minute),
			SteeringKind: "job_notification",
		},
		schema.Turn{
			Kind:         schema.TurnSteering,
			Message:      llm.User("<delegate-notification>delegate finished</delegate-notification>"),
			Timestamp:    now.Add(-3 * time.Minute),
			SteeringKind: "delegate_notification",
		},
		schema.Turn{
			Kind: schema.TurnToolResults,
			Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
				{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
					ToolCallID: "call_probe",
					Name:       "probe_tool",
					Content:    "probe ok",
				}},
			}},
			Timestamp: now.Add(-2 * time.Minute),
		},
		schema.Turn{
			Kind: schema.TurnAssistant,
			Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
				{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{
					Text: "legacy reasoning",
				}},
				{Kind: llm.ContentText, Text: "intermediate answer"},
			}},
			Timestamp: now.Add(-time.Minute),
		},
	)

	eventsDone := make(chan struct{})
	go func() {
		defer close(eventsDone)
		for range sess.Events() {
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: in-process httptest server with a scripted reply, no real network I/O; only fires on a genuine hang.
	defer cancel()
	got, err := sess.ProcessInput(ctx, "new user prompt", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got != "rollover settled" {
		t.Fatalf("ProcessInput output = %q, want the communicate message", got)
	}
	if state := sess.State(); state != SessionIdle {
		t.Fatalf("State after settlement = %q, want %q", state, SessionIdle)
	}

	sess.Close()
	<-eventsDone

	bodies := script.requestBodies()
	if len(bodies) != 1 {
		t.Fatalf("interactions request count = %d, want 1", len(bodies))
	}
	var body map[string]any
	if err := json.Unmarshal(bodies[0], &body); err != nil {
		t.Fatalf("decode interactions request: %v", err)
	}
	if store, ok := body["store"].(bool); !ok || store {
		t.Fatalf("request store = %#v, want explicit false: %s", body["store"], bodies[0])
	}
	if model, _ := body["model"].(string); model != "gemini-4-carbon" {
		t.Fatalf("request model = %q, want gemini-4-carbon", model)
	}

	steps := interactionsSteps(t, bodies[0])
	want := []any{
		map[string]any{"type": "user_input", "content": []any{
			map[string]any{"type": "text", "text": "original request"},
		}},
		map[string]any{"type": "thought", "signature": "sig_rollover", "summary": []any{
			map[string]any{"type": "text", "text": "why"},
		}},
		map[string]any{"type": "function_call", "id": "call_probe", "name": "probe_tool", "arguments": map[string]any{}},
		map[string]any{"type": "function_result", "call_id": "call_probe", "name": "probe_tool", "result": "probe ok"},
		map[string]any{"type": "user_input", "content": []any{
			map[string]any{"type": "text", "text": "<job-notification>job finished</job-notification>"},
			map[string]any{"type": "text", "text": "<delegate-notification>delegate finished</delegate-notification>"},
		}},
		map[string]any{"type": "model_output", "content": []any{
			map[string]any{"type": "text", "text": "intermediate answer"},
		}},
		map[string]any{"type": "user_input", "content": []any{
			map[string]any{"type": "text", "text": "new user prompt"},
		}},
	}
	if !reflect.DeepEqual(steps, want) {
		t.Fatalf("outbound steps mismatch\n got: %s\nwant: %s", mustJSON(t, steps), mustJSON(t, want))
	}
	assertNoAdjacentUserInputs(t, steps)
	if pending := script.pending(); pending != 0 {
		t.Fatalf("scripted endpoint still has %d unserved replies", pending)
	}
}

func TestSessionGoogleInteractions_PreCutoverSignedToolCallRestoreAndFallback(t *testing.T) {
	dir := t.TempDir()
	const (
		sessionID = "restored-google-legacy-signed"
		model     = "gemini-3.8-flash"
	)

	transcriptPath := schema.SessionTranscriptPath(dir, sessionID)
	writer, err := transcript.NewWriter(transcriptPath, transcript.Header{
		SessionID: sessionID,
		ProfileID: "google",
		Model:     model,
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	now := time.Now().UTC()
	legacyTurns := []schema.Turn{
		{
			Kind:      schema.TurnUserInput,
			Message:   llm.User("legacy request"),
			Timestamp: now.Add(-5 * time.Minute),
		},
		{
			Kind: schema.TurnAssistant,
			Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
				{Kind: llm.ContentWebSearch, WebSearch: &llm.WebSearchData{
					Query: "legacy query",
					Raw:   json.RawMessage(`{"type":"server_tool_use"}`),
				}},
				{Kind: llm.ContentText, Text: "legacy grounded answer"},
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
					ID:        "call_search",
					Name:      "web_search",
					Arguments: json.RawMessage(`{"query":"legacy"}`),
				}},
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
					ID:                "call_read",
					Name:              "read_file",
					Arguments:         json.RawMessage(`{"path":"a.txt"}`),
					ThoughtSignature:  "legacy_sig",
					GoogleInteraction: false,
				}},
			}},
			Timestamp:        now.Add(-4 * time.Minute),
			ResponseProvider: "google",
			ResponseModel:    model,
		},
		{
			Kind: schema.TurnToolResults,
			Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
				{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
					ToolCallID: "call_search",
					Name:       "web_search",
					Content:    "search result",
				}},
				{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
					ToolCallID: "call_read",
					Name:       "read_file",
					Content:    "file body",
				}},
			}},
			Timestamp: now.Add(-3 * time.Minute),
		},
		{
			Kind: schema.TurnAssistant,
			Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
				{Kind: llm.ContentText, Text: "intermediate legacy answer"},
			}},
			Timestamp:        now.Add(-2 * time.Minute),
			ResponseProvider: "google",
			ResponseModel:    model,
		},
	}
	for _, turn := range legacyTurns {
		if _, err := writer.Record(turn, transcript.RecordOptions{Door: transcript.DoorDurable, Place: transcript.PlaceVerbatim}); err != nil {
			t.Fatalf("record legacy turn: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close transcript writer: %v", err)
	}

	meta := schema.SessionMeta{
		ID:        sessionID,
		ProfileID: "google",
		Model:     model,
		Config:    (SessionConfig{StateDir: dir}).toSnapshot(),
		CreatedAt: now.Add(-6 * time.Minute),
		UpdatedAt: now.Add(-time.Minute),
	}
	if err := schema.SaveSessionMeta(dir, meta); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}

	before, err := os.ReadFile(transcriptPath)
	if err != nil {
		t.Fatalf("read transcript before restore: %v", err)
	}

	script := &scriptedInteractions{t: t}
	script.enqueueSSE(interactionsFunctionCallSSE(model, "int_restored", "call_comm", "communicate", map[string]any{
		"message":  "restored turn settled",
		"end_turn": true,
		"output": map[string]any{
			"message":   "",
			"data":      map[string]any{},
			"artifacts": []string{},
		},
	}))
	srv := httptest.NewServer(script)
	t.Cleanup(srv.Close)

	client := registryClientAt(t, dir, map[string]registry.Provider{
		"google": {
			Base:   "google",
			APIKey: "test",
			Transport: registry.Transport{
				BaseURL: srv.URL + "/v1beta",
			},
		},
	}, []string{"google"})
	// The namer's background cheap-model call must not consume the scripted
	// main-provider reply, so it goes to its own scripted provider.
	profile := withTestSessionNamer(client, resolveClientProfile(t, client, "google/"+model))

	restored, err := RestoreSessionFromMetaWithConfig(client, profile, execenv.NewLocalExecutionEnvironment(dir), meta, RestoreSessionConfig{
		StateDir: dir,
		testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	})
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	t.Cleanup(func() { restored.Close() })
	restored.envCollector = nil
	restoredTurns := append([]schema.Turn(nil), restored.history...)

	eventsDone := make(chan struct{})
	go func() {
		defer close(eventsDone)
		for range restored.Events() {
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: in-process httptest server with scripted replies, no real network I/O; only fires on a genuine hang.
	defer cancel()

	// Phase A: a post-upgrade turn replays the restored pre-cutover history.
	got, err := restored.ProcessInput(ctx, "post-upgrade prompt", nil)
	if err != nil {
		t.Fatalf("ProcessInput after restore: %v (requests=%d, pending=%d)", err, len(script.requestBodies()), script.pending())
	}
	if got != "restored turn settled" {
		t.Fatalf("ProcessInput output = %q, want the communicate message", got)
	}
	if state := restored.State(); state != SessionIdle {
		t.Fatalf("State after settlement = %q, want %q", state, SessionIdle)
	}

	restored.Close()
	<-eventsDone

	after, err := os.ReadFile(transcriptPath)
	if err != nil {
		t.Fatalf("read transcript after restore: %v", err)
	}
	if !bytes.HasPrefix(after, before) {
		t.Fatalf("restored session rewrote its transcript prefix:\nbefore: %s\nafter: %s", before, after)
	}

	bodies := script.requestBodies()
	if len(bodies) != 1 {
		t.Fatalf("phase A interactions request count = %d, want 1", len(bodies))
	}
	if bytes.Contains(bodies[0], []byte("legacy_sig")) {
		t.Fatalf("attempt 1 sent the pre-cutover signature: %s", bodies[0])
	}
	if bytes.Contains(bodies[0], []byte("server_tool_use")) || bytes.Contains(bodies[0], []byte("legacy grounded answer")) {
		t.Fatalf("attempt 1 replayed historical Grounding: %s", bodies[0])
	}

	phaseASteps := interactionsSteps(t, bodies[0])
	wantPhaseA := []any{
		map[string]any{"type": "user_input", "content": []any{
			map[string]any{"type": "text", "text": "legacy request"},
		}},
		map[string]any{"type": "model_output", "content": []any{
			map[string]any{"type": "text", "text": "[historical grounded output omitted from model replay]"},
		}},
		map[string]any{"type": "function_call", "id": "call_search", "name": "web_search", "arguments": map[string]any{
			"_omitted": "[historical grounded tool input omitted from model replay]",
		}},
		map[string]any{"type": "function_call", "id": "call_read", "name": "read_file", "arguments": map[string]any{"path": "a.txt"}},
		map[string]any{"type": "function_result", "call_id": "call_search", "name": "web_search", "result": "[historical web_search output omitted from model replay]"},
		map[string]any{"type": "function_result", "call_id": "call_read", "name": "read_file", "result": "file body"},
		map[string]any{"type": "model_output", "content": []any{
			map[string]any{"type": "text", "text": "intermediate legacy answer"},
		}},
		map[string]any{"type": "user_input", "content": []any{
			map[string]any{"type": "text", "text": "post-upgrade prompt"},
		}},
	}
	if !reflect.DeepEqual(phaseASteps, wantPhaseA) {
		t.Fatalf("phase A steps mismatch\n got: %s\nwant: %s", mustJSON(t, phaseASteps), mustJSON(t, wantPhaseA))
	}
	assertNoAdjacentUserInputs(t, phaseASteps)

	// Phase B and C drive the restored history through the protocol directly so
	// the 400 fallback is the only thing under test.
	scope := replayScope{
		Instance:       profile.ID(),
		Model:          profile.Model(),
		WireID:         profile.Resolved().WireID,
		Protocol:       profile.Protocol(),
		InFlightFrom:   len(restoredTurns),
		ResultToolName: restored.resultToolName(),
		protocolOf:     restored.instanceProtocol,
		canonicalModel: func(m string) string {
			return restored.canonicalModelID(profile.ID(), m)
		},
	}
	baseMessages := append(expandHistory(restoredTurns, scope), llm.User("next prompt"))

	script.enqueueJSON(http.StatusBadRequest, googleSignature400)
	script.enqueueJSON(http.StatusOK, googleInteractionsJSON)
	completeReq := llm.Request{Provider: profile.ID(), Model: profile.Model(), Messages: baseMessages}
	resp, err := client.Complete(ctx, completeReq)
	if err != nil {
		t.Fatalf("Complete after signature 400: %v", err)
	}
	if resp.Text() != "hello" {
		t.Fatalf("Complete text = %q, want hello", resp.Text())
	}

	bodies = script.requestBodies()
	if len(bodies) != 3 {
		t.Fatalf("request count after Complete = %d, want 3 (phase A plus one retry)", len(bodies))
	}
	firstAttempt := interactionsSteps(t, bodies[1])
	if !stepsContainFunc(firstAttempt, "function_call", "read_file") {
		t.Fatalf("first Complete attempt dropped the unsigned legacy tool call: %s", mustJSON(t, firstAttempt))
	}
	if bytes.Contains(bodies[1], []byte("legacy_sig")) {
		t.Fatalf("first Complete attempt sent legacy_sig: %s", bodies[1])
	}
	retrySteps := interactionsSteps(t, bodies[2])
	for _, step := range retrySteps {
		switch stepType(step) {
		case "function_call", "function_result":
			t.Fatalf("fallback retry still sent structured tool steps: %s", mustJSON(t, retrySteps))
		}
	}
	if !stepsContainTextPrefix(retrySteps, "model_output", "[historical tool call:") {
		t.Fatalf("fallback retry did not render the legacy tool call as model_output text: %s", mustJSON(t, retrySteps))
	}
	if !stepsContainTextPrefix(retrySteps, "user_input", "[historical tool result:") {
		t.Fatalf("fallback retry did not render the legacy tool result as user_input text: %s", mustJSON(t, retrySteps))
	}

	script.enqueueJSON(http.StatusBadRequest, googleSignature400)
	script.enqueueSSE(interactionsModelOutputSSE(model, "int_sse_ok"))
	stream, err := client.Stream(ctx, llm.Request{Provider: profile.ID(), Model: profile.Model(), Messages: baseMessages})
	if err != nil {
		t.Fatalf("Stream after signature 400: %v", err)
	}
	var (
		streamStarts int
		streamText   strings.Builder
		streamErr    error
	)
	for event := range stream.Events() {
		switch event.Type {
		case llm.StreamEventStreamStart:
			streamStarts++
		case llm.StreamEventTextDelta:
			streamText.WriteString(event.Delta)
		case llm.StreamEventError:
			streamErr = event.Err
		}
	}
	if closeErr := stream.Close(); closeErr != nil {
		t.Fatalf("close stream: %v", closeErr)
	}
	if streamErr != nil {
		t.Fatalf("Stream surfaced the signature 400 instead of recovering: %v", streamErr)
	}
	if streamStarts != 1 {
		t.Fatalf("StreamEventStreamStart count = %d, want exactly 1", streamStarts)
	}
	if streamText.String() != "hello" {
		t.Fatalf("stream text = %q, want hello", streamText.String())
	}

	bodies = script.requestBodies()
	if len(bodies) != 5 {
		t.Fatalf("request count after Stream = %d, want 5", len(bodies))
	}
	streamRetrySteps := interactionsSteps(t, bodies[4])
	if !stepsContainTextPrefix(streamRetrySteps, "model_output", "[historical tool call:") {
		t.Fatalf("stream fallback retry did not render the legacy tool call as model_output text: %s", mustJSON(t, streamRetrySteps))
	}
	if pending := script.pending(); pending != 0 {
		t.Fatalf("scripted endpoint still has %d unserved replies", pending)
	}
}

func stepsContainFunc(steps []any, kind, name string) bool {
	for _, step := range steps {
		m, _ := step.(map[string]any)
		if m["type"] == kind && m["name"] == name {
			return true
		}
	}
	return false
}

func stepsContainTextPrefix(steps []any, kind, prefix string) bool {
	for _, step := range steps {
		m, _ := step.(map[string]any)
		if m["type"] != kind {
			continue
		}
		content, _ := m["content"].([]any)
		for _, itemAny := range content {
			item, _ := itemAny.(map[string]any)
			text, _ := item["text"].(string)
			if strings.HasPrefix(text, prefix) {
				return true
			}
		}
	}
	return false
}
