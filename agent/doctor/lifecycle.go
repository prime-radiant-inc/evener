package doctor

import (
	"fmt"
	"strings"

	"primeradiant.com/evener/appwire"
)

// Lifecycle returns the product's declared lifecycle machines in registry
// order. It reads no session state: the machines are compiled declarations, so
// the answer is the same on any host and for any selector.
func Lifecycle() []appwire.LifecycleMachine { return appwire.LifecycleMachines() }

// RenderLifecycle writes the registry as a compact table for a person reading
// the CLI. Each machine names its initial state, every state with its class and
// terminality, and the declared edges. An agent should use --json and read the
// structs.
func RenderLifecycle(machines []appwire.LifecycleMachine) string {
	var b strings.Builder
	for i, machine := range machines {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%s", machine.Kind)
		if machine.Initial != "" {
			fmt.Fprintf(&b, " (initial %s)", machine.Initial)
		}
		b.WriteString("\n")
		for _, state := range machine.States {
			markers := []string{string(state.Class)}
			if state.Terminal {
				markers = append(markers, "terminal")
			}
			if state.Resting {
				markers = append(markers, "resting")
			}
			fmt.Fprintf(&b, "  %-24s %-28s %s\n", state.Name, strings.Join(markers, ", "), state.Summary)
		}
		var edges []string
		for _, state := range machine.States {
			for _, to := range machine.Next(state.Name) {
				edges = append(edges, state.Name+" -> "+to)
			}
		}
		if len(edges) > 0 {
			fmt.Fprintf(&b, "  transitions: %s\n", strings.Join(edges, ", "))
		}
	}
	return b.String()
}
