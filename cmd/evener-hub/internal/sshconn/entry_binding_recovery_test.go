package sshconn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

func TestRecoveryRetainsEntryBindingAcrossCommandRecording(t *testing.T) {
	for _, mode := range []string{"restart-alive", "restart-stopped", "start-stopped", "completed"} {
		t.Run(mode, func(t *testing.T) {
			const target = "requested-build"
			running, started := mode != "start-stopped", "2026-01-01T00:00:00Z"
			effects, writes := 0, 0
			entryValid := true
			refused := errors.New("entry validation refused")
			guard := func() error {
				if !entryValid {
					return refused
				}
				return nil
			}
			fr := deployRunner(t, func(int) ([]byte, error) { return launchContract(target), nil }, func(int) ([]byte, error) {
				if !running {
					return nil, errors.New("no listener")
				}
				return fmt.Appendf(nil, `{"version":%q,"mobile_api_version":1,"hub_addr":"127.0.0.1:9180","started_at":%q}`, target, started), nil
			})
			run := fr.runFn
			fr.runFn = func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
				switch command := strings.Join(argv, " "); {
				case strings.Contains(command, "list-units") && mode == "start-stopped":
					return []byte("evener-hub.service loaded inactive dead\n"), nil
				case strings.Contains(command, "lsof -ti :9180"):
					return []byte(noListenerMarker), nil
				case strings.Contains(command, "systemctl restart"):
					effects++
					if effects == 1 {
						running = mode == "restart-alive" || mode == "completed"
						return nil, errors.New("command acknowledgement lost")
					}
					running, started = true, "2026-01-02T00:00:00Z"
					return nil, nil
				case strings.Contains(command, "cat >"), strings.Contains(command, "evener-install.XXXXXX"):
					writes++
				}
				return run(ctx, argv, stdin)
			}
			host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
			m := newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "other-controller", BuildBinary: writeStageBinary, sleep: func(context.Context, time.Duration) error { return nil }})
			host, _ = m.reg.Get(host.Name)
			recordConnectionOperations(t, m)
			facts, err := m.preflight(t.Context(), host)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := m.RestartForOperation(t.Context(), host, facts, guard); !errors.Is(err, ErrRestart) || effects != 1 {
				t.Fatalf("initial command interruption: effects=%d err=%v", effects, err)
			}
			pending := m.pendingRestart(host.Name)
			entryValid = false
			if mode == "completed" {
				started = "2026-01-02T00:00:00Z"
			}
			ch, err := m.ensureOnce(t.Context(), host, false)
			if ch != nil {
				defer func() { _ = ch.Close() }()
			}
			if mode != "completed" {
				if !errors.Is(err, refused) || ch != nil || effects != 1 || writes != 0 || m.pendingRestart(host.Name) != pending {
					t.Fatalf("stale-entry replay: err=%v channel=%v effects=%d writes=%d pending=%+v want=%+v", err, ch, effects, writes, m.pendingRestart(host.Name), pending)
				}
				entryValid = true
				ch, err = m.ensureOnce(t.Context(), host, false)
				if ch != nil {
					defer func() { _ = ch.Close() }()
				}
			}
			wantEffects := 2
			if mode == "completed" {
				wantEffects = 1
			}
			if err != nil || ch == nil || effects != wantEffects || writes != 0 || !running || started != "2026-01-02T00:00:00Z" || m.pendingRestart(host.Name).active() {
				t.Fatalf("original effect recovery: err=%v channel=%v effects=%d writes=%d running=%t started=%s pending=%+v", err, ch, effects, writes, running, started, m.pendingRestart(host.Name))
			}
		})
	}
}

