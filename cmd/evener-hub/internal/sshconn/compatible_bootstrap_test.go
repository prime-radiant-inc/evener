package sshconn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// stoppedInstallation scripts only the remote process boundary. The bridge cannot
// initialize until the manager actually issues the startup command.
type stoppedInstallation struct {
	version, sha      string
	flags             []string
	starts, deploys   int
	running           bool
	interrupt, landed bool
}

func (s *stoppedInstallation) manager(t *testing.T, opts Options) *Manager {
	t.Helper()
	fr := &fakeRunner{}
	fr.runFn = func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
		command := strings.Join(argv, " ")
		switch {
		case strings.Contains(command, "launch-check"):
			return json.Marshal(map[string]any{"protocol": appwire.ProtocolVersion, "version": s.version, "launch_flags": s.flags})
		case strings.Contains(command, "api/health"):
			if !s.running {
				return nil, errors.New("connection refused")
			}
			return []byte(fmt.Sprintf(`{"version":%q,"backend_git_sha":%q,"mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`, s.version, s.sha)), nil
		case strings.Contains(command, "lsof -ti :9180"):
			if s.running {
				return []byte("1234\n"), nil
			}
			return []byte(noListenerMarker), nil
		case strings.Contains(command, "list-units"):
			return []byte("evener-hub.service loaded inactive dead\n"), nil
		case strings.Contains(command, "systemctl start"), strings.Contains(command, "systemctl restart"):
			s.starts++
			if s.interrupt && s.starts == 1 {
				s.running = s.landed
				return nil, errors.New("transport interrupted during start")
			}
			s.running = true
			return nil, nil
		case strings.Contains(command, "cat >"), strings.Contains(command, "evener-install.XXXXXX"):
			s.deploys++
			return nil, errors.New("unexpected deploy")
		default:
			return cannedRun(nil)(ctx, argv, stdin)
		}
	}
	fr.startFn = func(ctx context.Context, argv []string, stderr io.Writer) (Stdio, error) {
		if !s.running {
			return nil, errors.New("bridge has no listener")
		}
		return goodStartFn(t)(ctx, argv, stderr)
	}
	opts.sleep = func(context.Context, time.Duration) error { return nil }
	return newTestManager(t, testRegistry(t, hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}), fr, opts)
}

func TestEnsureStoppedCompatibleInstallationBootstrapsWithoutDeploy(t *testing.T) {
	for _, version := range []string{"remote-clean", "dev", "remote-dirty"} {
		t.Run(version, func(t *testing.T) {
			s := &stoppedInstallation{version: version, flags: []string{"api-log"}}
			m := s.manager(t, Options{controllerVersionOverride: "controller-clean", BuildBinary: writeStageBinary})
			ch, err := m.Ensure(t.Context(), "alpha")
			if err != nil || ch == nil {
				t.Fatalf("Connect: channel=%v err=%v", ch, err)
			}
			if s.starts != 1 || s.deploys != 0 {
				t.Fatalf("starts=%d deploys=%d", s.starts, s.deploys)
			}
		})
	}
}

func TestReconnectStoppedCompatibleInstallationDoesNotBootstrap(t *testing.T) {
	s := &stoppedInstallation{version: "remote-clean", flags: []string{"api-log"}}
	m := s.manager(t, Options{controllerVersionOverride: "controller-clean", BuildBinary: writeStageBinary})
	host, _ := m.reg.Get("alpha")
	ch, err := m.ensureOnce(t.Context(), host, false)
	if err == nil || ch != nil || s.starts != 0 || s.deploys != 0 {
		t.Fatalf("reconnect: channel=%v err=%v starts=%d deploys=%d", ch, err, s.starts, s.deploys)
	}
}

func TestEnsureStoppedInstallationRequiresLaunchCapabilities(t *testing.T) {
	s := &stoppedInstallation{version: "remote-clean", flags: []string{}}
	m := s.manager(t, Options{DeployDisabled: true})
	ch, err := m.Ensure(t.Context(), "alpha")
	if !errors.Is(err, ErrLaunchContract) || ch != nil || s.starts != 0 || s.deploys != 0 {
		t.Fatalf("Connect: channel=%v err=%v starts=%d deploys=%d", ch, err, s.starts, s.deploys)
	}
}

func TestEnsureSnapshotBootstrapDoesNotRequireControllerCommit(t *testing.T) {
	savedChannel, savedSHA := buildinfo.Channel, buildinfo.GitSHA
	t.Cleanup(func() { buildinfo.Channel, buildinfo.GitSHA = savedChannel, savedSHA })
	buildinfo.Channel, buildinfo.GitSHA = "snapshot", "controller-sha"
	for _, mode := range []string{"direct", "interrupted-before-start", "interrupted-after-start"} {
		t.Run(mode, func(t *testing.T) {
			s := &stoppedInstallation{version: "snapshot", sha: "remote-sha", flags: []string{"api-log"}, interrupt: mode != "direct", landed: mode == "interrupted-after-start"}
			m := s.manager(t, Options{controllerVersionOverride: "snapshot", BuildBinary: writeStageBinary})
			recordConnectionOperations(t, m)
			if s.interrupt {
				if _, err := m.Ensure(t.Context(), "alpha"); !errors.Is(err, ErrRestart) {
					t.Fatalf("interrupted start: %v", err)
				}
				if pending := m.pendingRestart("alpha"); pending.command == "" || !pending.start {
					t.Fatalf("start was not retained: %+v", pending)
				}
			}
			ch, err := m.Ensure(t.Context(), "alpha")
			if err != nil || ch == nil {
				t.Fatalf("Connect: channel=%v err=%v", ch, err)
			}
			wantStarts := 1
			if mode == "interrupted-before-start" {
				wantStarts = 2
			}
			if s.starts != wantStarts || s.deploys != 0 || m.pendingRestart("alpha").command != "" {
				t.Fatalf("starts=%d deploys=%d pending=%+v", s.starts, s.deploys, m.pendingRestart("alpha"))
			}
		})
	}
}

