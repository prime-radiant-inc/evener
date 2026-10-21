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
			restarts, writes := 0, 0
			fr := deployRunner(t, func(int) ([]byte, error) { return launchContract(diskVersion), nil }, func(int) ([]byte, error) {
				if !present {
					return nil, errors.New("health unavailable")
				}
				return []byte(fmt.Sprintf(`{"version":%q,"backend_git_sha":%q,"mobile_api_version":1,"hub_addr":"127.0.0.1:9180","started_at":%q}`, version, sha, started)), nil
			})
			run := fr.runFn
			fr.runFn = func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
				command := strings.Join(argv, " ")
				if strings.Contains(command, "systemctl restart") {
					restarts++
					present = true
				}
				if strings.Contains(command, "cat >") {
					writes++
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
			wantErr := ErrRestart
			switch mode {
			case "disk-drift", "disk-drift-replay":
				version, diskVersion, started = "other-build", "other-build", "2026-01-02T00:00:00Z"
				present = mode != "disk-drift-replay"
				wantErr = ErrVersionMismatch
			case "wrong-commit":
				started = "2026-01-02T00:00:00Z"
			case "old-process":
				sha = "deployed-sha"
			case "unknown-start":
				sha, started = "deployed-sha", "0001-01-01T00:00:00Z"
			}
			beforeRestarts, beforeWrites, beforePresent := restarts, writes, present
			ch, recoveryErr := m.Ensure(t.Context(), "alpha")
			if !errors.Is(recoveryErr, wantErr) || ch != nil || m.pendingRestart("alpha") != pending {
				t.Fatalf("unproved recovery: channel=%v error=%v pending=%+v", ch, recoveryErr, m.pendingRestart("alpha"))
			}
			if errors.Is(wantErr, ErrVersionMismatch) && (restarts != beforeRestarts || writes != beforeWrites || present != beforePresent) {
				t.Fatalf("disk drift mutated remote: restarts=%d writes=%d present=%t", restarts, writes, present)
			}
			for _, id := range *ids {
				record, ok := store.Record(id)
				if !ok || record.State != hostops.StateFailed {
					t.Fatalf("unproved recovery settled journal: %+v", record)
				}
			}
			version, sha, started, present = "dev", "deployed-sha", "2026-01-03T00:00:00Z", true
			beforeRestarts, beforeWrites = restarts, writes
			if ch, err := m.Ensure(t.Context(), "alpha"); err != nil || ch == nil || m.pendingRestart("alpha").command != "" {
				t.Fatalf("proved replacement did not recover: channel=%v err=%v pending=%+v", ch, err, m.pendingRestart("alpha"))
			}
			if restarts != beforeRestarts || writes != beforeWrites {
				t.Fatalf("completed target mutated remote: restarts=%d writes=%d", restarts, writes)
			}
		})
	}
}

