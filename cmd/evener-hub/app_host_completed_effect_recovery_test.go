package hub

import (
	"fmt"
	"testing"
	"testing/synctest"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
)

// A command can succeed before its verification is interrupted. A later broken
// disk contract must block new execution, not read-only proof of that effect.
func TestHostRestartSettlesCompletedEffectWithUnusableContract(t *testing.T) {
	sha, dirty, channel := buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel
	t.Cleanup(func() { buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel = sha, dirty, channel })
	for _, scenario := range []struct {
		name, control         string
		startup, incompatible bool
	}{
		{name: "restart-missing-flag"},
		{name: "restart-incompatible-protocol", incompatible: true},
		{name: "startup-missing-flag", startup: true},
		{name: "startup-incompatible-protocol", startup: true, incompatible: true},
		{name: "same-predecessor", control: "predecessor"},
		{name: "wrong-build", control: "build"},
		{name: "wrong-pin", control: "pin"},
		{name: "unknown-identity", control: "identity"},
		{name: "stopped", control: "stopped"},
		{name: "startup-final-identity-retained", startup: true, control: "final-identity"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel = restartBuildB, "", "snapshot"
				runner := &restartRecoveryRunner{
					diskVersion: restartBuildB, runningVersion: restartBuildA, startedAt: restartStartA,
					delayBridgeExit: true, interruptRestartVerification: true,
				}
				if scenario.startup {
					runner.diskVersion, runner.runningVersion = restartBuildA, restartBuildB
					runner.acknowledgeFirstWrite, runner.noSupervisor = true, true
					runner.firstWriteContract = []byte(`{`)
				}
				m, manager, store, host, events := publicRecoveryHub(t, runner, restartBuildB)
				ch, err := manager.Ensure(t.Context(), host.Name)
				if err != nil {
					t.Fatal(err)
				}
				writes, restarts, starts := 0, 1, 0
				if scenario.startup {
					plan, err := m.Plan(t.Context(), appwire.HostPlanParams{Name: host.Name})
					if err != nil {
						t.Fatal(err)
					}
					deploy, err := m.Deploy(t.Context(), appwire.HostDeployParams{Name: host.Name, Token: plan.Token, OperationID: "prepare-owned-start"})
					if err != nil {
						t.Fatal(err)
					}
					m.cfg.opsWG.Wait()
					assertRestartRecoveryRecord(t, store, deploy.ID, hostops.StateFailed)
					runner.mu.Lock()
					runner.runningVersion, runner.startedAt = "", ""
					runner.mu.Unlock()
					if err := ch.Close(); err != nil {
						t.Fatal(err)
					}
					waitPublicRecoveryEvent(t, events, sshconn.EventFailed)
					runner.mu.Lock()
					runner.launchResponse = nil
					runner.startupIdentityFault = "identity-failed"
					runner.mu.Unlock()
					writes, restarts, starts = 1, 0, 1
				}
				restart := func(id string, want hostops.State) string {
					t.Helper()
					op, err := m.Restart(t.Context(), appwire.HostRestartParams{Name: host.Name, Generation: host.Generation, IncarnationID: host.IncarnationID, OperationID: id})
					if err != nil {
						t.Fatal(err)
					}
					m.cfg.opsWG.Wait()
					assertRestartRecoveryRecord(t, store, op.ID, want)
					return op.ID
				}
				assertEffects := func(version, started string) {
					t.Helper()
					assertPublicRecoveryEffects(t, runner, writes, writes, restarts, version, started)
					runner.mu.Lock()
					defer runner.mu.Unlock()
					if runner.starts != starts || runner.kills != 0 {
						t.Fatalf("unexpected extra execution: starts=%d kills=%d, want %d/0", runner.starts, runner.kills, starts)
					}
				}
				restart("interrupted-verification", hostops.StateFailed)
				assertEffects(restartBuildB, restartStartB)
				runner.mu.Lock()
				runner.interruptRestartVerification, runner.startupIdentityFault = false, ""
				protocol, flags := appwire.ProtocolVersion, "[]"
				if scenario.incompatible {
					protocol, flags = "other-protocol", `["api-log"]`
				}
				runner.launchResponse = fmt.Appendf(nil, `{"protocol":%q,"version":%q,"launch_flags":%s}`, protocol, restartBuildB, flags)
				version, started := restartBuildB, restartStartB
				switch scenario.control {
				case "predecessor":
					started = restartStartA
				case "build":
					version = restartBuildA
				case "pin":
					runner.healthPin = restartBuildA
				case "identity":
					started = ""
				case "stopped":
					version, started = "", ""
				case "final-identity":
					// First health read proves completion, second fails final startup
					// identity. The saved startup must survive that second refusal.
					runner.healthAfterStart, runner.startupIdentityFault = 0, "identity-failed"
				}
				runner.runningVersion, runner.startedAt = version, started
				runner.mu.Unlock()
				buildinfo.GitSHA = restartBuildA // Recovery still owns the original B build/pin.
				if scenario.control != "" {
					restart("unproven-effect", hostops.StateFailed)
					assertEffects(version, started)
					runner.mu.Lock()
					if scenario.control == "final-identity" && runner.healthAfterStart != 2 {
						t.Errorf("startup did not reach final identity: health reads=%d, want 2", runner.healthAfterStart)
					}
					runner.runningVersion, runner.startedAt = restartBuildB, restartStartB
					runner.healthPin, runner.startupIdentityFault = "", ""
					runner.mu.Unlock()
				}
				id := restart("settle-completed-effect", hostops.StateComplete)
				assertEffects(restartBuildB, restartStartB)
				runner.mu.Lock()
				epochs := append([]appwire.FencingEpoch(nil), runner.probeEpochs...)
				runner.mu.Unlock()
				record, _ := store.Record(id)
				if len(epochs) == 0 || epochs[len(epochs)-1] != epochOf(record) {
					t.Fatalf("missing independent epoch-bearing verification: epochs=%v record=%+v", epochs, record)
				}
				// Settlement consumed the saved effect, not the launch contract.
				restart("new-restart-still-blocked", hostops.StateFailed)
				assertEffects(restartBuildB, restartStartB)
			})
		})
	}
}
