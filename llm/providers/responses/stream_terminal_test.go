package responses

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"primeradiant.com/evener/llm"
)

func settledBoth(t *testing.T, frames string) []*llm.Response {
	body := []byte("data: " + strings.ReplaceAll(frames, "\n\n", "\n\ndata: ") + "\n\n")
	_, live := responsesEventsFromSSE(t, string(body))
	recorded, err := ExtractRecordedResponse(body, "gpt-5.5") // both paths must settle the same body
	if err != nil {
		t.Fatal(err)
	}
	return []*llm.Response{live, &recorded}
}

func summary(r *llm.Response) string {
	var parts []string
	for _, p := range r.Message.Content {
		if p.ToolCall != nil {
			parts = append(parts, "call")
		} else if p.Thinking == nil {
			parts = append(parts, p.Text+"|"+p.Phase)
		} else if p.Thinking.ResponsesIncomplete {
			parts = append(parts, "incomplete("+p.Thinking.Text+map[bool]string{true: "s", false: "c"}[len(p.Thinking.ResponsesSummaryParts) > 0]+")")
		} else {
			parts = append(parts, "think("+p.Thinking.Text+"+"+strings.Join(p.Thinking.Summary, "+")+")")
		}
	}
	return r.Finish.Reason + " " + strings.Join(parts, " ")
}

