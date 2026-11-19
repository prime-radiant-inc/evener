package responses

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func TestCompleteReplaysNativeReasoning(t *testing.T) {
	const native = `{"content":[{"type":"reasoning_text","text":" α\n "},{"type":"reasoning_text","text":""}],"id":"rs_native","summary":[{"type":"summary_text","text":" summary "}],"type":"reasoning"}`
	const display = `{"content":[{"type":"summary_text","text":"display"}],"id":"rs_display","summary":[],"type":"reasoning"}`
	const mixedContent = `{"id":"rs_content","type":"reasoning","content":[{"type":"reasoning_text","text":"valid"},{"type":"summary_text","text":"wrong slot"}]}`
	const mixedSummary = `{"id":"rs_summary","type":"reasoning","content":[{"type":"reasoning_text","text":"valid"}],"summary":[{"type":"reasoning_text","text":"wrong slot"}]}`
	const incomplete = `{"id":"rs_cutoff","type":"reasoning","status":"incomplete","content":[{"type":"reasoning_text","text":"partial"}]}`
	const inProgress = `{"id":"rs_started","type":"reasoning","status":"in_progress","content":[{"type":"reasoning_text","text":"partial"}]}`
	const call = `{"arguments":"{}","call_id":"call_1","name":"f","type":"function_call"}`
	issuer, _ := server(t, http.StatusOK, `{"status":"completed","output":[`+native+`,`+display+`,`+mixedContent+`,`+mixedSummary+`,`+incomplete+`,`+inProgress+`,`+call+`]}`)
	p := &Protocol{}
	req := userReq("hello")
	resp, err := p.Complete(t.Context(), req, liveRes(issuer, openaiCaps))
	if err != nil {
		t.Fatal(err)
	}
	req.Messages = append(req.Messages, resp.Message, llm.ToolResult("call_1", "done", false))
	for _, auth := range []string{registry.AuthBearer, registry.AuthHeader} {
		t.Run(auth, func(t *testing.T) {
			secondNative := strings.Replace(native, "rs_native", "rs_next", 1)
			destination, wire := server(t, http.StatusOK, `{"status":"completed","output":[`+secondNative+`,{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}]}`)
			res := liveRes(destination, openaiCaps)
			res.Transport.Auth, res.Transport.AuthHeader = auth, "X-Token"
			complete := func(req llm.Request) (llm.Response, []json.RawMessage) {
				t.Helper()
				resp, err := p.Complete(t.Context(), req, res)
				if err != nil {
					t.Fatal(err)
				}
				var body struct{ Input []json.RawMessage }
				if err := json.Unmarshal(wire.raw, &body); err != nil {
					t.Fatal(err)
				}
				return resp, body.Input
			}
			follow, first := complete(req)
			if len(first) != 4 || string(first[1]) != native || string(first[2]) != call {
				t.Fatalf("first replay input = %s", first)
			}
			nextReq := req
			nextReq.Messages = append(slices.Clone(req.Messages), follow.Message, llm.User("next"))
			_, next := complete(nextReq)
			if len(next) != len(first)+3 || string(next[len(first)]) != secondNative ||
				!slices.EqualFunc(next[:len(first)], first, func(a, b json.RawMessage) bool { return string(a) == string(b) }) {
				t.Fatalf("next replay input = %s, want prefix %s", next, first)
			}
			withReasoning := llm.EstimateInputTokensForResolved(res, req)
			ordinaryReq := req
			ordinaryReq.Messages = slices.Clone(req.Messages)
			ordinaryReq.Messages[len(ordinaryReq.Messages)-2].Content = resp.Message.Content[1:]
			if ordinary := llm.EstimateInputTokensForResolved(res, ordinaryReq); ordinary.Tokens >= withReasoning.Tokens {
				t.Fatalf("estimates with/without native reasoning = %d/%d", withReasoning.Tokens, ordinary.Tokens)
			}
			toolReq := userReq("hello")
			toolReq.Messages = append(toolReq.Messages, llm.Message{Role: llm.RoleTool, Content: resp.Message.Content[:1]})
			toolEstimate := llm.EstimateInputTokensForResolved(res, toolReq)
			_, toolInput := complete(toolReq)
			toolReq.Messages[len(toolReq.Messages)-1].Content = nil
			if ordinary := llm.EstimateInputTokensForResolved(res, toolReq); len(toolInput) != 1 || toolEstimate.Tokens != ordinary.Tokens {
				t.Fatalf("tool input = %s, estimates with/without native reasoning = %d/%d", toolInput, toolEstimate.Tokens, ordinary.Tokens)
			}
		})
	}
}
