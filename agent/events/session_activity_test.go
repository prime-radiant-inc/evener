package events

import (
	"primeradiant.com/evener/appwire"
	"testing"
)

func TestSessionActivityEventSeparatesPhysicalAndLogicalSession(t *testing.T) {
	t.Parallel()
	payload := SessionActivityChangedData{ThreadID: "parent", SessionID: "parent", Ref: "local:parent", Resources: []appwire.SessionActivityResource{appwire.SessionActivityResourceSummary, appwire.SessionActivityResourceWatches}}
	event := New(payload)
	event.SessionID = "child"
	if event.Kind != EventSessionActivityChanged {
		t.Fatalf("kind=%s", event.Kind)
	}
	got, ok := event.Data.(SessionActivityChangedData)
	if !ok || event.SessionID == got.SessionID || got.Resources[1] != appwire.SessionActivityResourceWatches {
		t.Fatalf("event=%+v", event)
	}
}
