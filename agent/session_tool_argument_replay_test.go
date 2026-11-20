package agent

// A Chat Completions tool call may execute a repaired form of its arguments
// while the model's original argument bytes must still be what the next
// request replays. Here the model sends valid, non-canonical JSON whose offset
// is a string, so the schema rejects it and prepareToolCall coerces the offset
// for local execution. The replay must not adopt that execution
// representation.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/tool/repair"
	"primeradiant.com/evener/llm/registry"
)

// argumentReplayDial is pxDial without the discarded event stream: a session
// event channel has exactly one consumer, so this test hands it to the
// repaired-call collector instead.
func argumentReplayDial(t *testing.T, instance, ref string, instances map[string]registry.Provider, opts ...sessionOpt) (*Session, <-chan []events.ToolCallRepairedData) {
	t.Helper()
	stateDir := t.TempDir()
	client := registryClientAt(t, stateDir, instances, []string{instance})
	profile := withTestSessionNamer(client, resolveClientProfile(t, client, ref))
	sess := newSession(t, append([]sessionOpt{withClient(client), withProfile(profile), withStateDir(stateDir)}, opts...)...)
	return sess, drainRepairedEvents(sess)
}

func TestChatWarmToolLoopReplaysOriginalArgumentsAfterRepair(t *testing.T) {
	t.Parallel()

	const firstLine = "replay guard first line"
	const secondLine = "replay guard second line"
	work := t.TempDir()
	file := filepath.Join(work, "argument_replay.txt")
	pxMust(t, os.WriteFile(file, []byte(firstLine+"\n"+secondLine+"\n"), 0o644))

	// The model's own argument bytes: valid JSON, spaced so canonical encoding
	// differs, with the path JSON-escaped and offset sent as a string.
	quotedPath, err := json.Marshal(file)
	pxMust(t, err)
	originalArgs := fmt.Sprintf(`{ "file_path" : %s, "offset" : "0" }`, quotedPath)

	readCall, err := json.Marshal(map[string]any{
		"role": "assistant",
		"tool_calls": []any{map[string]any{
			"index": 0,
			"id":    "call_1",
			"type":  "function",
			"function": map[string]any{
				"name":      "read_file",
				"arguments": originalArgs,
			},
		}},
	})
	pxMust(t, err)

	w := pxServe(t, func(n int, rw http.ResponseWriter) {
		if n == 1 {
			pxSSE(rw, pxChatFrame, "resp_1", readCall)
			return
		}
		pxSSE(rw, pxChatFrame, fmt.Sprintf("resp_%d", n), pxDone(n, true))
	})
	instances := map[string]registry.Provider{"tooldial": {
		Base: "openai-compatible", APIKey: "test", DefaultModel: "dial-model", InheritModels: new(false),
		Transport: registry.Transport{BaseURL: w.url},
		Models:    map[string]registry.Model{"dial-model": {}},
	}}
	sess, repairedCh := argumentReplayDial(t, "tooldial", "tooldial/dial-model", instances, withDir(work))

	pxTurn(t, sess, "read the guard file")

	bodies := w.reqs(t, 2) // a retry must not be able to mask a rejected call
	repaired := closeAndDrainRepairedEvents(sess, repairedCh)
	replay := pxDecode(t, bodies[1])

	// One pass over the warm request recovers the executed result and the
	// replayed call.
	var toolOutput, replayedArgs string
	var sawToolResult, sawReplayedCall bool
	for _, raw := range replay.Messages {
		var msg struct {
			Role       string `json:"role"`
			ToolCallID string `json:"tool_call_id"`
			Content    string `json:"content"`
			ToolCalls  []struct {
				ID       string `json:"id"`
				Function struct {
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		if msg.Role == "tool" && msg.ToolCallID == "call_1" {
			toolOutput, sawToolResult = msg.Content, true
		}
		for _, call := range msg.ToolCalls {
			if call.ID == "call_1" {
				replayedArgs, sawReplayedCall = call.Function.Arguments, true
			}
		}
	}

	// (a) The repaired call really executed: the tool result carries the file.
	if !sawToolResult {
		t.Fatalf("request 2 has no tool result for call_1:\n%s", bodies[1])
	}
	if !strings.Contains(toolOutput, firstLine) || !strings.Contains(toolOutput, secondLine) {
		t.Fatalf("tool result for call_1 = %q, want the file contents", toolOutput)
	}

	// (b) The invalid input is diagnosed: a coerce_type repair on offset.
	wantChange := string(repair.ChangeCoerceType) + ":offset:"
	var sawRepair bool
	for _, event := range repaired {
		if event.ToolName != "read_file" || event.CallID != "call_1" {
			continue
		}
		for _, change := range event.Changes {
			if strings.HasPrefix(change, wantChange) {
				sawRepair = true
			}
		}
	}
	if !sawRepair {
		t.Fatalf("no %q repair for call_1, got %+v", wantChange, repaired)
	}

	// (c) The exact model bytes replay on the assistant tool call.
	if !sawReplayedCall {
		t.Fatalf("request 2 has no assistant tool call with id call_1:\n%s", bodies[1])
	}
	if replayedArgs != originalArgs {
		t.Fatalf("replayed arguments = %q, want the model's original bytes %q", replayedArgs, originalArgs)
	}
}
