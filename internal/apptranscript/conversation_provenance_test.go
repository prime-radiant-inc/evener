package apptranscript

import (
	"testing"

	"primeradiant.com/evener/agent/schema"
)

func TestConversationProvenance_ProjectsOnlyDurableOrigin(t *testing.T) {
	turn := schema.Turn{Format: schema.TurnFormatIdentity, Kind: schema.TurnCommunicate, Communicate: &schema.CommunicateInfo{CallID: "call", Message: "result"}, ConversationProvenance: &schema.ConversationOutputProvenance{Version: 1, InstanceID: "instance", RunGeneration: 9, RequestID: "request", FrontierID: "frontier"}}
	items, _ := ProjectEntryParts("turn", 17, turn, nil, nil, nil)
	if len(items) != 1 {
		t.Fatalf("items=%+v", items)
	}
	p := items[0].ConversationProvenance
	if p == nil || p.SourceItemID != items[0].ID || p.RequestID != "request" || p.FrontierID != "frontier" || p.RunGeneration != 9 {
		t.Fatalf("committed origin missing or resampled: %+v", p)
	}
	turn.ConversationProvenance = nil
	items, _ = ProjectEntryParts("turn", 18, turn, nil, nil, nil)
	if items[0].ConversationProvenance != nil {
		t.Fatal("legacy output synthesized fresh")
	}
	ordinal := uint64(17)
	turn.OriginalOrdinal = &ordinal
	items, _ = ProjectEntryParts("turn", 19, turn, nil, nil, nil)
	if len(items) != 0 {
		t.Fatal("fold copy duplicated output")
	}
}
