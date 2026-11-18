package agent

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/chatcompletions"
	"primeradiant.com/evener/llm/providers/responses"
	"primeradiant.com/evener/llm/registry"
)

func TestToolCallHistoryWirePrefixSurvivesTranscriptResume(t *testing.T) {
	t.Parallel()
	const valid = " \t" + `{ "z": 1.0, "a": "<&>λ\u0061", "q": "a\"b" }` + "\r\n"
	const malformed = `{"x":broken}`
	const first, second = " \tλ<&>\n", "  next\r\n"
	good, bad := mustJSON(t, valid), mustJSON(t, malformed)
	for _, tc := range []struct {
		protocol llm.Protocol
		field    string
		response string
		thoughts []string
	}{
		{&responses.Protocol{}, "input", `{"output":[
			{"type":"reasoning","id":"r1","summary":[],"content":[
				{"type":"reasoning_text","text":` + mustJSON(t, first) + `},
				{"type":"reasoning_text","text":` + mustJSON(t, second) + `}]},
			{"type":"function_call","call_id":"c1","name":"f","arguments":` + good + `},
			{"type":"function_call","call_id":"c2","name":"f","arguments":` + bad + `}
		]}`, []string{first, second}},
		{&chatcompletions.Protocol{}, "messages", `{"choices":[{"message":{
			"role":"assistant","reasoning":` + mustJSON(t, first+second) + `,
			"tool_calls":[
				{"id":"c1","type":"function","function":{"name":"f","arguments":` + good + `}},
				{"id":"c2","type":"function","function":{"name":"f","arguments":` + bad + `}}
			]},"finish_reason":"tool_calls"}]}`, []string{first + second}},
	} {
		t.Run(tc.protocol.ID(), func(t *testing.T) {
			requests := make(chan []byte, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				requests <- body
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.response)
			}))
			t.Cleanup(srv.Close)
			row := registry.Resolved{
				Instance: "fixture", ModelID: "m", WireID: "m", Protocol: tc.protocol.ID(),
				Caps: registry.Caps{
					Fields: registry.Baseline(tc.protocol.ID()), ReplayReasoningText: new(true),
					ReasoningField: new("reasoning_content"),
				},
				Transport:  registry.Transport{BaseURL: srv.URL, Endpoint: "/completion", Auth: registry.AuthBearer},
				Credential: registry.Credential{Value: "test"},
			}
			send := func(messages []llm.Message) (llm.Response, []byte) {
				t.Helper()
				req := llm.ShapeRequest(llm.Request{
					Model: row.ModelID, Messages: messages, Tools: []llm.ToolDefinition{{Name: "f"}},
				}, row)
				resp, err := tc.protocol.Complete(t.Context(), req, row)
				if err != nil {
					t.Fatal(err)
				}
				return resp, <-requests
			}
			user := llm.User("u1")
			resp, _ := send([]llm.Message{user})
			result := llm.ToolResult("c1", "r1", false)
			result.Content = append(result.Content, llm.ToolResult("c2", "r2", true).Content...)
			turns := []schema.Turn{
				{Kind: schema.TurnUserInput, Message: user},
				{
					Kind: schema.TurnAssistant, Message: assistantHistoryMessage(resp.Message),
					ResponseProvider: row.Instance, ResponseRequestModel: row.ModelID,
					ResponseWireModel: row.WireID, ResponseProtocol: row.Protocol,
					ResponseReasoningRoute: row.ReasoningReplayRoute(),
				},
				{Kind: schema.TurnToolResults, Message: result},
			}
			scope := replayScope{Instance: row.Instance, Model: row.ModelID, Protocol: row.Protocol}
			scope.setReasoningTextTarget(row)
			_, warmBody := send(expandHistory(turns, scope))
			dir := t.TempDir()
			writeFindTranscript(t, dir, "argument-replay", time.Unix(1, 0), turns...)
			_, entries, skipped, err := readTranscript(t.Context(), transcriptPath(dir, "argument-replay"), "")
			if err != nil || skipped != 0 {
				t.Fatalf("read transcript: skipped %d, err %v", skipped, err)
			}
			resumed := ResumeHistory(entries)
			if rejected, ok := findToolCallInHistory(resumed, "c2"); !ok || rejected.RawArguments != malformed {
				t.Fatalf("restored rejected call = %+v", rejected)
			}
			coldHistory := expandHistory(resumed, scope)
			_, sameBody := send(coldHistory)
			if !bytes.Equal(warmBody, sameBody) {
				t.Errorf("resending unchanged history produced a different HTTP body after resume")
			}
			_, coldBody := send(append(coldHistory, llm.User("u2")))
			historyField := func(b []byte) json.RawMessage {
				t.Helper()
				var body map[string]json.RawMessage
				if err := json.Unmarshal(b, &body); err != nil {
					t.Fatal(err)
				}
				return body[tc.field]
			}
			warm, cold := historyField(warmBody), historyField(coldBody)
			for _, history := range []json.RawMessage{warm, cold} {
				got, thoughts := capturedToolTurn(t, history)
				if got["c1"] != valid || got["c2"] != "{}" {
					t.Errorf("outbound argument strings = %q, want original valid bytes and the rejected call's placeholder", got)
				}
				if !slices.Equal(thoughts, tc.thoughts) {
					t.Errorf("outbound reasoning parts = %q, want %q", thoughts, tc.thoughts)
				}
			}
			prefix := bytes.TrimSuffix(warm, []byte("]"))
			if len(prefix) == 0 || !bytes.HasPrefix(cold, append(bytes.Clone(prefix), ',')) {
				t.Errorf("warm history %s is not a byte-identical prefix of resumed history %s", warm, cold)
			}
		})
	}
}

func capturedToolTurn(t *testing.T, history json.RawMessage) (map[string]string, []string) {
	t.Helper()
	var items []struct {
		CallID    string `json:"call_id"`
		Type      string
		Arguments string
		Reasoning string          `json:"reasoning_content"`
		Content   json.RawMessage `json:"content"`
		ToolCalls []struct {
			ID       string
			Function struct{ Arguments string }
		} `json:"tool_calls"`
	}
	if err := json.Unmarshal(history, &items); err != nil {
		t.Fatal(err)
	}
	args := make(map[string]string)
	var reasoning []string
	for _, item := range items {
		if item.Reasoning != "" {
			reasoning = append(reasoning, item.Reasoning)
		}
		if item.Type == "reasoning" {
			var parts []struct{ Type, Text string }
			if err := json.Unmarshal(item.Content, &parts); err != nil {
				t.Fatal(err)
			}
			for _, p := range parts {
				if p.Type == "reasoning_text" {
					reasoning = append(reasoning, p.Text)
				}
			}
		}
		if item.Type == "function_call" {
			args[item.CallID] = item.Arguments
		}
		for _, call := range item.ToolCalls {
			args[call.ID] = call.Function.Arguments
		}
	}
	return args, reasoning
}
