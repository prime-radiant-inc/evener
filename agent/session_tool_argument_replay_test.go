package agent

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/provider"
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
	const serial = "9007199254740993"
	const scale = "6.250e+3"
	unsigned := `{"type":"reasoning.text","id":"part-a","format":"fmt-a","index":7,"text":` + mustJSON(t, first) + `,"serial":` + serial + `},
		{"type":"reasoning.text","format":"fmt-b","index":2,"text":` + mustJSON(t, second) + `}`
	encrypted := `{"type":"reasoning.encrypted","id":"sealed","data":"opaque","serial":` + serial + `,"scale":` + scale + `}`
	chat := func(details ...string) string {
		return `{"choices":[{"message":{
		"role":"assistant","reasoning":` + mustJSON(t, first+second) + `,
		"reasoning_details":[` + strings.Join(details, ",") + `],
		"tool_calls":[
			{"id":"c1","type":"function","function":{"name":"f","arguments":` + good + `}},
			{"id":"c2","type":"function","function":{"name":"f","arguments":` + bad + `}}
		]},"finish_reason":"tool_calls"}]}`
	}
	for _, tc := range []struct {
		protocol    llm.Protocol
		field       string
		replayField string
		response    string
		thoughts    []string
	}{
		{&responses.Protocol{}, "input", "", `{"output":[
			{"type":"reasoning","id":"r1","summary":[],"content":[
				{"type":"reasoning_text","text":` + mustJSON(t, first) + `},
				{"type":"reasoning_text","text":` + mustJSON(t, second) + `}]},
			{"type":"function_call","call_id":"c1","name":"f","arguments":` + good + `},
			{"type":"function_call","call_id":"c2","name":"f","arguments":` + bad + `}
		]}`, []string{first, second}},
		{&chatcompletions.Protocol{}, "messages", "reasoning_content", chat(unsigned, encrypted), []string{first + second}},
		{&chatcompletions.Protocol{}, "messages", "reasoning_details", chat(unsigned), []string{first, second}},
	} {
		name := tc.protocol.ID()
		if tc.replayField != "" {
			name += "/" + tc.replayField
		}
		t.Run(name, func(t *testing.T) {
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
					ReasoningField: new(tc.replayField),
				},
				Transport:  registry.Transport{BaseURL: srv.URL, Endpoint: "/completion", Auth: registry.AuthBearer},
				Credential: registry.Credential{Value: "test"},
			}
			send := func(messages []llm.Message, target registry.Resolved) (llm.Response, []byte) {
				t.Helper()
				req := llm.ShapeRequest(llm.Request{
					Model: target.ModelID, Messages: messages, Tools: []llm.ToolDefinition{{Name: "f"}},
				}, target)
				resp, err := tc.protocol.Complete(t.Context(), req, target)
				if err != nil {
					t.Fatal(err)
				}
				return resp, <-requests
			}
			user := llm.User("u1")
			resp, _ := send([]llm.Message{user}, row)
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
			scope.setReasoningTextTarget(provider.FromResolved(row, nil))
			_, warmBody := send(expandHistory(turns, scope), row)
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
			_, sameBody := send(coldHistory, row)
			if !bytes.Equal(warmBody, sameBody) {
				t.Errorf("resending unchanged history produced a different HTTP body after resume")
			}
			_, coldBody := send(append(coldHistory, llm.User("u2")), row)
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
				got, thoughts, details := capturedToolTurn(t, history)
				if got["c1"] != valid || got["c2"] != "{}" {
					t.Errorf("outbound argument strings = %q, want original valid bytes and the rejected call's placeholder", got)
				}
				if !slices.Equal(thoughts, tc.thoughts) {
					t.Errorf("outbound reasoning parts = %q, want %q", thoughts, tc.thoughts)
				}
				var want []capturedChatDetail
				switch tc.replayField {
				case "reasoning_details":
					want = []capturedChatDetail{
						{Type: "reasoning.text", ID: "part-a", Format: "fmt-a", Index: 7, Text: first, Serial: json.Number(serial)},
						{Type: "reasoning.text", Format: "fmt-b", Index: 2, Text: second},
					}
				case "reasoning_content":
					want = []capturedChatDetail{{Type: "reasoning.encrypted", ID: "sealed", Data: "opaque", Serial: json.Number(serial), Scale: json.Number(scale)}}
				}
				if !slices.Equal(details, want) {
					t.Errorf("outbound native details = %#v, want %#v", details, want)
				}
			}
			prefix := bytes.TrimSuffix(warm, []byte("]"))
			if len(prefix) == 0 || !bytes.HasPrefix(cold, append(bytes.Clone(prefix), ',')) {
				t.Errorf("warm history %s is not a byte-identical prefix of resumed history %s", warm, cold)
			}
			if tc.replayField == "reasoning_details" {
				for _, enrolled := range []bool{false, true} {
					target := row
					target.Caps.ReplayReasoningText = new(enrolled)
					if enrolled {
						target.ModelID = "other-model"
					}
					profile := provider.FromResolved(target, nil)
					dst := replayScope{Instance: target.Instance, Model: target.ModelID, Protocol: target.Protocol, InFlightFrom: len(resumed), DropThinking: dropsReplayedThinking(profile)}
					dst.setReasoningTextTarget(profile)
					_, sent := send(expandHistory(resumed, dst), target)
					calls, thoughts, details := capturedToolTurn(t, historyField(sent))
					if len(thoughts) != 0 || len(details) != 0 || calls["c1"] != valid {
						t.Errorf("ineligible target %s replayed reasoning %v or lost tool history", target.ModelID, thoughts)
					}
				}
			}
		})
	}
}

type capturedChatDetail struct {
	Type, ID, Format, Text, Data string
	Index                        int
	Serial, Scale                json.Number
}

func capturedToolTurn(t *testing.T, history json.RawMessage) (map[string]string, []string, []capturedChatDetail) {
	t.Helper()
	var items []struct {
		CallID           string `json:"call_id"`
		Type             string
		Arguments        string
		Reasoning        string               `json:"reasoning_content"`
		ReasoningDetails []capturedChatDetail `json:"reasoning_details"`
		Content          json.RawMessage      `json:"content"`
		ToolCalls        []struct {
			ID       string
			Function struct{ Arguments string }
		} `json:"tool_calls"`
	}
	if err := json.Unmarshal(history, &items); err != nil {
		t.Fatal(err)
	}
	args := make(map[string]string)
	var reasoning []string
	var details []capturedChatDetail
	for _, item := range items {
		if item.Reasoning != "" {
			reasoning = append(reasoning, item.Reasoning)
		}
		for _, detail := range item.ReasoningDetails {
			if detail.Type != "reasoning.encrypted" {
				reasoning = append(reasoning, detail.Text)
			}
		}
		details = append(details, item.ReasoningDetails...)
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
	return args, reasoning, details
}
