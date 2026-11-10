package contextmgr

import (
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func TestPendingAskTailCutoff(t *testing.T) {
	history := []schema.Turn{{Kind: schema.TurnUserInput, TurnID: "old"}, {Kind: schema.TurnUserInput, TurnID: "a"}, {Kind: schema.TurnAssistant, Message: llm.Message{Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call-a", Name: "ask_user"}}}}}, {Kind: schema.TurnToolResults, TurnID: "a"}, {Kind: schema.TurnUserInput, TurnID: "b"}, {Kind: schema.TurnAssistant, TurnID: "b"}}
	for _, tc := range []struct {
		name           string
		ids, callIDs   []string
		ordinary, want int
	}{
		{name: "no pending", ordinary: 0, want: 0},
		{name: "multiple unordered", ids: []string{"b", "a", "a"}, ordinary: 0, want: 5},
		{name: "live unstamped call", ids: []string{"not-stamped"}, callIDs: []string{"call-a"}, ordinary: 0, want: 4},
		{name: "ordinary keeps more", ids: []string{"b"}, ordinary: 4, want: 4},
		{name: "missing source cannot invent tail", ids: []string{"missing"}, ordinary: 2, want: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := preservePendingAskTail(history, tc.ordinary, &CompactionMeta{PendingAskTurnIDs: tc.ids, PendingAskCallIDs: tc.callIDs}); got != tc.want {
				t.Fatalf("preserve=%d want=%d", got, tc.want)
			}
		})
	}
}