// A terminal payload settles only what the stream settled, whatever its own output holds.
func TestSettledResponsesFromEmptyTerminalOutput(t *testing.T) {
	t.Parallel()
	const nl, open, done = "\n\n", `{"type":"response.function_call_arguments.delta","call_id":"call_open","name":"probe","delta":"{}"}`, `{"type":"response.completed","response":{"id":"r","status":"completed","output":[]}}`

	for want, frames := range map[string]string{
		"content_filter ": `{"type":"response.output_text.delta","output_index":0,"delta":"filtered answer"}` + nl + `{"type":"response.incomplete","response":{"id":"r","status":"incomplete","incomplete_details":{"reason":"content_filter"},"output":[]}}`,
		"length incomplete(truncated thoughts) Hello world|final_answer": `{"type":"response.reasoning_summary_text.delta","output_index":0,"delta":"truncated thought"}` + nl + `{"type":"response.output_item.added","output_index":1,"item":{"type":"message","phase":"final_answer"}}` + nl + `{"type":"response.output_text.delta","output_index":1,"delta":"Hello"}` + nl + `{"type":"response.output_text.delta","output_index":1,"delta":" world"}` + nl + open + nl + `{"type":"response.output_item.done","output_index":1,"item":{"type":"message","content":[{"type":"output_text","text":""}]}}` + nl + `{"type":"response.incomplete","response":{"id":"r","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[]}}`,
		"stop one|commentary two!|final_answer ten|commentary":           `{"type":"response.output_item.added","output_index":2,"item":{"type":"message","phase":"final_answer"}}` + nl + `{"type":"response.output_text.delta","output_index":2,"delta":"two"}` + nl + `{"type":"response.output_item.added","output_index":10,"item":{"type":"message","phase":"commentary"}}` + nl + `{"type":"response.output_text.delta","output_index":10,"delta":"ten"}` + nl + `{"type":"response.output_item.added","output_index":1,"item":{"type":"message","phase":"commentary"}}` + nl + `{"type":"response.output_text.delta","output_index":1,"delta":"one"}` + nl + `{"type":"response.output_text.delta","output_index":2,"delta":"!"}` + nl + done,
		"length incomplete(cut offc)":                                    `{"type":"response.reasoning_text.delta","output_index":0,"delta":"cut off"}` + nl + `{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","status":"incomplete"}}` + nl + `{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[]}}`,
		"stop think(think+)":                                             `{"type":"response.reasoning_summary_text.delta","output_index":0,"delta":"think"}` + nl + `{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","status":"completed","content":[{"type":"reasoning_text","text":"think"}]}}` + nl + done,
		"stop think(+think) answer|":                                     open + nl + `{"type":"response.output_text.delta","output_index":0,"delta":"answer"}` + nl + `{"type":"response.completed","response":{"output":[{"type":"reasoning","summary":[{"type":"summary_text","text":"think"}]}]}}`,
		"stop think(one\n\ntwo+one+two)":                                 `{"type":"response.reasoning_summary_part.added"}` + nl + `{"type":"response.reasoning_summary_text.delta","output_index":0,"delta":"one"}` + nl + `{"type":"response.reasoning_summary_part.added"}` + nl + `{"type":"response.reasoning_summary_text.delta","output_index":0,"delta":"two"}` + nl + `{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","status":"completed"}}` + nl + done,
		"stop think(thought+think) answer|":                              `{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","status":"completed","content":[{"type":"reasoning_text","text":"thought"}],"summary":[{"type":"summary_text","text":"think"}]}}` + nl + `{"type":"response.output_text.delta","output_index":1,"delta":"answer"}` + nl + `{"type":"response.completed","response":{"output":[{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"think"}]}]}}`,
		"stop first|commentary second|final_answer":                      `{"type":"response.output_item.added","item":{"type":"message","id":"m1","phase":"commentary"}}` + nl + `{"type":"response.output_item.added","item":{"type":"message","id":"m2","phase":"final_answer"}}` + nl + `{"type":"response.output_text.delta","item_id":"m1","delta":"first"}` + nl + `{"type":"response.output_text.delta","item_id":"m2","delta":"second"}` + nl + done,
		"stop incomplete(thoughts) one|commentary two|final_answer":      `{"type":"response.reasoning_summary_part.added"}` + nl + `{"type":"response.reasoning_summary_text.delta","output_index":0,"delta":"thought"}` + nl + `{"type":"response.output_item.added","output_index":5,"item":{"type":"message","phase":"final_answer"}}` + nl + `{"type":"response.output_text.delta","output_index":5,"delta":"two"}` + nl + `{"type":"response.output_item.added","output_index":1,"item":{"type":"message","phase":"commentary"}}` + nl + `{"type":"response.output_text.delta","output_index":1,"delta":"one"}` + nl + done,
		"stop think(+eye) answer|":                                       `{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","status":"completed","encrypted_content":"opaque"}}` + nl + `{"type":"response.output_text.delta","output_index":1,"delta":"answer"}` + nl + `{"type":"response.completed","response":{"output":[{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"eye"}]}]}}`,
		"tool_calls think(+think) call":                                  `{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","call_id":"c1","name":"probe","arguments":"{}","status":"completed"}}` + nl + `{"type":"response.completed","response":{"status":"completed","output":[{"type":"reasoning","summary":[{"type":"summary_text","text":"think"}]}]}}`,
		// The terminal settles no answer, its only call unfinished, so recovery keeps the streamed text.
		"stop incomplete(thoughts) omitted|": `{"type":"response.reasoning_summary_text.delta","output_index":0,"delta":"thought"}` + nl + `{"type":"response.output_text.delta","output_index":1,"delta":"omitted"}` + nl + `{"type":"response.completed","response":{"output":[{"type":"reasoning","summary":[{"type":"summary_text","text":"thought"}]},{"type":"function_call","call_id":"c","name":"probe","arguments":"{}","status":"incomplete"}]}}`,
	} {
		for _, r := range settledBoth(t, frames) {
			if got := summary(r); got != want {
				t.Errorf("settled = %q, want %q", got, want)
			}
		}
	}
}

func TestRecoveredSummaryDisplaysAndReplaysAsSummary(t *testing.T) {
	t.Parallel()
	const frames = `{"type":"response.reasoning_summary_text.delta","output_index":0,"delta":"thought"}` + "\n\n" + `{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","status":"completed"}}` + "\n\n" + `{"type":"response.output_text.delta","output_index":1,"delta":"answer"}` + "\n\n" + `{"type":"response.completed","response":{"output":[{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"thought"}]}]}}`
	for _, r := range settledBoth(t, frames) {
		_, items, _ := toResponsesInput([]llm.Message{r.Message}, "")
		if got := jsonOf(t, items); r.ReasoningText() != "thought" || r.Text() != "answer" || !strings.Contains(got, `"summary_text"`) {
			t.Fatalf("reasoning %q, replay %s: want the summary shown and replayed as summary", r.ReasoningText(), got)
		}
	}
}

