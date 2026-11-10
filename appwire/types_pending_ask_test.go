package appwire

import (
	"encoding/json"
	"testing"
)

func TestPendingAskClone(t *testing.T) {
	if ClonePendingAsk(nil) != nil {
		t.Fatal("unknown state became authoritative")
	}
	empty := ClonePendingAsk(&PendingAskState{Version: 1, InstanceID: "instance", Revision: 3})
	encoded, err := json.Marshal(empty)
	if err != nil || string(encoded) != `{"version":1,"instanceId":"instance","revision":3,"calls":[]}` {
		t.Fatalf("authoritative absence was lost: %s, %v", encoded, err)
	}
	original := &PendingAskState{Version: 1, InstanceID: "instance", Revision: 4, Calls: []PendingAskCall{{TurnID: "turn", CallID: "call"}}}
	cloned := ClonePendingAsk(original)
	cloned.Calls[0].CallID = "changed"
	cloned.Revision++
	if original.Calls[0].CallID != "call" || original.Revision != 4 {
		t.Fatal("clone aliases pending state")
	}
}