func TestEnsureRecoversDeploymentBeforeRestartMarker(t *testing.T) {
	savedChannel, savedTag, savedDirty := buildinfo.Channel, buildinfo.ReleaseTag, buildinfo.GitDirty
	t.Cleanup(func() {
		buildinfo.Channel, buildinfo.ReleaseTag, buildinfo.GitDirty = savedChannel, savedTag, savedDirty
	})
	buildinfo.Channel, buildinfo.ReleaseTag, buildinfo.GitDirty = "release", "v0.9.1", "false"
	for _, phase := range []string{"write-ack", "write-not-landed", "installer-write-ack", "launch-check", "supervisor", "deploy-only", "deploy-only-not-landed", "same-label-not-landed", "deploy-only-same-label-not-landed", "source-refusal"} {
		t.Run(phase, func(t *testing.T) {
			requested := "v0.9.1"
			disk, running, started := "old-build", "old-build", "2026-01-01T00:00:00Z"
			if strings.Contains(phase, "same-label") {
				requested, disk, running = "dev", "dev", "dev"
			}
			initialRunning := running
			requestedInstalled := false
			interrupted := errors.New("injected transport interruption")
			inject := true
			writes, restarts := 0, 0
			fr := deployRunner(t, func(int) ([]byte, error) {
				if inject && phase == "launch-check" && writes > 0 {
					return nil, interrupted
				}
				return launchContract(disk), nil
			}, func(int) ([]byte, error) {
				return []byte(fmt.Sprintf(`{"version":%q,"mobile_api_version":1,"hub_addr":"127.0.0.1:9180","started_at":%q}`, running, started)), nil
			})
			run := fr.runFn
			fr.runFn = func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
				command := strings.Join(argv, " ")
				switch {
				case strings.Contains(command, "cat >"), strings.Contains(command, "evener-install.XXXXXX"):
					writes++
					if inject && strings.HasSuffix(phase, "not-landed") {
						return nil, interrupted
					}
					disk = requested // The atomic rename landed before the acknowledgement was lost.
					requestedInstalled = true
					if inject && (phase == "write-ack" || phase == "installer-write-ack" || phase == "deploy-only") {
						return nil, interrupted
					}
					if strings.Contains(command, "evener-install.XXXXXX") {
						return nil, nil
					}
				case strings.Contains(command, "list-units"):
					if inject && phase == "supervisor" {
						return nil, interrupted
					}
				case strings.Contains(command, "systemctl restart"):
					restarts++
					running, started = disk, "2026-01-02T00:00:00Z"
				}
				return run(ctx, argv, stdin)
			}
			host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
			opts := Options{controllerVersionOverride: requested, BuildBinary: writeStageBinary}
			switch phase {
			case "installer-write-ack":
				opts.BuildBinary = nil
			case "source-refusal":
				opts.BuildBinary = func(context.Context, string, string, string) error { return interrupted }
			}
			m := newTestManager(t, testRegistry(t, host), fr, opts)
			recordConnectionOperations(t, m)
			facts, err := m.preflight(t.Context(), host)
			if err != nil {
				t.Fatal(err)
			}
			_, after, err := m.DeployForOperation(t.Context(), host, facts, !strings.HasPrefix(phase, "deploy-only"), nil)
			if phase == "supervisor" && err == nil {
				_, err = m.RestartForOperation(t.Context(), host, after, nil)
			}
			wantWrites := 1
			if phase == "source-refusal" {
				wantWrites = 0
			}
			if err == nil || writes != wantWrites || restarts != 0 {
				t.Fatalf("interrupted deployment: err=%v writes=%d restarts=%d", err, writes, restarts)
			}
			inject = false
			ch, err := m.Ensure(t.Context(), host.Name)
			wantRunning, wantRestarts := requested, 1
			if strings.HasPrefix(phase, "deploy-only") || phase == "source-refusal" {
				wantRunning, wantRestarts = initialRunning, 0
			}
			if strings.HasSuffix(phase, "not-landed") || phase == "write-ack" || phase == "installer-write-ack" || phase == "deploy-only" {
				wantWrites++
			}
			wantInstalled := phase != "source-refusal"
			if err != nil || ch == nil || running != wantRunning || restarts != wantRestarts || writes != wantWrites || requestedInstalled != wantInstalled || m.pendingRestart(host.Name).active() {
				t.Fatalf("recovery: channel=%v err=%v running=%q writes=%d restarts=%d requestedInstalled=%v", ch, err, running, writes, restarts, requestedInstalled)
			}
		})
	}
}

func TestEnsureStoppedMalformedContractDoesNotDeploy(t *testing.T) {
	for _, body := range []string{`{`, `{"protocol":"evener-appwire-v7"}`, `{"version":"remote-build"}`} {
		t.Run(body, func(t *testing.T) {
			s := &stoppedInstallation{version: "remote-build", flags: []string{"api-log"}}
			builds := 0
			m := s.manager(t, Options{controllerVersionOverride: "controller-build", BuildBinary: func(ctx context.Context, goos, goarch, path string) error {
				builds++
				return writeStageBinary(ctx, goos, goarch, path)
			}})
			fr := m.runner.(*fakeRunner)
			run := fr.runFn
			fr.runFn = func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
				command := strings.Join(argv, " ")
				switch {
				case strings.Contains(command, "launch-check"):
					return []byte(body), nil
				case strings.Contains(command, "test -d /opt/evener/bin"):
					return nil, nil
				case strings.Contains(command, "evener_resolve"):
					return []byte("/opt/evener/bin/evener\n"), nil
				}
				return run(ctx, argv, stdin)
			}
			recordConnectionOperations(t, m)
			ch, err := m.Ensure(t.Context(), "alpha")
			if !errors.Is(err, ErrPreflightDecode) || ch != nil || builds != 0 || s.deploys != 0 || s.starts != 0 {
				t.Fatalf("Connect: err=%v channel=%v builds=%d writes=%d starts=%d", err, ch, builds, s.deploys, s.starts)
			}
		})
	}
}