// An incomplete summary stays display-only: it shows as thought text, never as replayed content.
func TestIncompleteStreamedSummaryDoesNotReplay(t *testing.T) {
	t.Parallel()
	const frames = `{"type":"response.reasoning_summary_text.delta","output_index":0,"delta":"half"}` + "\n\n" + `{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[]}}`
	for _, r := range settledBoth(t, frames) {
		_, items, _ := toResponsesInput([]llm.Message{r.Message}, "")
		if th := r.Message.Content[0].Thinking; !th.ResponsesIncomplete || len(th.ResponsesSummaryParts) == 0 || r.ReasoningText() != "half" || len(items) != 0 {
			t.Fatalf("reasoning %+v, replay %s: want a display-only summary", th, jsonOf(t, items))
		}
	}
}

// The terminal settles an answer only when it carries text, a refusal or a finished call. Otherwise the
// stream's answer is recovered, minus a call the terminal marks unfinished. Both paths must settle alike.
func TestSettledTerminalAnswersAndDisownedCalls(t *testing.T) {
	t.Parallel()
	const nl = "\n\n"
	const reason = `"response":{"id":"r","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},`
	const blob = `{"type":"reasoning","id":"rs_1","encrypted_content":"blob"}`
	const think = `{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"think"}]}`
	const answer = `{"type":"response.output_text.delta","output_index":1,"delta":"answer"}`
	const call = `{"type":"function_call","call_id":"c1","name":"probe","arguments":"{}"}`
	for name, c := range map[string]struct {
		frames, text, finish string
		calls                int
	}{
		// Reasoning alone settles no answer, so the stream's text is recovered beside it.
		"blob only":    {answer + nl + `{"type":"response.incomplete",` + reason + `"output":[` + blob + `]}}`, "answer", "", 0},
		"summary only": {answer + nl + `{"type":"response.incomplete",` + reason + `"output":[` + think + `]}}`, "answer", "", 0},
		// The terminal settles its own answer, so the stream's is not recovered.
		"terminal text":    {answer + nl + `{"type":"response.incomplete",` + reason + `"output":[{"type":"message","content":[{"type":"output_text","text":"own"}]}]}}`, "own", "", 0},
		"terminal refusal": {answer + nl + `{"type":"response.incomplete",` + reason + `"output":[{"type":"message","content":[{"type":"refusal","text":"no"}]}]}}`, "", "", 0},
		// A wire refusal carries its text under "refusal"; an empty placeholder is no answer.
		"wire refusal":  {answer + nl + `{"type":"response.incomplete",` + reason + `"output":[{"type":"message","content":[{"type":"refusal","refusal":"no"}]}]}}`, "", "", 0},
		"empty refusal": {answer + nl + `{"type":"response.incomplete",` + reason + `"output":[{"type":"message","content":[{"type":"refusal"}]}]}}`, "answer", "", 0},
		// The terminal disowns the streamed call, so recovery must not restore it.
		"disowned call": {`{"type":"response.output_item.done","item":` + call + `}` + nl + `{"type":"response.incomplete",` + reason + `"output":[{"type":"function_call","call_id":"c1","name":"probe","arguments":"{}","status":"incomplete"}]}}`, "", "", 0},
		// Recovery keeps a call the terminal does not disown and drops the one it does.
		"recovered call":       {`{"type":"response.output_item.done","item":` + call + `}` + nl + answer + nl + `{"type":"response.incomplete",` + reason + `"output":[` + blob + `]}}`, "answer", "", 1},
		"disowned with answer": {`{"type":"response.output_item.done","item":` + call + `}` + nl + answer + nl + `{"type":"response.incomplete",` + reason + `"output":[` + blob + `,{"type":"function_call","call_id":"c1","name":"probe","arguments":"{}","status":"in_progress"}]}}`, "answer", "", 0},
		// A filtered answer was retracted, so nothing the stream sent is recovered.
		"filtered":                {answer + nl + `{"type":"response.incomplete","response":{"id":"r","status":"incomplete","incomplete_details":{"reason":"content_filter"},"output":[]}}`, "", "", 0},
		"filtered with reasoning": {answer + nl + `{"type":"response.incomplete","response":{"id":"r","status":"incomplete","incomplete_details":{"reason":"content_filter"},"output":[` + blob + `]}}`, "", "", 0},
		// Terminal reasoning content is no answer either, so the stream's text is still recovered.
		"terminal reasoning": {answer + nl + `{"type":"response.incomplete",` + reason + `"output":[{"type":"reasoning","id":"rs_2","content":[{"type":"reasoning_text","text":"thought"}]}]}}`, "answer", "", 0},
		// A completed response keeps the call the stream finished, even though the terminal lists it
		// unfinished: only an incomplete response may disown a call.
		"settled keeps call": {`{"type":"response.output_item.done","item":` + call + `}` + nl + `{"type":"response.completed","response":{"id":"r","status":"completed","output":[` + blob + `,{"type":"function_call","call_id":"c1","name":"probe","arguments":"{}","status":"incomplete"}]}}`, "", "tool_calls", 1},
	} {
		for _, r := range settledBoth(t, c.frames) {
			if got := r.Text(); got != c.text {
				t.Errorf("%s: settled text %q, want %q", name, got, c.text)
			}
			if calls := len(r.ToolCalls()); calls != c.calls {
				t.Errorf("%s: settled %d tool calls, want %d", name, calls, c.calls)
			}
			if c.finish != "" && r.Finish.Reason != c.finish {
				t.Errorf("%s: settled finish %q, want %q", name, r.Finish.Reason, c.finish)
			}
		}
	}
}

