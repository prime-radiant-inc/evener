package apptranscript

import (
	"encoding/json"
	"testing"

	"primeradiant.com/serf/agent/schema"
)

// FuzzProjectTurn drives the real apptranscript.ProjectTurn seam: it decodes
// fuzzed bytes into a schema.Turn (the typed transcript turn the reload path
// hands ProjectTurn) and projects it into AppWire thread items. ProjectTurn
// dispatches on turn.Kind (checkpoint/summary/userInput/steering/assistant/
// tool/toolResults) and walks the message content parts (text, thinking,
// web_search, tool calls/results, images, audio, documents), so a structurally
// varied turn exercises every projection branch. The oracle is floor "no panic"
// plus re-serializability: projected items go on the wire, so each must marshal
// cleanly.
func FuzzProjectTurn(f *testing.F) {
	f.Add([]byte(`{"kind":"USER_INPUT","message":{"role":"user","content":[{"kind":"text","text":"hello"}]}}`))
	f.Add([]byte(`{"kind":"ASSISTANT","message":{"role":"assistant","content":[{"kind":"text","text":"hi"},{"kind":"thinking","thinking":{"text":"hmm"}}]}}`))
	f.Add([]byte(`{"kind":"ASSISTANT","message":{"role":"assistant","content":[{"kind":"tool_call","tool_call":{"id":"c1","name":"shell","arguments":{"command":"ls"}}}]}}`))
	f.Add([]byte(`{"kind":"ASSISTANT","message":{"role":"assistant","content":[{"kind":"tool_call","tool_call":{"id":"c2","name":"communicate","arguments":{"message":"done"}}}]}}`))
	f.Add([]byte(`{"kind":"TOOL_RESULTS","message":{"role":"tool","content":[{"kind":"tool_result","tool_result":{"tool_call_id":"c1","name":"shell","content":"ok"}}]}}`))
	f.Add([]byte(`{"kind":"SUMMARY","message":{"role":"system","content":[{"kind":"text","text":"summary"}]}}`))
	f.Add([]byte(`{"kind":"STEERING","message":{"role":"user","content":[{"kind":"text","text":"steer"}]}}`))
	f.Add([]byte(`{"kind":"ASSISTANT","message":{"role":"assistant","content":[{"kind":"web_search","web_search":{"query":"q","raw":{"action":{"query":"q"},"content":[{"type":"web_search_result","url":"http://x","title":"T"}]}}}]}}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`not json`))

	f.Fuzz(func(t *testing.T, raw []byte) {
		var turn schema.Turn
		if err := json.Unmarshal(raw, &turn); err != nil {
			return // rejected input
		}
		toolNames := map[string]string{}
		items := ProjectTurn("turn_1", 1, turn, toolNames, nil, nil)
		for _, item := range items {
			if _, err := json.Marshal(item); err != nil {
				t.Fatalf("projected item failed to marshal: %v\n raw=%q\n item=%#v", err, raw, item)
			}
		}
	})
}