func TestRecoveryEntryBindingDeploymentWriteAndReplay(t *testing.T) {
	sha, channel := buildinfo.GitSHA, buildinfo.Channel
	t.Cleanup(func() { buildinfo.GitSHA, buildinfo.Channel = sha, channel })
	buildinfo.GitSHA, buildinfo.Channel = "original-pin", "snapshot"
	for _, acknowledged := range []bool{false, true} {
		for _, stopped := range []bool{false, true} {
			t.Run(fmt.Sprintf("acknowledged=%t/stopped=%t", acknowledged, stopped), func(t *testing.T) {
				writes, launches := 0, 0
				valid, badContract := true, true
				running, started := "predecessor", "2026-01-01T00:00:00Z"
				refused := errors.New("selected entry changed")
				guard := func() error {
					if !valid {
						return refused
					}
					return nil
				}
				fr := deployRunner(t, func(int) ([]byte, error) {
					if writes > 0 && badContract {
						return []byte(`{`), nil
					}
					return launchContract("snapshot"), nil
				}, func(int) ([]byte, error) {
					if running == "" {
						return nil, errors.New("no listener")
					}
					return fmt.Appendf(nil, `{"version":%q,"backend_git_sha":"original-pin","mobile_api_version":1,"hub_addr":"127.0.0.1:9180","started_at":%q}`, running, started), nil
				})
				run := fr.runFn
				fr.runFn = func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
					switch command := strings.Join(argv, " "); {
					case strings.Contains(command, "cat >"):
						writes++
						if writes == 1 && !acknowledged {
							return nil, errors.New("lost write acknowledgement")
						}
					case strings.Contains(command, "list-units") && stopped:
						return nil, nil
					case strings.Contains(command, "lsof -ti :9180"):
						return []byte(noListenerMarker), nil
					case strings.Contains(command, "systemctl restart"), strings.Contains(command, "nohup"):
						launches++
						if launches == 1 {
							return nil, errors.New("lost launch acknowledgement")
						}
						running, started = "snapshot", "2026-01-02T00:00:00Z"
						return nil, nil
					}
					return run(ctx, argv, stdin)
				}
				host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
				m := newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "snapshot", BuildBinary: writeStageBinary, sleep: func(context.Context, time.Duration) error { return nil }})
				host, _ = m.reg.Get(host.Name)
				recordConnectionOperations(t, m)
				facts, err := m.preflight(t.Context(), host)
				if err != nil {
					t.Fatal(err)
				}
				if _, _, err := m.DeployForOperation(t.Context(), host, facts, true, guard); err == nil {
					t.Fatal("initial deployment unexpectedly verified")
				}
				pending := m.pendingRestart(host.Name)
				if writes != 1 || !pending.deployment || pending.writeAcknowledged != acknowledged || pending.binding == nil || pending.expectedGitSHA != "original-pin" || pending.replaced.version != "predecessor" {
					t.Fatalf("initial owned write: writes=%d pending=%+v", writes, pending)
				}
				valid, badContract = false, false
				if stopped {
					running = ""
				}
				if _, err := m.ensureOnce(t.Context(), host, false); !errors.Is(err, refused) || writes != 1 || launches != 0 || m.pendingRestart(host.Name) != pending {
					t.Fatalf("unauthorized recovery: err=%v writes=%d launches=%d pending=%+v", err, writes, launches, m.pendingRestart(host.Name))
				}
				valid = true
				if _, err := m.ensureOnce(t.Context(), host, false); !errors.Is(err, ErrRestart) || launches != 1 {
					t.Fatalf("command interruption: err=%v launches=%d", err, launches)
				}
				command := m.pendingRestart(host.Name)
				if command.command == "" || command.binding != pending.binding || command.expectedVersion != pending.expectedVersion || command.expectedGitSHA != pending.expectedGitSHA || command.replaced != pending.replaced || command.start != stopped {
					t.Fatalf("deployment to command lost ownership: before=%+v after=%+v", pending, command)
				}
				valid = false
				if _, err := m.ensureOnce(t.Context(), host, false); !errors.Is(err, refused) || launches != 1 || m.pendingRestart(host.Name) != command {
					t.Fatalf("command replay lost binding: err=%v launches=%d pending=%+v", err, launches, m.pendingRestart(host.Name))
				}
				valid = true
				m.opts.controllerVersionOverride = "later-controller"
				ch, err := m.ensureOnce(t.Context(), host, false)
				if ch != nil {
					defer func() { _ = ch.Close() }()
				}
				wantWrites := 2
				if acknowledged {
					wantWrites = 1
				}
				if err != nil || ch == nil || writes != wantWrites || launches != 2 || running != "snapshot" || m.pendingRestart(host.Name) != (pendingRestartState{}) || m.resolvedTarget(host.Name) != host.EvenerPath {
					t.Fatalf("recovery: err=%v writes=%d launches=%d running=%s pending=%+v target=%s", err, writes, launches, running, m.pendingRestart(host.Name), m.resolvedTarget(host.Name))
				}
			})
		}
	}
}