// A reasoning item the terminal repeats by ID lands once, keeping the native content the repeat
// omitted, and a search both the terminal and the stream reported is not duplicated by recovery.
func TestSettledTerminalRepeatKeepsNativeReasoning(t *testing.T) {
	t.Parallel()
	const nl = "\n\n"
	const done = `{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","status":"completed","content":[{"type":"reasoning_text","text":"native step"}]}}`
	const search = `{"type":"web_search_call","id":"ws_1","action":{"query":"q"}}`
	const answer = `{"type":"response.output_text.delta","output_index":1,"delta":"answer"}`
	const report = `"response":{"id":"r","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},`
	frames := done + nl + `{"type":"response.output_item.done","item":` + search + `}` + nl + answer + nl +
		`{"type":"response.incomplete",` + report + `"output":[{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"short summary"}]},` + search + `]}}`
	for _, r := range settledBoth(t, frames) {
		search := slices.DeleteFunc(slices.Clone(r.Message.Content), func(p llm.ContentPart) bool { return p.Kind != llm.ContentWebSearch })
		if r.Text() != "answer" || len(search) != 1 || r.Message.Content[0].Thinking == nil ||
			r.Message.Content[0].Thinking.Text != "native step" {
			t.Errorf("settled %+v, want the recovered answer and one reasoning item keeping the native text", r.Message.Content)
		}
	}
}

