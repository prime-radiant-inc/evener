package dev

import (
	"os"
	"strings"
	"syscall"
	"testing"
)

// renamedBrowserGate gives a guard a new label and gives an independent Node
// guard the cascade label. Their launch contracts, not those labels, must
// determine build fencing and interrupt handling.
func renamedBrowserGate(guard string, buildFrontend bool) *webGate {
	gate := newBrowserGate(2, buildFrontend)
	gate.checks = []string{"renamedguard", cascadeGuard}
	gate.spec = func(check, root string) guardSpec {
		contract := "layoutguard"
		if check == "renamedguard" {
			contract = guard
		}
		spec := browserGuardSpec(contract, root)
		spec.name = check
		return spec
	}
	return gate
}

func TestBrowserGateBuildFailureUsesLaunchContract(t *testing.T) {
	for _, guard := range []string{"skillguard", "cascadeguard", "backgroundjobsguard"} {
		t.Run(guard, func(t *testing.T) {
			launcher := newFakeLauncher()
			launcher.buildStatus = 17
			tg := startGate(t, renamedBrowserGate(guard, true), launcher, make(chan os.Signal, 4))
			stop, drained := make(chan struct{}), make(chan struct{})
			go func() {
				defer close(drained)
				for {
					select {
					case name := <-launcher.started:
						launcher.guard(name).exit <- 0
					case <-stop:
						return
					}
				}
			}()
			defer func() { close(stop); <-drained }()

			if result := tg.await(t); result.status != 17 || !result.keep {
				t.Fatalf("gate result = %+v, want failed build and retained evidence", result)
			}
			if launcher.guard("renamedguard") != nil {
				t.Error("the production guard started after its frontend build failed")
			}
			if launcher.guard(cascadeGuard) == nil {
				t.Error("the independent Node guard was fenced by its label")
			}
			if !strings.Contains(tg.stderr.String(), "FAIL  web-renamedguard (frontend build, exit 17)") {
				t.Errorf("the production guard lacks its build-failure verdict:\n%s", tg.stderr.String())
			}
			if strings.Contains(tg.stderr.String(), "FAIL  web-cascadeguard") {
				t.Errorf("the independent guard inherited the production label's failure:\n%s", tg.stderr.String())
			}
		})
	}
}

func TestBrowserGateInterruptUsesLaunchContract(t *testing.T) {
	for _, guard := range []string{"retirementguard", "skillguard", "cascadeguard", "backgroundjobsguard"} {
		t.Run(guard, func(t *testing.T) {
			launcher := newFakeLauncher()
			tg := startGate(t, renamedBrowserGate(guard, false), launcher, make(chan os.Signal))
			launcher.awaitStart(t)
			launcher.awaitStart(t)
			waited := launcher.guard("renamedguard")
			node := launcher.guard(cascadeGuard)
			tg.signals <- syscall.SIGTERM
			waited.exit <- 0
			node.exit <- 143
			if result := tg.await(t); result.status != 143 || !result.keep {
				t.Fatalf("gate result = %+v, want interrupt status and retained evidence", result)
			}
			select {
			case <-waited.terminated:
				t.Error("the gate signalled a guard that owns its cleanup")
			default:
			}
			select {
			case <-node.terminated:
			default:
				t.Error("the gate did not terminate the independent Node guard")
			}
		})
	}
}
