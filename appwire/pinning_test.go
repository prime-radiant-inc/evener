package appwire

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestPinningMethodsAreHubScopedWithTypedContracts(t *testing.T) {
	want := map[string]struct {
		params any
		result any
	}{
		MethodEvenerPinSectionRename: {PinSectionRenameParams{}, PinSectionRenameResponse{}},
		MethodEvenerPinSectionDelete: {PinSectionDeleteParams{}, PinSectionDeleteResponse{}},
		MethodEvenerSessionPinAssign: {SessionPinAssignParams{}, SessionPinMutationResponse{}},
		MethodEvenerSessionPinUnpin:  {SessionPinUnpinParams{}, SessionPinMutationResponse{}},
	}
	for _, method := range Methods {
		contract, ok := want[method.Name]
		if !ok {
			continue
		}
		if method.Scope != ScopeHub {
			t.Errorf("method %q scope = %q, want hub", method.Name, method.Scope)
		}
		if reflect.TypeOf(method.Params) != reflect.TypeOf(contract.params) {
			t.Errorf("method %q params = %T, want %T", method.Name, method.Params, contract.params)
		}
		if reflect.TypeOf(method.Result) != reflect.TypeOf(contract.result) {
			t.Errorf("method %q result = %T, want %T", method.Name, method.Result, contract.result)
		}
		delete(want, method.Name)
	}
	for method := range want {
		t.Errorf("method %q missing from catalog", method)
	}
}

func TestPinningWireShapes(t *testing.T) {
	navigation := NavigationMutation{GenerationID: "generation-a", Targets: []NavigationInvalidationTarget{}}
	tests := []struct {
		name  string
		value any
		want  string
	}{
		{"rename params", PinSectionRenameParams{SectionID: "section-a", Name: "Research"}, `{"section_id":"section-a","name":"Research"}`},
		{"delete params", PinSectionDeleteParams{SectionID: "section-a"}, `{"section_id":"section-a"}`},
		{"assign by id", SessionPinAssignParams{SessionRef: "local:session-a", SectionID: stringPointer("section-a")}, `{"session_ref":"local:session-a","section_id":"section-a"}`},
		{"assign by name", SessionPinAssignParams{SessionRef: "local:session-a", SectionName: stringPointer("Research")}, `{"session_ref":"local:session-a","section_name":"Research"}`},
		{"unpin params", SessionPinUnpinParams{SessionRef: "local:session-a"}, `{"session_ref":"local:session-a"}`},
		{"rename response", PinSectionRenameResponse{OK: true, Changed: true, Section: PinSection{ID: "section-a", Name: "Research", MemberCount: 2}, Navigation: navigation}, `{"ok":true,"changed":true,"section":{"id":"section-a","name":"Research","member_count":2},"navigation":{"generation_id":"generation-a","targets":[]}}`},
		{"delete response", PinSectionDeleteResponse{OK: true, Changed: true, MemberCount: 2, Navigation: navigation}, `{"ok":true,"changed":true,"member_count":2,"navigation":{"generation_id":"generation-a","targets":[]}}`},
		{"assign response", SessionPinMutationResponse{OK: true, Changed: true, Assignment: SessionPinAssignment{SessionRef: "local:session-a", Section: &PinSection{ID: "section-a", Name: "Research", MemberCount: 1}}, Navigation: navigation}, `{"ok":true,"changed":true,"assignment":{"session_ref":"local:session-a","section":{"id":"section-a","name":"Research","member_count":1}},"navigation":{"generation_id":"generation-a","targets":[]}}`},
		{"unpin response", SessionPinMutationResponse{OK: true, Assignment: SessionPinAssignment{SessionRef: "local:session-a"}, Navigation: navigation}, `{"ok":true,"changed":false,"assignment":{"session_ref":"local:session-a"},"navigation":{"generation_id":"generation-a","targets":[]}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := json.Marshal(test.value)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != test.want {
				t.Fatalf("got %s, want %s", got, test.want)
			}
		})
	}
}

func stringPointer(value string) *string {
	return &value
}
