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
		MethodEvenerSessionPinAssign: {SessionPinAssignParams{}, SessionPinAssignResponse{}},
		MethodEvenerSessionPinUnpin:  {SessionPinUnpinParams{}, SessionPinUnpinResponse{}},
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
		{"rename params", PinSectionRenameParams{SectionID: "section-a", Name: "Research"}, `{"sectionId":"section-a","name":"Research"}`},
		{"delete params", PinSectionDeleteParams{SectionID: "section-a"}, `{"sectionId":"section-a"}`},
		{"assign by id", SessionPinAssignParams{SessionRef: "local:session-a", SectionID: new("section-a")}, `{"sessionRef":"local:session-a","sectionId":"section-a"}`},
		{"assign by name", SessionPinAssignParams{SessionRef: "local:session-a", SectionName: new("Research")}, `{"sessionRef":"local:session-a","sectionName":"Research"}`},
		{"unpin params", SessionPinUnpinParams{SessionRef: "local:session-a"}, `{"sessionRef":"local:session-a"}`},
		{"rename response", PinSectionRenameResponse{OK: true, Changed: true, Section: PinSection{ID: "section-a", Name: "Research", MemberCount: 2}, Navigation: navigation}, `{"ok":true,"changed":true,"section":{"id":"section-a","name":"Research","memberCount":2},"navigation":{"generation_id":"generation-a","targets":[]}}`},
		{"delete response", PinSectionDeleteResponse{OK: true, Changed: true, MemberCount: 2, Navigation: navigation}, `{"ok":true,"changed":true,"memberCount":2,"navigation":{"generation_id":"generation-a","targets":[]}}`},
		{"assign response", SessionPinAssignResponse{OK: true, Changed: true, Assignment: SessionPinAssignment{SessionRef: "local:session-a", Section: PinSection{ID: "section-a", Name: "Research", MemberCount: 1}}, Navigation: navigation}, `{"ok":true,"changed":true,"assignment":{"sessionRef":"local:session-a","section":{"id":"section-a","name":"Research","memberCount":1}},"navigation":{"generation_id":"generation-a","targets":[]}}`},
		{"unpin response", SessionPinUnpinResponse{OK: true, Assignment: SessionPinUnpinAssignment{SessionRef: "local:session-a"}, Navigation: navigation}, `{"ok":true,"changed":false,"assignment":{"sessionRef":"local:session-a"},"navigation":{"generation_id":"generation-a","targets":[]}}`},
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