// Real operation records prove a failed recovery cannot become a completed
// deployment merely because an older, compatible process answers initialize.
func recordConnectionOperations(t *testing.T, m *Manager) (*hostops.Store, *[]string) {
	t.Helper()
	store := ensureDeployTestStore(t)
	ids := []string{}
	hook := func(kind hostops.Kind) func(hostreg.Host) (func(error), error) {
		return func(host hostreg.Host) (func(error), error) {
			record, err := store.Create(hostops.NewRecord{ClientOperationID: fmt.Sprintf("connection-%d", len(ids)), Host: host.Name, Kind: kind, Generation: host.Generation, IncarnationID: host.IncarnationID})
			if err != nil {
				return nil, err
			}
			ids = append(ids, record.ID)
			return func(err error) {
				state := hostops.StateComplete
				if err != nil {
					state = hostops.StateFailed
				}
				if _, transitionErr := store.TransitionToState(record.ID, state, &hostops.Result{OK: err == nil, Message: "connection operation finished"}, ""); transitionErr != nil {
					t.Errorf("journal transition: %v", transitionErr)
				}
			}, nil
		}
	}
	m.SetEnsureDeployHook(hook(hostops.KindDeploy))
	m.SetEnsureRestartHook(hook(hostops.KindRestart))
	return store, &ids
}

func TestEnsureCompatibleLiveHubDoesNotSettlePendingRestart(t *testing.T) {
	savedChannel, savedSHA := buildinfo.Channel, buildinfo.GitSHA
	t.Cleanup(func() { buildinfo.Channel, buildinfo.GitSHA = savedChannel, savedSHA })
	buildinfo.Channel, buildinfo.GitSHA = "snapshot", "deployed-sha"
	for _, mode := range []string{"disk-drift", "disk-drift-replay", "wrong-commit", "old-process", "unknown-start"} {
		t.Run(mode, func(t *testing.T) {
			version, diskVersion, sha := "dev", "dev", "old-sha"
			started := "2026-01-01T00:00:00Z"
			present := true
			fr := deployRunner(t, func(int) ([]byte, error) { return launchContract(diskVersion), nil }, func(int) ([]byte, error) {
				if !present {
					return nil, errors.New("health unavailable")
				}
				return []byte(fmt.Sprintf(`{"version":%q,"backend_git_sha":%q,"mobile_api_version":1,"hub_addr":"127.0.0.1:9180","started_at":%q}`, version, sha, started)), nil
			})
			run := fr.runFn
			fr.runFn = func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
				if strings.Contains(strings.Join(argv, " "), "systemctl restart") {
					present = true
				}
				return run(ctx, argv, stdin)
			}
			requireProtocolUpgrade(t, fr)
			m := newTestManager(t, testRegistry(t, hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}), fr, Options{controllerVersionOverride: "dev", BuildBinary: writeStageBinary, sleep: func(context.Context, time.Duration) error { return nil }})
			store, ids := recordConnectionOperations(t, m)
			if _, err := m.Ensure(t.Context(), "alpha"); !errors.Is(err, ErrRestart) {
				t.Fatalf("initial deployment did not leave an interrupted restart: %v", err)
			}
			pending := m.pendingRestart("alpha")
			if pending.command == "" || pending.start || len(*ids) != 1 {
				t.Fatalf("pending=%+v journal=%v", pending, *ids)
			}
			switch mode {
			case "disk-drift", "disk-drift-replay":
				version, diskVersion, started = "other-build", "other-build", "2026-01-02T00:00:00Z"
				present = mode != "disk-drift-replay"
			case "wrong-commit":
				started = "2026-01-02T00:00:00Z"
			case "old-process":
				sha = "deployed-sha"
			case "unknown-start":
				sha, started = "deployed-sha", "0001-01-01T00:00:00Z"
			}
			ch, recoveryErr := m.Ensure(t.Context(), "alpha")
			if !errors.Is(recoveryErr, ErrRestart) || ch != nil || m.pendingRestart("alpha").command == "" {
				t.Fatalf("unproved recovery: channel=%v error=%v pending=%+v", ch, recoveryErr, m.pendingRestart("alpha"))
			}
			for _, id := range *ids {
				record, ok := store.Record(id)
				if !ok || record.State != hostops.StateFailed {
					t.Fatalf("unproved recovery settled journal: %+v", record)
				}
			}
			version, sha, started = "dev", "deployed-sha", "2026-01-03T00:00:00Z"
			if ch, err := m.Ensure(t.Context(), "alpha"); err != nil || ch == nil || m.pendingRestart("alpha").command != "" {
				t.Fatalf("proved replacement did not recover: channel=%v err=%v pending=%+v", ch, err, m.pendingRestart("alpha"))
			}
		})
	}
}
