package doctor

import (
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestLifecycleReturnsValidatedMachines(t *testing.T) {
	machines := Lifecycle()
	if len(machines) != len(appwire.LifecycleMachines()) {
		t.Fatalf("Lifecycle() returned %d machines, registry has %d", len(machines), len(appwire.LifecycleMachines()))
	}
	for _, machine := range machines {
		if err := machine.Validate(); err != nil {
			t.Errorf("%q.Validate(): %v", machine.Kind, err)
		}
	}
}

func TestRenderLifecycleNamesEveryMachineAndState(t *testing.T) {
	machines := Lifecycle()
	text := RenderLifecycle(machines)
	for _, machine := range machines {
		if !strings.Contains(text, string(machine.Kind)) {
			t.Errorf("render omits machine %q", machine.Kind)
		}
		for _, state := range machine.States {
			if !strings.Contains(text, state.Name) {
				t.Errorf("render omits state %q of %q", state.Name, machine.Kind)
			}
		}
		if len(machine.Transitions) > 0 && !strings.Contains(text, "transitions:") {
			t.Errorf("render omits transitions for %q", machine.Kind)
		}
	}
}
