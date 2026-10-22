package hub

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
	"primeradiant.com/evener/internal/hubendpoint"
)

// The endpoint is unavailable when the restart selects its predecessor, but
// process, birth, launch and supervisor evidence remain independently available.
func TestHostRestartRecoveryWithoutPredecessorTime(t *testing.T) {
	sha, dirty, channel := buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel
	t.Cleanup(func() { buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel = sha, dirty, channel })
	for _, scenario := range []string{"lost-response", "stopped-replay", "unusable-contract", "wrong-refresh-process", "missing-refresh-time"} {
		t.Run(scenario, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel = restartBuildA, "", "snapshot"
				runner := &restartRecoveryRunner{diskVersion: restartBuildA, runningVersion: restartBuildA, startedAt: restartStartA, delayBridgeExit: true, supervisor: "systemd"}
				remote := &unknownRestartTimeRunner{restartRecoveryRunner: runner, stoppedReplay: scenario == "stopped-replay"}
				m, manager, store, host, events := publicRecoveryHubForHost(t, runner, remote, restartBuildA, deployTestHost())
				if _, err := manager.Ensure(t.Context(), host.Name); err != nil {
					t.Fatal(err)
				}
				waitPublicRecoveryEvent(t, events, sshconn.EventAttached)
				restartHost := m.cfg.restartHost
				m.cfg.restartHost = func(ctx context.Context, entry hostreg.Host, facts sshconn.Preflight, guard func() error) (string, error) {
					runner.mu.Lock()
					remote.hidePredecessor = true
					runner.mu.Unlock()
					return restartHost(ctx, entry, facts, guard)
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
				restart("interrupted-unavailable-predecessor", hostops.StateFailed)
				if remote.interrupts != 1 || remote.hiddenReports == 0 {
					t.Fatalf("restart did not select unavailable health and reach the effect: interruptions=%d hidden reports=%d", remote.interrupts, remote.hiddenReports)
				}
				runner.mu.Lock()
				initialRestarts, initialStarts := runner.restarts, runner.starts
				if scenario == "unusable-contract" {
					runner.launchResponse = []byte(`{"hub_endpoint_schema":1,"protocol":"` + appwire.ProtocolVersion + `","version":"` + restartBuildA + `","launch_flags":[]}`)
				}
				if scenario == "wrong-refresh-process" {
					runner.probeStart = restartStartA
				}
				if scenario == "missing-refresh-time" {
					runner.omitProbeStart = true
				}
				runner.mu.Unlock()
				if initialRestarts != 1 || initialStarts != 0 {
					t.Fatalf("initial effects: restarts=%d starts=%d", initialRestarts, initialStarts)
				}
				var recovered *sshconn.RestartResult
				recoverDeployment := m.cfg.recoverDeployment
				m.cfg.recoverDeployment = func(ctx context.Context, entry hostreg.Host, holder hostops.Holder, guard func() error) (*sshconn.RestartResult, error) {
					result, err := recoverDeployment(ctx, entry, holder, guard)
					if err == nil && result != nil {
						resultCopy := *result
						recovered = &resultCopy
					}
					return result, err
				}
				want := hostops.StateComplete
				if scenario == "wrong-refresh-process" || scenario == "missing-refresh-time" {
					want = hostops.StateFailed
				}
				id := restart("recover-verified-successor", want)
				start, err := time.Parse(time.RFC3339, restartStartB)
				if err != nil {
					t.Fatal(err)
				}
				if recovered == nil || !recovered.ReplacedStartedAt.IsZero() || recovered.StartedAt.IsZero() || !recovered.StartedAt.Equal(start) || recovered.TargetVersion != restartBuildA {
					t.Fatalf("verified successor timing was not returned: %+v", recovered)
				}
				runner.mu.Lock()
				defer runner.mu.Unlock()
				starts := 0
				if scenario == "stopped-replay" {
					starts = 1
				}
				if runner.restarts != 1 || runner.starts != starts || runner.kills != 0 || runner.writes != 0 || runner.afterProbes != 1 {
					t.Fatalf("recovery effects: restarts=%d starts=%d kills=%d writes=%d refresh probes=%d", runner.restarts, runner.starts, runner.kills, runner.writes, runner.afterProbes)
				}
				record, _ := store.Record(id)
				if len(runner.probeEpochs) == 0 || runner.probeEpochs[len(runner.probeEpochs)-1] != epochOf(record) {
					t.Fatal("recovery skipped epoch-bearing refresh")
				}
			})
		})
	}
}

type unknownRestartTimeRunner struct {
	*restartRecoveryRunner
	hidePredecessor           bool
	stoppedReplay             bool
	interrupts, hiddenReports int
}

func (r *unknownRestartTimeRunner) Run(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
	out, err := r.restartRecoveryRunner.Run(ctx, argv, in)
	if err != nil {
		return out, err
	}
	command := strings.Join(argv[12:], " ")
	r.mu.Lock()
	defer r.mu.Unlock()
	if command == "systemctl restart evener-hub.service" && r.interrupts == 0 {
		r.interrupts++
		if r.stoppedReplay {
			r.runningVersion, r.startedAt = "", ""
		}
		return nil, errors.New("restart reply lost")
	}
	if r.hidePredecessor && r.startedAt == restartStartA && strings.Contains(command, " hub endpoint --json") {
		var report hubendpoint.Report
		if err := json.Unmarshal(out, &report); err != nil {
			return nil, err
		}
		report.State, report.Running = "unavailable", nil
		r.hiddenReports++
		return json.Marshal(report)
	}
	return out, nil
}
