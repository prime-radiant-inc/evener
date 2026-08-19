package appsource

import (
	"encoding/json"
	"testing"
)

// FuzzMapCodexTurn drives the real mapCodexTurn seam exactly as the codex source
// does: fuzzed bytes are first decoded into a codexTurn (so each item carried in
// turn.Items is, by construction, a valid-JSON element just as it is on the live
// wire), then mapped into an appwire.Turn. This exercises mapCodexTurnStatus, the
// error mapping, and mapCodexItem's full type switch (userMessage / agentMessage
// / commandExecution / mcpToolCall / dynamicToolCall / reasoning / default). The
// oracle is floor "no panic" plus re-serializability: the mapped turn goes
// straight on the wire, so it must marshal cleanly.
func FuzzMapCodexTurn(f *testing.F) {
	f.Add([]byte(`{"id":"t1","status":"inProgress","items":[{"type":"userMessage","id":"i1","content":"hi"}]}`))
	f.Add([]byte(`{"id":"t2","items":[{"type":"agentMessage","id":"i2","text":"yo"}]}`))
	f.Add([]byte(`{"id":"t3","items":[{"type":"commandExecution","id":"i3","command":"ls","cwd":"/","aggregatedOutput":"x","status":"failed"}]}`))
	f.Add([]byte(`{"id":"t4","items":[{"type":"mcpToolCall","id":"i4","tool":"t","arguments":{"a":1},"result":"r","status":"ok"}]}`))
	f.Add([]byte(`{"id":"t5","items":[{"type":"dynamicToolCall","id":"i5","tool":"d","arguments":[1],"contentItems":[{"x":1}]},{"type":"reasoning","id":"i6","text":"because"}]}`))
	f.Add([]byte(`{"id":"t6","status":"interrupted","error":{"message":"boom","additionalDetails":"more","codexErrorInfo":{"code":1}}}`))
	f.Add([]byte(`{"id":"t7","items":[123,"str",{"type":"x"}]}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`not json`))

	f.Fuzz(func(t *testing.T, raw []byte) {
		var turn codexTurn
		if err := json.Unmarshal(raw, &turn); err != nil {
			return // rejected input
		}
		mapped := mapCodexTurn(turn)
		if _, err := json.Marshal(mapped); err != nil {
			t.Fatalf("mapped turn failed to marshal: %v\n raw=%q\n turn=%#v", err, raw, mapped)
		}
	})
}
