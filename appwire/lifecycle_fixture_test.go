package appwire

import (
	"encoding/json"
	"os"
	"testing"
)

// lifecycleClassCase is one row of the cross-language contract both the Go
// registry test and appwire-client's lifecycle test read. It pins the class,
// terminality and resting polarity a state must keep; a change to the declared
// machine without the fixture failing one side is the drift this catches.
type lifecycleClassCase struct {
	Machine  string `json:"machine"`
	State    string `json:"state"`
	Class    string `json:"class"`
	Terminal bool   `json:"terminal"`
	Resting  bool   `json:"resting"`
}

func TestLifecycleFixtureMatchesRegistry(t *testing.T) {
	raw, err := os.ReadFile("../testdata/lifecycle/state_classes.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []lifecycleClassCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("lifecycle fixture is empty")
	}
	seen := make(map[string]map[string]bool)
	for _, tc := range cases {
		machine, ok := LifecycleMachineFor(MachineKind(tc.Machine))
		if !ok {
			t.Errorf("fixture names unknown machine %q", tc.Machine)
			continue
		}
		state, ok := machine.State(tc.State)
		if !ok {
			t.Errorf("fixture names unknown state %q of %q", tc.State, tc.Machine)
			continue
		}
		if seen[tc.Machine] == nil {
			seen[tc.Machine] = make(map[string]bool)
		}
		if seen[tc.Machine][tc.State] {
			t.Errorf("fixture lists %s/%s twice", tc.Machine, tc.State)
		}
		seen[tc.Machine][tc.State] = true
		if got := string(state.Class); got != tc.Class {
			t.Errorf("%s/%s class = %q, want %q", tc.Machine, tc.State, got, tc.Class)
		}
		if state.Terminal != tc.Terminal {
			t.Errorf("%s/%s terminal = %v, want %v", tc.Machine, tc.State, state.Terminal, tc.Terminal)
		}
		if state.Resting != tc.Resting {
			t.Errorf("%s/%s resting = %v, want %v", tc.Machine, tc.State, state.Resting, tc.Resting)
		}
	}
	for _, machine := range LifecycleMachines() {
		for _, state := range machine.States {
			if !seen[string(machine.Kind)][state.Name] {
				t.Errorf("fixture omits declared state %s/%s", machine.Kind, state.Name)
			}
		}
	}
}