func TestIncompleteResponseKeepsUnsettledToolArgumentsOutOfFinalResponse(t *testing.T) {
	t.Parallel()
	const delta = `{"type":"response.function_call_arguments.delta","call_id":"call_unfinished","name":"probe","delta":"{}"}`
	const unfinished = `{"type":"function_call","call_id":"call_unfinished","name":"probe","arguments":"{}","status":"incomplete"}`
	const running = `{"type":"function_call","call_id":"call_running","name":"probe","arguments":"{}","status":"in_progress"}`
	const completed = `{"type":"function_call","call_id":"call_completed","name":"probe","arguments":"{}","status":"completed"}`
	const unstamped = `{"type":"function_call","call_id":"call_unstamped","name":"probe","arguments":"{}"}`
	frame := func(payload string) string { return "data: " + payload + "\n\n" }
	for _, terminalType := range []string{"response.completed", "response.incomplete"} {
		for _, mode := range []string{"empty", "terminal", "streamed", "both", "mismatch"} {
			t.Run(terminalType+"/"+mode, func(t *testing.T) {
				var wire strings.Builder
				wire.WriteString(frame(delta))
				output := "[]"
				var wantCalls []string
				if mode == "terminal" || mode == "both" || mode == "mismatch" {
					output = "[" + unfinished + "," + running + "," + completed + "," + unstamped + "]"
				}
				if mode == "streamed" || mode == "both" || mode == "mismatch" {
					items := []string{unfinished, running, completed, unstamped}
					if mode == "mismatch" {
						items = items[:3]
					}
					for _, item := range items {
						wire.WriteString(frame(`{"type":"response.output_item.done","item":` + item + `}`))
					}
				}
				if mode != "empty" {
					wantCalls = []string{"call_completed", "call_unstamped"}
				}
				wire.WriteString(frame(`{"type":"` + terminalType + `","response":{"status":"incomplete","output":` + output + `}}`))
				body := wire.String()
				recorded, err := ExtractRecordedResponse([]byte(body), "gpt-5.5")
				if err != nil {
					t.Error(err)
				}
				events, final := responsesEventsFromSSE(t, body)
				if event := toolEvent(t, events, llm.StreamEventToolCallDelta); event == nil || event.ID != "call_unfinished" || string(event.Arguments) != "{}" {
					t.Fatalf("transient tool delta = %+v", event)
				}
				for _, response := range []*llm.Response{final, &recorded} {
					if response == nil || response.Finish.Reason != llm.FinishReasonLength || response.Finish.Raw != "incomplete" {
						t.Fatalf("incomplete response = %+v", response)
					}
					var ids []string
					for _, call := range response.ToolCalls() {
						ids = append(ids, call.ID)
					}
					if !slices.Equal(ids, wantCalls) {
						t.Errorf("settled tool calls = %v, want %v", ids, wantCalls)
					}
					mismatch := slices.ContainsFunc(response.Warnings, func(w llm.Warning) bool {
						return w.Code == "responses_output_item_count_mismatch"
					})
					if mismatch != (mode == "mismatch") {
						t.Errorf("settled count mismatch warning = %v, want %v", mismatch, mode == "mismatch")
					}
				}
				if mode == "streamed" && !hasEventType(events, llm.StreamEventProviderEvent) {
					t.Error("stream omitted the raw unfinished completion")
				}
			})
		}
	}
}

// abortingSSEServer writes prefix and then hijacks and closes the connection
// without a terminating chunk, so the client's read fails mid-stream. That is
// evidence about the transport, never about which endpoints the model
// implements.
func abortingSSEServer(t *testing.T, prefix string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		_, _ = conn.Write([]byte(prefix))
		if tcp, ok := conn.(*net.TCPConn); ok {
			// A reset rather than a clean FIN, so the client reads an error
			// instead of an orderly EOF.
			_ = tcp.SetLinger(0)
		}
		_ = conn.Close()
	}))
	t.Cleanup(srv.Close)
	return srv
}

// isUnsupportedEndpoint reports whether err is the endpoint-capability
// sentinel, which is the one terminal error that routes the caller off this
// endpoint entirely.
func isUnsupportedEndpoint(err error) bool {
	_, ok := errors.AsType[*llm.UnsupportedEndpointError](err)
	return ok
}

// streamTerminalError drains a stream and returns its terminal error event.
func streamTerminalError(t *testing.T, srv *httptest.Server, req llm.Request) error {
	t.Helper()
	return streamTerminalErrorWithClient(t, srv.Client(), srv, req)
}
func streamTerminalErrorWithClient(t *testing.T, client *http.Client, srv *httptest.Server, req llm.Request) error {
	t.Helper()
	s, err := (&Protocol{Client: client}).Stream(t.Context(), req, liveRes(srv, openaiCaps))
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var terminal error
	for ev := range s.Events() {
		if ev.Type == llm.StreamEventError {
			terminal = ev.Err
		}
	}
	return terminal
}