func TestRecoveryEntryBindingDeploySupersession(t *testing.T) {
	sha, channel := buildinfo.GitSHA, buildinfo.Channel
	t.Cleanup(func() { buildinfo.GitSHA, buildinfo.Channel = sha, channel })
	buildinfo.GitSHA, buildinfo.Channel = "original-pin", "snapshot"
	for _, source := range []string{"push", "installer"} {
		t.Run(source, func(t *testing.T) {
			writes, entry := 0, 1
			refused, sourceFailure := errors.New("entry refused"), errors.New("source unavailable")
			validator := func(bound int) func() error {
				return func() error {
					if entry != bound {
						return refused
					}
					return nil
				}
			}
			fr := deployRunner(t, func(int) ([]byte, error) { return launchContract("snapshot"), nil }, func(int) ([]byte, error) { return nil, errors.New("no listener") })
			run := fr.runFn
			fr.runFn = func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
				command := strings.Join(argv, " ")
				if strings.Contains(command, "cat >") || strings.Contains(command, "evener-install.XXXXXX") {
					writes++
					return nil, errors.New("write acknowledgement lost")
				}
				return run(ctx, argv, stdin)
			}
			host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
			m := newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "snapshot", BuildBinary: writeStageBinary})
			host, _ = m.reg.Get(host.Name)
			facts, err := m.preflight(t.Context(), host)
			if err != nil {
				t.Fatal(err)
			}
			if source == "installer" {
				m.opts.BuildBinary = nil
			}
			// A refused first write creates neither effect ownership nor target.
			if _, _, err := m.DeployForOperation(t.Context(), host, facts, true, validator(2)); !errors.Is(err, refused) || writes != 0 || m.pendingRestart(host.Name).active() || m.resolvedTarget(host.Name) != "" {
				t.Fatalf("refused first write: err=%v writes=%d pending=%+v", err, writes, m.pendingRestart(host.Name))
			}
			if _, _, err := m.DeployForOperation(t.Context(), host, facts, true, validator(1)); !errors.Is(err, ErrDeploy) || writes != 1 {
				t.Fatalf("initial write: err=%v writes=%d", err, writes)
			}
			old := m.pendingRestart(host.Name)
			entry = 2
			m.opts.controllerVersionOverride = "new-request"
			m.opts.BuildBinary = func(context.Context, string, string, string) error { return sourceFailure }
			if _, _, err := m.DeployForOperation(t.Context(), host, facts, true, validator(2)); !errors.Is(err, sourceFailure) || writes != 1 || m.pendingRestart(host.Name) != old {
				t.Fatalf("source refusal superseded intent: err=%v writes=%d pending=%+v", err, writes, m.pendingRestart(host.Name))
			}
			m.opts.BuildBinary = writeStageBinary
			if source == "installer" {
				m.opts.BuildBinary = nil
			}
			if _, _, err := m.DeployForOperation(t.Context(), host, facts, true, validator(1)); !errors.Is(err, refused) || writes != 1 || m.pendingRestart(host.Name) != old {
				t.Fatalf("pre-write refusal superseded intent: err=%v writes=%d pending=%+v", err, writes, m.pendingRestart(host.Name))
			}
			if _, _, err := m.DeployForOperation(t.Context(), host, facts, true, validator(2)); !errors.Is(err, ErrDeploy) || writes != 2 {
				t.Fatalf("authorized superseding write: err=%v writes=%d", err, writes)
			}
			newIntent := m.pendingRestart(host.Name)
			if !newIntent.deployment || newIntent.writeAcknowledged || newIntent.binding == nil || newIntent.binding == old.binding || newIntent.expectedVersion != "new-request" || newIntent.expectedGitSHA != "original-pin" {
				t.Fatalf("superseding intent: %+v", newIntent)
			}
			entry = 1
			recordConnectionOperations(t, m)
			if _, err := m.ensureOnce(t.Context(), host, false); !errors.Is(err, refused) || writes != 2 || m.pendingRestart(host.Name) != newIntent {
				t.Fatalf("recovery adopted old binding: err=%v writes=%d pending=%+v", err, writes, m.pendingRestart(host.Name))
			}
		})
	}
}

