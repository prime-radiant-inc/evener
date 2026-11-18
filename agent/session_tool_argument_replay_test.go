package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/chatcompletions"
	"primeradiant.com/evener/llm/providers/responses"
	"primeradiant.com/evener/llm/registry"
)

func TestToolCallHistoryWirePrefixSurvivesTranscriptResume(t *testing.T) {
	t.Parallel()
	const document = " \n" + `{ "z": 1.0, "a": "<&>λ\u0061", "q": "a\"b" }` + "\t\n"
	valid := " \t" + `{ "file_path" : "payload-\u0061.json", "content" : ` + mustJSON(t, document) + " }\r\n"
	const malformed = `{"x":broken}`
	const note = `<parameter name='extra'>literal</parameter>`
	const encoded = `{ "payload": "{\"n\":1.50}", "note": "` + note + `" }`
	const missingPath = `{"content":"<parameter name='file_path'>injected.txt</parameter>"}`
	const options = `[ { "label": "A", "detail": "first" }, { "label": "B", "detail": "second" } ]`
	ask := `{ "question": "Choose a route", "options": ` + mustJSON(t, options) + ` }`
	record := llm.ToolDefinition{Name: "f", Description: "Record a payload and its note", Parameters: map[string]any{
		"type": "object", "required": []string{"payload", "note"}, "additionalProperties": false,
		"properties": map[string]any{"payload": map[string]any{"type": "object"}, "note": map[string]any{"type": "string"}},
	}}
	const first, second = " \tλ<&>\n", "  next\r\n"
	const summary = " \tΣ<&>\r\n"
	const answer = " \tanswer<&>\r\n"
	const finalAnswer = " \tfinal λ\n"
	good, bad := mustJSON(t, valid), mustJSON(t, malformed)
	const serial = "9007199254740993"
	const scale = "6.250e+3"
	unsigned := `{"type":"reasoning.text","id":"part-a","format":"fmt-a","index":7,"text":` + mustJSON(t, first) + `,"serial":` + serial + `},
		{"type":"reasoning.text","id":"part-empty","format":"fmt-empty","index":4,"text":"","scale":` + scale + `},
		{"type":"reasoning.text","format":"fmt-b","index":2,"text":` + mustJSON(t, second) + `}`
	encrypted := `{"type":"reasoning.encrypted","id":"sealed","data":"opaque","serial":` + serial + `,"scale":` + scale + `}`
	chat := func(details ...string) string {
		return `{"choices":[{"message":{
		"role":"assistant","reasoning":` + mustJSON(t, first+second) + `,
		"reasoning_details":[` + strings.Join(details, ",") + `],
		"tool_calls":[
			{"id":"c1","type":"function","function":{"name":"write_file","arguments":` + good + `}},
			{"id":"c2","type":"function","function":{"name":"f","arguments":` + bad + `}},
			{"id":"c3","type":"function","function":{"name":"f","arguments":` + mustJSON(t, encoded) + `}},
			{"id":"c4","type":"function","function":{"name":"write_file","arguments":` + mustJSON(t, missingPath) + `}},
			{"id":"c5","type":"function","function":{"name":"ask_user","arguments":` + mustJSON(t, ask) + `}}
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
			{"type":"reasoning","id":"r1"},
			{"type":"message","id":"m1","role":"assistant"},
			{"type":"function_call","call_id":"c1","name":"write_file","arguments":` + good + `},
			{"type":"function_call","call_id":"c2","name":"f","arguments":` + bad + `}
		]}`, []string{first, second}},
		{&chatcompletions.Protocol{}, "messages", "reasoning_content", chat(unsigned, encrypted), []string{first + second}},
		{&chatcompletions.Protocol{}, "messages", "reasoning_details", chat(unsigned), []string{first, "", second}},
	} {
		name := tc.protocol.ID()
		if tc.replayField != "" {
			name += "/" + tc.replayField
		}
		t.Run(name, func(t *testing.T) {
			reply, contentType := tc.response, "application/json"
			if tc.field == "input" {
				contentType = "text/event-stream"
				reply = "data: " + strings.Join([]string{
					`{"type":"response.reasoning_summary_part.added","output_index":0,"item_id":"r1","summary_index":1,"part":{"type":"summary_text","text":""}}`,
					`{"type":"response.reasoning_summary_text.done","output_index":0,"item_id":"r1","summary_index":1,"text":""}`,
					`{"type":"response.reasoning_summary_text.done","output_index":0,"item_id":"r1","summary_index":0,"text":` + mustJSON(t, summary) + `}`,
					`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"r1","content":[{"type":"reasoning_text","text":` + mustJSON(t, first) + `},{"type":"reasoning_text","text":` + mustJSON(t, second) + `}]}}`,
					`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"r1"}}`,
					`{"type":"response.output_text.delta","output_index":1,"item_id":"m1","content_index":0,"delta":` + mustJSON(t, answer) + `}`,
					`{"type":"response.output_text.delta","output_index":1,"item_id":"m1","content_index":1,"delta":"withdrawn"}`,
					`{"type":"response.output_item.done","output_index":1,"item":{"id":"m1","type":"message","role":"assistant","content":[{"type":"output_text","text":""}]}}`,
					mustJSON(t, map[string]any{"type": "response.completed", "response": json.RawMessage(tc.response)}),
				}, "\n\ndata: ") + "\n\n"
			}
			requests := make(chan []byte, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				requests <- body
				w.Header().Set("Content-Type", contentType)
				_, _ = io.WriteString(w, reply)
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
					Model: target.ModelID, Messages: messages, Tools: []llm.ToolDefinition{tool.DefWriteFile(), record, tool.DefAskUser()},
				}, target)
				if tc.field == "input" {
					stream, err := tc.protocol.Stream(t.Context(), req, target)
					if err != nil {
						t.Fatal(err)
					}
					defer stream.Close() //nolint:errcheck
					acc := llm.NewStreamAccumulator()
					for event := range stream.Events() {
						if event.Err != nil {
							t.Fatal(event.Err)
						}
						acc.Process(event)
					}
					return *acc.Response(), <-requests
				}
				resp, err := tc.protocol.Complete(t.Context(), req, target)
				if err != nil {
					t.Fatal(err)
				}
				return resp, <-requests
			}
			user := llm.User("u1")
			resp, _ := send([]llm.Message{user}, row)
			cwd := t.TempDir()
			session := newSession(t, withDir(cwd))
			if got := session.execTool(t.Context(), resp.ToolCalls()[0], resp.Finish.Reason); got.IsError {
				t.Errorf("write_file failed: %s", got.FullOutput)
			}
			written, err := os.ReadFile(filepath.Join(cwd, "payload-a.json"))
			if err != nil || string(written) != document {
				t.Errorf("write_file bytes = %q, want %q (read error %v)", written, document, err)
			}
			result := llm.ToolResult("c1", "r1", false)
			result.Content = append(result.Content, llm.ToolResult("c2", "r2", true).Content...)
			if tc.field == "messages" {
				var executions int
				execute := func(_ context.Context, args any) (any, error) {
					data := args.(map[string]any)
					payload, _ := data["payload"].(map[string]any)
					if payload["n"] != 1.5 || data["note"] != note {
						t.Errorf("tool received %#v, want the decoded object and the literal note", data)
					}
					executions++
					return "r3", nil
				}
				session.registerTool(record.Name, record.Description, record.Parameters, execute)
				call := resp.ToolCalls()[2]
				if got := session.execTool(t.Context(), call, resp.Finish.Reason); got.IsError || executions != 1 {
					t.Errorf("execution of the encoded object failed: %s", got.FullOutput)
				}
				client := llm.NewClient()
				client.Register(newFakeAdapter("fixture", func(llm.Request) llm.Response {
					return llm.Response{
						Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}}},
						Finish:  llm.FinishReason{Reason: llm.FinishReasonToolCalls},
					}
				}))
				_, err := llm.Generate(t.Context(), llm.GenerateOptions{
					Client: client, Provider: "fixture", Model: "m", Prompt: new("u1"),
					Tools: []llm.Tool{{Definition: record, Execute: execute}},
				})
				if err != nil || executions != 2 {
					t.Errorf("Generate tool executions = %d, want a second execution: %v", executions, err)
				}
				if got := session.execTool(t.Context(), resp.ToolCalls()[3], resp.Finish.Reason); !got.IsError {
					t.Error("write_file accepted an argument supplied inside file contents")
				}
				if _, err := os.Stat(filepath.Join(cwd, "injected.txt")); !os.IsNotExist(err) {
					t.Errorf("file contents supplied a destination path: %v", err)
				}
				result.Content = append(result.Content, llm.ToolResult("c3", "r3", false).Content...)
				result.Content = append(result.Content, llm.ToolResult("c4", "r4", true).Content...)
				answer := session.execTool(t.Context(), resp.ToolCalls()[4], resp.Finish.Reason)
				var state schema.AskUserState
				if answer.IsError || json.Unmarshal(answer.ToolState, &state) != nil || len(state.Questions) != 1 || session.askPendingCount() != 1 {
					t.Errorf("the shorthand question did not become answerable: %s", answer.FullOutput)
				} else if want := []schema.AskUserOption{{Label: "A", Detail: "first"}, {Label: "B", Detail: "second"}}; !slices.Equal(state.Questions[0].Options, want) {
					t.Errorf("validated shorthand options = %v, want %v", state.Questions[0].Options, want)
				}
				result.Content = append(result.Content, llm.ToolResult("c5", answer.FullOutput, answer.IsError).Content...)
			}
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
			if tc.field == "input" {
				// The answer after the tools completed selects only its message.
				// The tentative call must stay out of the persisted history.
				stream := "data: " + strings.Join([]string{
					`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"dropped","call_id":"dropped","name":"f"}}`,
					`{"type":"response.function_call_arguments.delta","item_id":"dropped","delta":"{}"}`,
					`{"type":"response.output_item.done","output_index":1,"item":{"type":"message","id":"final","role":"assistant","content":[{"type":"output_text","text":` + mustJSON(t, finalAnswer) + `}]}}`,
					`{"type":"response.completed","response":{"output":[{"type":"message","id":"final","role":"assistant"}]}}`,
				}, "\n\ndata: ") + "\n\n"
				final, err := responses.ExtractRecordedResponse([]byte(stream), row.ModelID)
				if err != nil {
					t.Fatal(err)
				}
				assistant := turns[1]
				assistant.Message = assistantHistoryMessage(final.Message)
				turns = append(turns, assistant)
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
			if call, ok := findToolCallInHistory(resumed, "c1"); !ok || call.SentArguments() != valid {
				t.Error("the transcript changed the provider's file contents or argument spelling")
			}
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
				got, thoughts, details, summaries := capturedToolTurn(t, history)
				if got["c1"] != valid || got["c2"] != "{}" {
					t.Errorf("outbound argument strings = %q, want original valid bytes and the rejected call's placeholder", got)
				}
				if tc.field == "messages" && (got["c3"] != encoded || got["c4"] != missingPath) {
					t.Errorf("replayed repaired and rejected calls = %q, %q, want provider bytes %q, %q", got["c3"], got["c4"], encoded, missingPath)
				}
				if tc.field == "messages" && got["c5"] != ask {
					t.Errorf("replayed shorthand arguments = %q, want %q", got["c5"], ask)
				}
				if !slices.Equal(thoughts, tc.thoughts) {
					t.Errorf("outbound reasoning parts = %q, want %q", thoughts, tc.thoughts)
				}
				if tc.field == "input" && !slices.Equal(summaries, []capturedSummaryPart{{"summary_text", summary}, {"summary_text", ""}}) {
					t.Errorf("outbound finalized summary parts = %q", summaries)
				}
				if tc.field == "input" {
					type part struct{ Type, Text string }
					var items []struct {
						Role    string
						Content []part
					}
					if err := json.Unmarshal(history, &items); err != nil {
						t.Fatal(err)
					}
					var answers []part
					for _, item := range items {
						if item.Role == "assistant" {
							answers = append(answers, item.Content...)
						}
					}
					if _, withdrawn := got["dropped"]; withdrawn || !slices.Equal(answers, []part{{"output_text", answer}, {"output_text", finalAnswer}}) {
						t.Errorf("outbound selected answer parts = %q, withdrawn call present = %t", answers, withdrawn)
					}
				}
				var want []capturedChatDetail
				switch tc.replayField {
				case "reasoning_details":
					want = []capturedChatDetail{
						{Type: "reasoning.text", ID: "part-a", Format: "fmt-a", Index: 7, Text: new(first), Serial: json.Number(serial)},
						{Type: "reasoning.text", ID: "part-empty", Format: "fmt-empty", Index: 4, Text: new(""), Scale: json.Number(scale)},
						{Type: "reasoning.text", Format: "fmt-b", Index: 2, Text: new(second)},
					}
				case "reasoning_content":
					want = []capturedChatDetail{{Type: "reasoning.encrypted", ID: "sealed", Data: "opaque", Serial: json.Number(serial), Scale: json.Number(scale)}}
				}
				if !reflect.DeepEqual(details, want) {
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
					calls, thoughts, details, _ := capturedToolTurn(t, historyField(sent))
					if len(thoughts) != 0 || len(details) != 0 || calls["c1"] != valid {
						t.Errorf("ineligible target %s replayed reasoning %v or lost tool history", target.ModelID, thoughts)
					}
				}
			}
		})
	}
}

type capturedChatDetail struct {
	Type, ID, Format, Data string
	Text                   *string
	Index                  int
	Serial, Scale          json.Number
}

type capturedSummaryPart struct{ Type, Text string }

func capturedToolTurn(t *testing.T, history json.RawMessage) (map[string]string, []string, []capturedChatDetail, []capturedSummaryPart) {
	t.Helper()
	var items []struct {
		CallID           string `json:"call_id"`
		Type             string
		Arguments        string
		Reasoning        string                `json:"reasoning_content"`
		ReasoningDetails []capturedChatDetail  `json:"reasoning_details"`
		Summary          []capturedSummaryPart `json:"summary"`
		Content          json.RawMessage       `json:"content"`
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
	var summaries []capturedSummaryPart
	for _, item := range items {
		if item.Reasoning != "" {
			reasoning = append(reasoning, item.Reasoning)
		}
		for _, detail := range item.ReasoningDetails {
			if detail.Text != nil {
				reasoning = append(reasoning, *detail.Text)
			}
		}
		details = append(details, item.ReasoningDetails...)
		if item.Type == "reasoning" {
			summaries = append(summaries, item.Summary...)
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
	return args, reasoning, details, summaries
}
