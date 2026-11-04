package appwire

import (
	"reflect"
	"testing"
)

func TestConversationProvenance_WirePresence(t *testing.T) {
	for _, tc := range []struct {
		typ   reflect.Type
		field string
	}{
		{reflect.TypeFor[EvenerThread](), "ConversationProvenance"},
		{reflect.TypeFor[ThreadItem](), "ConversationProvenance"},
		{reflect.TypeFor[FeatureSet](), "ConversationProvenance"},
		{reflect.TypeFor[FeatureSet](), "Voice"},
	} {
		if _, ok := tc.typ.FieldByName(tc.field); !ok {
			t.Errorf("%v has no %s wire field", tc.typ, tc.field)
		}
	}
}

func TestConversationProvenance_ExactEligibility(t *testing.T) {
	state := &ConversationProvenanceState{Version: 1, Authoritative: true, Revision: 7, InstanceID: "runtime-A", AdmittedInputs: []string{"queued-first", "steered-second"}, Frontiers: []ConversationFrontier{
		{ID: "empty"}, {ID: "steer", BaseID: "empty", AddedInputs: []string{"steered-second"}}, {ID: "both", BaseID: "steer", AddedInputs: []string{"queued-first"}},
	}}
	output := &ConversationOutputProvenance{Version: 1, InstanceID: "runtime-A", RunGeneration: 1, RequestID: "request-1", FrontierID: "steer", SourceItemID: "item-1"}
	if ConversationOutputEligible(state, output) {
		t.Fatal("out-of-admission-order consumed steer dismissed queued input")
	}
	state.RetiredInputs = []string{"queued-first"}
	if !ConversationOutputEligible(state, output) {
		t.Fatal("explicit queue retirement leaves impossible fence")
	}
	state.RetiredInputs = nil
	output.FrontierID = "both"
	if !ConversationOutputEligible(state, output) {
		t.Fatal("exactly covered progress rejected without newer input")
	}
	state.AdmittedInputs = append(state.AdmittedInputs, "new-input")
	if ConversationOutputEligible(state, output) {
		t.Fatal("new admission did not retire old output")
	}
	for _, id := range []string{"", "missing"} {
		if _, ok := ResolveConversationFrontier(state, id); ok {
			t.Errorf("invalid frontier %q accepted", id)
		}
	}
	state.AdmittedInputs = nil
	output.FrontierID = "empty"
	if !ConversationOutputEligible(state, output) {
		t.Fatal("known empty confused with unknown")
	}
	state.Authoritative = false
	if ConversationOutputEligible(state, output) || ConversationOutputEligible(nil, output) {
		t.Fatal("unknown state synthesized fresh")
	}
}

func TestConversationProvenance_InvalidGraphAndSource(t *testing.T) {
	for _, nodes := range [][]ConversationFrontier{
		{{ID: "x", BaseID: "missing"}},
		{{ID: "x", BaseID: "x"}},
		{{ID: "x", BaseID: "later"}, {ID: "later"}},
		{{ID: "x"}, {ID: "x"}},
		{{ID: "x", RemovedInputs: []string{"absent"}}},
		{{ID: "x", AddedInputs: []string{"input", "input"}}},
	} {
		state := &ConversationProvenanceState{Version: 1, Authoritative: true, InstanceID: "runtime-A", Frontiers: nodes}
		if _, ok := ResolveConversationFrontier(state, "x"); ok {
			t.Errorf("invalid graph accepted: %+v", nodes)
		}
	}
	state := &ConversationProvenanceState{Version: 1, Authoritative: true, InstanceID: "runtime-A", Frontiers: []ConversationFrontier{{ID: "x"}}}
	for _, output := range []*ConversationOutputProvenance{nil, {}, {Version: 1, InstanceID: "runtime-B", RunGeneration: 1, RequestID: "req", FrontierID: "x", SourceItemID: "item"}, {Version: 1, InstanceID: "runtime-A", RunGeneration: 1, RequestID: "req", FrontierID: "x"}} {
		if ConversationOutputEligible(state, output) {
			t.Fatalf("invalid output accepted: %+v", output)
		}
	}
}

func TestConversationProvenance_CloneOwnsMetadata(t *testing.T) {
	original := Thread{Evener: EvenerThread{ConversationProvenance: &ConversationProvenanceState{AdmittedInputs: []string{"a"}, RetiredInputs: []string{"r"}, Frontiers: []ConversationFrontier{{ID: "f", AddedInputs: []string{"a"}, RemovedInputs: []string{"r"}}}}}, Turns: []Turn{{Items: []ThreadItem{{ConversationProvenance: &ConversationOutputProvenance{RequestID: "request"}}}}}}
	copy := CloneThread(original)
	copy.Evener.ConversationProvenance.AdmittedInputs[0] = "changed"
	copy.Evener.ConversationProvenance.RetiredInputs[0] = "changed"
	copy.Evener.ConversationProvenance.Frontiers[0].AddedInputs[0] = "changed"
	copy.Evener.ConversationProvenance.Frontiers[0].RemovedInputs[0] = "changed"
	copy.Turns[0].Items[0].ConversationProvenance.RequestID = "changed"
	if original.Evener.ConversationProvenance.AdmittedInputs[0] != "a" || original.Evener.ConversationProvenance.RetiredInputs[0] != "r" || original.Evener.ConversationProvenance.Frontiers[0].AddedInputs[0] != "a" || original.Evener.ConversationProvenance.Frontiers[0].RemovedInputs[0] != "r" || original.Turns[0].Items[0].ConversationProvenance.RequestID != "request" {
		t.Fatal("clone shares mutable provenance")
	}
}