func TestRecoveryEntryBindingGuardsBothBareRestartMutations(t *testing.T) {
	for _, recovery := range []string{"kill", "launch", "launch-fresh"} {
		t.Run(recovery, func(t *testing.T) {
			boundary := strings.TrimSuffix(recovery, "-fresh")
			valid, inject := true, true
			kills, launches := 0, 0
			started := "2026-01-01T00:00:00Z"
			refused := errors.New("entry changed during bare restart")
			guard := func() error {
				if !valid {
					return refused
				}
				return nil
			}
			fr := deployRunner(t, func(int) ([]byte, error) { return launchContract("original"), nil }, func(int) ([]byte, error) {
				if kills > launches {
					return nil, errors.New("no listener")
				}
				return fmt.Appendf(nil, `{"version":"original","mobile_api_version":1,"hub_addr":"127.0.0.1:9180","started_at":%q}`, started), nil
			})
			run := fr.runFn
			fr.runFn = func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
				switch command := strings.Join(argv, " "); {
				case strings.Contains(command, "list-units"):
					return nil, nil
				case strings.Contains(command, "lsof -ti :9180"):
					if kills > launches {
						return []byte(noListenerMarker), nil
					}
					return []byte("4242\n"), nil
				case strings.Contains(command, "id -un"), strings.Contains(command, pidOwnerRemote("4242")):
					return []byte("dev\n"), nil
				case strings.Contains(command, "ps -p 4242 -ww -o command"):
					return []byte("/opt/evener/bin/evener hub -addr 127.0.0.1:9180\n"), nil
				case strings.Contains(command, "lsof -p 4242 -a -d 1,2"):
					if inject && boundary == "kill" {
						valid = false
					}
					return nil, nil
				case strings.Contains(command, "kill -s 0 -- 4242"):
					return []byte(pidGoneMarker), nil
				case strings.Contains(command, "kill -- 4242"):
					kills++
					if inject && boundary == "launch" {
						valid = false
					}
					return nil, nil
				case strings.Contains(command, "nohup"):
					launches++
					started = "2026-01-02T00:00:00Z"
					return nil, nil
				}
				return run(ctx, argv, stdin)
			}
			host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
			m := newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "later-controller", sleep: func(context.Context, time.Duration) error { return nil }})
			host, _ = m.reg.Get(host.Name)
			recordConnectionOperations(t, m)
			facts, err := m.preflight(t.Context(), host)
			if err != nil {
				t.Fatal(err)
			}
			_, err = m.RestartForOperation(t.Context(), host, facts, guard)
			wantKills := 0
			if boundary == "launch" {
				wantKills = 1
			}
			if !errors.Is(err, refused) || kills != wantKills || launches != 0 {
				t.Fatalf("bare boundary: err=%v kills=%d launches=%d", err, kills, launches)
			}
			if boundary == "launch" {
				pending := m.pendingRestart(host.Name)
				if pending.command == "" || pending.binding == nil || pending.expectedVersion != "original" || pending.replaced.startedAt.Format(time.RFC3339) != "2026-01-01T00:00:00Z" {
					t.Fatalf("stopped predecessor lost recovery: %+v", pending)
				}
				if _, err := m.ensureOnce(t.Context(), host, false); !errors.Is(err, refused) || kills != 1 || launches != 0 || m.pendingRestart(host.Name) != pending {
					t.Fatalf("unauthorized relaunch replay: err=%v kills=%d launches=%d", err, kills, launches)
				}
			} else if m.pendingRestart(host.Name) != (pendingRestartState{}) {
				t.Fatal("refused kill recorded a nonexistent effect")
			}
			inject = false
			if recovery == "launch-fresh" {
				// The old entry stays invalid. Only the new request's validator
				// authorizes replay, without turning replacement into startup.
				result, err := m.restartForOperation(t.Context(), host, facts, func() error { return nil })
				if err != nil || result.TargetVersion != "original" || result.ReplacedStartedAt.Format(time.RFC3339) != "2026-01-01T00:00:00Z" || !result.StartedAt.IsZero() {
					t.Fatalf("renewed replacement proof: result=%+v err=%v", result, err)
				}
			} else {
				valid = true
			}
			if boundary == "kill" {
				if _, err := m.RestartForOperation(t.Context(), host, facts, guard); err != nil {
					t.Fatal(err)
				}
			}
			ch, err := m.ensureOnce(t.Context(), host, false)
			if ch != nil {
				defer func() { _ = ch.Close() }()
			}
			if err != nil || ch == nil || kills != 1 || launches != 1 || started != "2026-01-02T00:00:00Z" || m.pendingRestart(host.Name) != (pendingRestartState{}) {
				t.Fatalf("bare recovery: err=%v kills=%d launches=%d pending=%+v", err, kills, launches, m.pendingRestart(host.Name))
			}
		})
	}
}