// TestStreamTerminalErrorsSeparateTheFourEndings pins the classification the
// Responses transport needs and the shared runner's single IncompleteMsg
// cannot express. Only one of the four says anything about endpoint support:
// a clean close with nothing this decoder recognizes. A stall, a broken read
// and a truncated-but-real stream are all evidence about the transport or the
// provider, and misreading any of them as "this model has no /v1/responses"
// sends the request to an endpoint that cannot serve it (#484).
func TestStreamTerminalErrorsSeparateTheFourEndings(t *testing.T) {
	t.Parallel()
	t.Run("stall", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			req := userReq("hi")
			req.AdapterTimeout = &llm.AdapterTimeout{StreamRead: time.Millisecond}
			client, srv := stallingSSEClient(t, twoResponsesEvents)
			err := streamTerminalErrorWithClient(t, client, srv, req)
			if _, ok := errors.AsType[*llm.StreamError](err); !ok {
				t.Fatalf("stalled stream error = %v, want a retryable stream error", err)
			}
			if !errors.Is(err, llm.ErrResponseIdleTimeout) {
				t.Fatalf("a stall must carry the response-byte idle timeout as its cause: %v", err)
			}
			if isUnsupportedEndpoint(err) {
				t.Fatalf("silence is not evidence about endpoint support: %v", err)
			}
		})
	})

	t.Run("read failure", func(t *testing.T) {
		err := streamTerminalError(t, abortingSSEServer(t, twoResponsesEvents), userReq("hi"))
		if _, ok := errors.AsType[*llm.StreamError](err); !ok {
			t.Fatalf("broken stream error = %v, want a retryable stream error", err)
		}
		if errors.Unwrap(err) == nil {
			t.Fatalf("a broken read must carry the read failure as its cause: %v", err)
		}
		if errors.Is(err, llm.ErrResponseIdleTimeout) || errors.Is(err, llm.ErrSSEReadTimeout) {
			t.Fatalf("a broken read is not a stall: %v", err)
		}
		if isUnsupportedEndpoint(err) {
			t.Fatalf("a broken read is not evidence about endpoint support: %v", err)
		}
	})

	t.Run("no responses events", func(t *testing.T) {
		// A clean 200 carrying only events this decoder does not recognize as
		// the Responses API: the model likely does not implement /v1/responses.
		srv, _ := server(t, 200, "event: ping\ndata: {\"type\":\"ping\"}\n\n")
		err := streamTerminalError(t, srv, userReq("hi"))
		if !isUnsupportedEndpoint(err) {
			t.Fatalf("a clean close with no Responses event is the endpoint sentinel: %v", err)
		}
	})

	t.Run("truncated", func(t *testing.T) {
		// Real Responses events, then a clean close with no response.completed.
		srv, _ := server(t, 200, twoResponsesEvents)
		err := streamTerminalError(t, srv, userReq("hi"))
		if _, ok := errors.AsType[*llm.StreamError](err); !ok {
			t.Fatalf("truncated stream error = %v, want a retryable stream error", err)
		}
		if cause := errors.Unwrap(err); cause != nil {
			t.Fatalf("a clean close has no read failure to report, got cause %v", cause)
		}
		if isUnsupportedEndpoint(err) {
			t.Fatalf("a stream that produced Responses events proves the endpoint served it: %v", err)
		}
	})
}

// A gateway can answer 200 OK with an error payload and no Responses event at
// all: lunaroute wraps an upstream rejection as a bare data frame, and
// OpenAI-compatible endpoints send the flat or nested envelope under an
// "error" event. Each of those is the provider reporting a failure, not
// evidence about which protocol the model speaks, so it must classify as a
// retryable stream error. Reading it as the endpoint sentinel makes a
// transient upstream failure permanent and the turn gets no retry.
func TestStreamTerminalErrorsDecodeInBandErrorFrames(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
	}{
		{
			name: "bare data frame",
			body: "data: {\"error\":{\"code\":null,\"message\":\"upstream returned status 400\",\"type\":\"server_error\"}}\n\n",
		},
		{
			name: "error event with flat envelope",
			body: "event: error\ndata: {\"message\":\"upstream returned status 400\",\"type\":\"server_error\"}\n\n",
		},
		{
			name: "error event with error envelope",
			body: "event: error\ndata: {\"error\":{\"message\":\"upstream returned status 400\",\"type\":\"server_error\"}}\n\n",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			srv, _ := server(t, 200, testCase.body)
			err := streamTerminalError(t, srv, userReq("hi"))
			if err == nil {
				t.Fatal("a stream carrying an error payload must report it")
			}
			if isUnsupportedEndpoint(err) {
				t.Fatalf("an error payload is not evidence about endpoint support: %v", err)
			}
			if got := llm.Classify(err); got != llm.ErrorClassRetryable {
				t.Fatalf("Classify(%v) = %v, want Retryable so the retry chain runs", err, got)
			}
			var le llm.Error
			if !errors.As(err, &le) {
				t.Fatalf("in-band error %T does not implement llm.Error", err)
			}
			if le.Provider() != "oai-prod" {
				t.Fatalf("provider = %q, want the resolved instance", le.Provider())
			}
		})
	}
}
