package hub

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
)

const (
	restartBuildA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	restartBuildB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	restartStartA = "2026-01-01T00:00:00Z"
	restartStartB = "2026-01-02T00:00:00Z"
)

// Exercise the public operation worker and the real SSH manager together. Only
// SSH and the compiler process are faked: the first deployment loses its write
// response, and Restart must finish the captured B deployment before replacing A.
func TestHostRestartReconcilesDistinctBuildDeployment(t *testing.T) {
	// Not parallel: the controller build stamp is process-wide.
	sha, dirty, channel := buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel
	t.Cleanup(func() {
		buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel = sha, dirty, channel
	})
	buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel = restartBuildB, "", ""

	for _, scenario := range []struct {
		name         string
		probeVersion string
		probeStart   string
		want         hostops.State
	}{
		{name: "requested-build-replaces-predecessor", want: hostops.StateComplete},
		{name: "wrong-build", probeVersion: restartBuildA, want: hostops.StateFailed},
		{name: "wrong-process", probeStart: restartStartA, want: hostops.StateFailed},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			host := deployTestHost()
			registry, err := hostreg.New([]hostreg.Host{host})
			if err != nil {
				t.Fatal(err)
			}
			store, err := hostops.Open(hostops.StorePath(t.TempDir()))
			if err != nil {
				t.Fatal(err)
			}
			runner := &restartRecoveryRunner{
				diskVersion: restartBuildA, runningVersion: restartBuildA, startedAt: restartStartA,
				probeVersion: scenario.probeVersion, probeStart: scenario.probeStart,
			}
			manager := sshconn.New(registry, sshconn.Options{
				Runner: runner, Stderr: io.Discard,
				BuildBinary: func(_ context.Context, _, _, out string) error {
					return os.WriteFile(out, []byte(restartBuildB), 0o700)
				},
			})
			t.Cleanup(func() { _ = manager.Close() })
			configPath := deployTestConfigPath(t, host)
			m := newHubHostManager(nil, manager, hubcore.WebConfig{
				RemoteHostOpsStore: store, HubBootID: "restart-test-boot",
			}, configPath, registry, t.Logf)
			t.Cleanup(func() { m.ShutdownHostOperations(operationsShutdownTimeout) })
			if _, err := manager.Ensure(t.Context(), host.Name); err != nil {
				t.Fatalf("initial attach: %v", err)
			}
			plan, err := m.Plan(t.Context(), appwire.HostPlanParams{Name: host.Name})
			if err != nil {
				t.Fatalf("plan: %v", err)
			}
			deploy, err := m.Deploy(t.Context(), appwire.HostDeployParams{
				Name: host.Name, Token: plan.Token, OperationID: "interrupted-deploy",
			})
			if err != nil {
				t.Fatalf("deploy admission: %v", err)
			}
			m.cfg.opsWG.Wait()
			assertRestartRecoveryRecord(t, store, deploy.ID, hostops.StateFailed)
			host, _ = registry.Get(host.Name)
			restart, err := m.Restart(t.Context(), appwire.HostRestartParams{
				Name: host.Name, Generation: host.Generation, IncarnationID: host.IncarnationID,
				OperationID: "recover-restart",
			})
			if err != nil {
				t.Fatalf("restart admission: %v", err)
			}
			m.cfg.opsWG.Wait()
			runner.mu.Lock()
			writes, restarts, probes := runner.writes, runner.restarts, runner.afterProbes
			diskVersion, runningVersion := runner.diskVersion, runner.runningVersion
			runner.mu.Unlock()
			if writes != 2 || restarts != 1 || probes != 1 || diskVersion != restartBuildB || runningVersion != restartBuildB {
				t.Fatalf("recovery: writes=%d restarts=%d post-restart probes=%d disk=%q running=%q", writes, restarts, probes, diskVersion, runningVersion)
			}
			assertRestartRecoveryRecord(t, store, restart.ID, scenario.want)
		})
	}
}

func assertRestartRecoveryRecord(t *testing.T, store *hostops.Store, id string, want hostops.State) {
	t.Helper()
	record, ok := store.Record(id)
	if !ok || record.State != want || record.Result == nil || record.Result.OK != (want == hostops.StateComplete) {
		t.Fatalf("operation %q: state=%s result=%+v, want %s", id, record.State, record.Result, want)
	}
}

// restartRecoveryRunner models a remote machine at the process/network boundary.
// Health reports the actual replacement; the AppWire overrides exercise the
// worker's independent verification after sshconn has accepted that replacement.
type restartRecoveryRunner struct {
	mu                            sync.Mutex
	diskVersion, runningVersion   string
	startedAt                     string
	probeVersion, probeStart      string
	writes, restarts, afterProbes int
	streams                       []*probeFakeStdio
}

func (r *restartRecoveryRunner) Run(_ context.Context, argv []string, stdin io.Reader) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	command := strings.Join(argv, " ")
	switch {
	case strings.HasSuffix(command, "uname -s"):
		return []byte("Linux\n"), nil
	case strings.HasSuffix(command, "uname -m"):
		return []byte("x86_64\n"), nil
	case strings.Contains(command, "XDG_STATE_HOME"):
		return []byte("HOME=/home/ops\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
	case strings.HasSuffix(command, "id -u"):
		return []byte("1000\n"), nil
	case strings.Contains(command, "launch-check"):
		return fmt.Appendf(nil, `{"protocol":%q,"version":%q,"launch_flags":["api-log"]}`, appwire.ProtocolVersion, r.diskVersion), nil
	case strings.Contains(command, "test -d /opt/evener/bin"):
		return nil, nil
	case strings.Contains(command, "evener_resolve"):
		return []byte("/opt/evener/bin/evener\n"), nil
	case strings.Contains(command, "list-units"):
		return []byte("evener-hub.service loaded active running Evener Hub\n"), nil
	case strings.Contains(command, "systemctl restart"):
		r.restarts++
		r.runningVersion, r.startedAt = r.diskVersion, restartStartB
		for _, stream := range r.streams {
			_ = stream.Kill()
		}
		return nil, nil
	case strings.Contains(command, "api/health"):
		return fmt.Appendf(nil, `{"version":%q,"mobile_api_version":1,"hub_addr":"127.0.0.1:9180","started_at":%q}`, r.runningVersion, r.startedAt), nil
	case strings.Contains(command, "cat >"):
		r.writes++
		if r.writes == 1 {
			return nil, errors.New("interrupted transport")
		}
		data, err := io.ReadAll(stdin)
		if err != nil {
			return nil, err
		}
		r.diskVersion = string(data)
		return nil, nil
	default:
		return nil, fmt.Errorf("unexpected remote command: %v", argv)
	}
}

func (r *restartRecoveryRunner) Start(context.Context, []string, io.Writer) (sshconn.Stdio, error) {
	stream := probeBridge(func(appwire.HostRunningParams) (appwire.HostRunningResponse, appwire.WireError) {
		r.mu.Lock()
		defer r.mu.Unlock()
		version, startedAt := r.runningVersion, r.startedAt
		if r.restarts > 0 {
			r.afterProbes++
			if r.probeVersion != "" {
				version = r.probeVersion
			}
			if r.probeStart != "" {
				startedAt = r.probeStart
			}
		}
		return appwire.HostRunningResponse{
			BuildRevision: version, Healthy: true, ProcessStartTime: startedAt,
		}, appwire.WireError{}
	})
	r.mu.Lock()
	r.streams = append(r.streams, stream)
	r.mu.Unlock()
	return stream, nil
}