func TestRecoveryEntryBindingReleasedAtTeardown(t *testing.T) {
	for _, boundary := range []string{"remove-readd", "update", "detach", "close"} {
		t.Run(boundary, func(t *testing.T) {
			effects, validations := 0, 0
			fr := deployRunner(t, func(int) ([]byte, error) { return launchContract("original"), nil }, func(int) ([]byte, error) {
				return []byte(`{"version":"original","mobile_api_version":1,"hub_addr":"127.0.0.1:9180","started_at":"2026-01-01T00:00:00Z"}`), nil
			})
			run := fr.runFn
			fr.runFn = func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
				if strings.Contains(strings.Join(argv, " "), "systemctl restart") {
					effects++
					return nil, errors.New("interrupted restart")
				}
				return run(ctx, argv, stdin)
			}
			host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
			m := newTestManager(t, testRegistry(t, host), fr, Options{})
			host, _ = m.reg.Get(host.Name)
			facts, err := m.preflight(t.Context(), host)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := m.RestartForOperation(t.Context(), host, facts, func() error { validations++; return nil }); !errors.Is(err, ErrRestart) || effects != 1 || m.pendingRestart(host.Name).binding == nil {
				t.Fatalf("initial effect: effects=%d err=%v pending=%+v", effects, err, m.pendingRestart(host.Name))
			}
			switch boundary {
			case "remove-readd":
				err = m.RemoveHost(host.Name)
				if err == nil {
					host.Generation, host.IncarnationID, host.PresenceEpoch = 0, "", 0
					err = m.AddHost(host)
				}
			case "update":
				host.SSH = "new-registration.example"
				err = m.UpdateHost(host, nil)
			case "detach":
				err = m.DetachHost(host.Name)
			case "close":
				err = m.Close()
			}
			if err != nil || m.pendingRestart(host.Name) != (pendingRestartState{}) {
				t.Fatalf("teardown retained callback: err=%v pending=%+v", err, m.pendingRestart(host.Name))
			}
			before := validations
			ch, err := m.Ensure(t.Context(), host.Name)
			if ch != nil {
				defer func() { _ = ch.Close() }()
			}
			if boundary == "close" {
				if !errors.Is(err, ErrManagerClosed) || ch != nil {
					t.Fatalf("closed manager recovered: channel=%v err=%v", ch, err)
				}
			} else if err != nil || ch == nil {
				t.Fatalf("new lifecycle attach: channel=%v err=%v", ch, err)
			}
			if effects != 1 || validations != before {
				t.Fatalf("new lifecycle inherited old work: effects=%d validations=%d→%d", effects, before, validations)
			}
		})
	}
}
