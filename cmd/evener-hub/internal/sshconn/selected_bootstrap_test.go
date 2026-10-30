package sshconn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// First starts retain the same selected intent as replacements. A failed start
// must retry a stopped owner, or verify an already-started successor, not replay
// an arbitrary saved command or accept a health response on another listener.
func TestEnsureSelectedBootstrapRecovery(t *testing.T) {
	for _, adhoc := range []bool{false, true} {
		for _, version := range []string{"newsha", "host-build"} {
			for _, started := range []bool{false, true} {
				t.Run(fmt.Sprintf("adhoc=%v/version=%s/lost-reply=%v", adhoc, version, started), func(t *testing.T) {
					host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
					starts := 0
					var f *selectedProcessFixture
					fr := &fakeRunner{startFn: goodStartFn(t)}
					fr.runFn = func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
						remote := strings.Join(argv[12:], " ")
						switch remote {
						case "uname -s":
							return []byte("Linux\n"), nil
						case "uname -m":
							return []byte("x86_64\n"), nil
						case "id -u":
							return []byte("1000\n"), nil
						case envProbeScript:
							return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
						case "/opt/evener/bin/evener launch-check --json", "/opt/evener/bin/evener launch-check --protocol evener-appwire-v7 --json":
							return []byte(strings.ReplaceAll(goodLaunchCheck, `"version":"dev"`, `"version":"`+version+`"`)), nil
						case "systemctl start evener-hub.service", "CDPATH='' cd -P -- /home/dev || exit; unset HOME XDG_CONFIG_HOME XDG_STATE_HOME EVENER_HUB_LISTEN_ADDR; export HOME=/home/dev; nohup /opt/evener/bin/evener hub </dev/null >/dev/null 2>&1 &":
							starts++
							if starts == 1 {
								if started {
									f.stopped = false
									f.mutated = true
								}
								return nil, errReplacementFixture
							}
							return nil, nil
						default:
							return nil, fmt.Errorf("undeclared bootstrap command %q", remote)
						}
					}
					f = modelSelectedProcess(t, fr, host.EvenerPath, version, version)
					f.stopped, f.adhoc = true, adhoc
					m := newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "newsha", sleep: func(context.Context, time.Duration) error { return nil }})
					testRestartRecorder(t, m)
					if _, err := m.Ensure(t.Context(), host.Name); !errors.Is(err, errReplacementFixture) {
						t.Fatalf("first Ensure=%v", err)
					}
					pending := m.pendingRestart(host.Name)
					if pending.selected == nil || !pending.start || len(fr.recordedStarts()) != 0 {
						t.Fatalf("first start intent=%+v", pending)
					}
					if _, err := m.Ensure(t.Context(), host.Name); err != nil {
						t.Fatal(err)
					}
					wantStarts := 2
					if started {
						wantStarts = 1
					}
					if starts != wantStarts || m.pendingRestart(host.Name).selected != nil || len(fr.recordedStarts()) != 1 {
						t.Fatalf("starts=%d want=%d pending=%+v", starts, wantStarts, m.pendingRestart(host.Name))
					}
				})
			}
		}
	}
}

func TestEnsureSelectedBootstrapSnapshotPin(t *testing.T) {
	savedChannel, savedSHA := buildinfo.Channel, buildinfo.GitSHA
	t.Cleanup(func() { buildinfo.Channel, buildinfo.GitSHA = savedChannel, savedSHA })
	buildinfo.Channel, buildinfo.GitSHA = "snapshot", "expectedsha"
	for _, sha := range []string{"", "wrongsha", "expectedsha"} {
		t.Run("sha="+sha, func(t *testing.T) {
			host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
			fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
				switch remote := strings.Join(argv[12:], " "); remote {
				case "uname -s":
					return []byte("Linux\n"), nil
				case "uname -m":
					return []byte("x86_64\n"), nil
				case "id -u":
					return []byte("1000\n"), nil
				case envProbeScript:
					return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
				case host.EvenerPath + " launch-check --json", host.EvenerPath + " launch-check --protocol evener-appwire-v7 --json":
					return []byte(strings.ReplaceAll(goodLaunchCheck, `"version":"dev"`, `"version":"newsha"`)), nil
				case "systemctl start evener-hub.service":
					return nil, nil
				default:
					return nil, fmt.Errorf("undeclared snapshot bootstrap command %q", remote)
				}
			}, startFn: goodStartFn(t)}
			f := modelSelectedProcess(t, fr, host.EvenerPath, "newsha", "newsha")
			f.stopped = true
			f.sha = sha
			m := newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "newsha", sleep: func(context.Context, time.Duration) error { return nil }})
			testRestartRecorder(t, m)
			// The controller pin belongs to retained startup intent, not a fresh
			// compatible installation's first start under main's Connect policy.
			m.setPendingStart(host.Name, "systemctl start evener-hub.service", "newsha", "expectedsha", nil)
			_, err := m.Ensure(t.Context(), host.Name)
			if sha != "expectedsha" {
				if !errors.Is(err, ErrRestart) || m.pendingRestart(host.Name).selected == nil || len(fr.recordedStarts()) != 0 {
					t.Fatalf("wrong pin err=%v pending=%+v", err, m.pendingRestart(host.Name))
				}
				f.sha = "expectedsha"
				if _, err = m.Ensure(t.Context(), host.Name); err != nil {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if m.pendingRestart(host.Name).selected != nil || len(fr.recordedStarts()) != 1 {
				t.Fatal("verified bootstrap did not attach once")
			}
		})
	}
}

func TestChannelArgvSeparatesBootstrapListener(t *testing.T) {
	opts := Options{HubListenAddr: "127.0.0.1:9999"}
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	for _, addr := range []string{"", "[::]:0"} {
		host.Addr = addr
		want := opts.HubListenAddr
		if addr != "" {
			want = addr
		}
		if slices.Contains(channelArgv(opts, host), "--addr") {
			t.Fatal("attachment overrides selected discovery")
		}
		if got := hubBootstrapArgv(opts, host, "/opt/evener"); !slices.Equal(got, []string{"/opt/evener", "hub", "--addr", want}) {
			t.Fatalf("bootstrap=%v", got)
		}
	}
}

func TestEnsureFreshBootstrapListener(t *testing.T) {
	for _, hostAddr := range []string{"", "0.0.0.0:0", "[::1]:0"} {
		t.Run(hostAddr, func(t *testing.T) {
			host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener", Addr: hostAddr}
			want := hostAddr
			if want == "" {
				want = "127.0.0.1:0"
			}
			quoted := want
			if strings.HasPrefix(want, "[") {
				quoted = "'" + want + "'"
			}
			launch := "CDPATH='' cd -P -- /home/dev || exit; unset HOME XDG_CONFIG_HOME XDG_STATE_HOME EVENER_HUB_LISTEN_ADDR; export HOME=/home/dev; nohup /opt/evener/bin/evener hub --addr " + quoted + " </dev/null >/dev/null 2>&1 &"
			preflight := cannedRun(nil)
			launched := 0
			fr := &fakeRunner{startFn: goodStartFn(t), runFn: func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
				if strings.Join(argv[12:], " ") == launch {
					launched++
					return nil, nil
				}
				return preflight(ctx, argv, in)
			}}
			f := modelSelectedProcess(t, fr, host.EvenerPath, "dev", "dev")
			f.stopped, f.adhoc, f.launchAddr = true, true, want
			m := newTestManager(t, testRegistry(t, host), fr, Options{HubListenAddr: "127.0.0.1:0"})
			if _, err := m.Ensure(t.Context(), host.Name); err != nil {
				t.Fatal(err)
			}
			starts := fr.recordedStarts()
			if launched != 1 || len(starts) != 1 || slices.Contains(starts[0], "--addr") || m.pendingRestart(host.Name).selected != nil {
				t.Fatalf("launches=%d starts=%v", launched, starts)
			}
		})
	}
}

func TestEnsureStoppedInspectionFailureIsNotAbsence(t *testing.T) {
	for _, output := range []string{"Failed to connect to bus: No medium found", "Failed to connect to bus: No such file or directory", "Failed to connect to bus: Host is down", "Failed to connect to bus: Permission denied", "ssh: Connection reset by peer"} {
		t.Run(output, func(t *testing.T) {
			host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
			preflight := cannedRun(nil)
			fr := &fakeRunner{startFn: goodStartFn(t), runFn: func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
				if strings.Join(argv[12:], " ") == "CDPATH='' cd -P -- /home/dev || exit; unset HOME XDG_CONFIG_HOME XDG_STATE_HOME EVENER_HUB_LISTEN_ADDR; export HOME=/home/dev; nohup /opt/evener/bin/evener hub </dev/null >/dev/null 2>&1 &" {
					return nil, nil
				}
				return preflight(ctx, argv, in)
			}}
			f := modelSelectedProcess(t, fr, host.EvenerPath, "dev", "dev")
			f.stopped, f.adhoc = true, true
			run := fr.runFn
			fr.runFn = func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
				if strings.Join(argv[12:], " ") == systemctlListUnitsUser {
					return []byte(output), errReplacementFixture
				}
				return run(ctx, argv, in)
			}
			m := newTestManager(t, testRegistry(t, host), fr, Options{})
			_, err := m.Ensure(t.Context(), host.Name)
			absent := output == "Failed to connect to bus: No medium found" || output == "Failed to connect to bus: No such file or directory" || output == "Failed to connect to bus: Host is down"
			if absent {
				if err != nil || len(fr.recordedStarts()) != 1 {
					t.Fatalf("absent user bus prevented selected bootstrap: %v", err)
				}
			} else if !errors.Is(err, errReplacementFixture) || len(fr.recordedStarts()) != 0 || m.pendingRestart(host.Name).selected != nil {
				t.Fatalf("failed inspection authorized absence: %v", err)
			}
		})
	}
}

func TestEnsureFreshBootstrapRejectsInvalidListener(t *testing.T) {
	for _, addr := range []string{"127.0.0.1", "http://127.0.0.1:9180", "127.0.0.1:notaport", "127.0.0.1:65536"} {
		t.Run(addr, func(t *testing.T) {
			host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener", Addr: addr}
			fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
			f := modelSelectedProcess(t, fr, host.EvenerPath, "dev", "dev")
			f.stopped, f.adhoc = true, true
			m := newTestManager(t, testRegistry(t, host), fr, Options{})
			if _, err := m.Ensure(t.Context(), host.Name); !errors.Is(err, ErrHostAddr) {
				t.Fatalf("invalid bootstrap listener err=%v", err)
			}
			if m.pendingRestart(host.Name).selected != nil || len(fr.recordedStarts()) != 0 {
				t.Fatal("invalid listener authorized a start or attachment")
			}
		})
	}
}

func TestEnsureStoppedCandidateInspectionFailure(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		t.Run(strconv.FormatBool(malformed), func(t *testing.T) {
			host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
			fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
			f := modelSelectedProcess(t, fr, host.EvenerPath, "dev", "dev")
			f.stopped = true
			run := fr.runFn
			fr.runFn = func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
				if strings.Join(argv[12:], " ") == "CDPATH='' cd -P -- /home/dev || exit; unset HOME XDG_CONFIG_HOME XDG_STATE_HOME EVENER_HUB_LISTEN_ADDR; export HOME=/home/dev; /opt/evener/bin/evener hub endpoint --json" {
					if malformed {
						return []byte("not json"), nil
					}
					return nil, errReplacementFixture
				}
				return run(ctx, argv, in)
			}
			m := newTestManager(t, testRegistry(t, host), fr, Options{})
			if _, err := m.Ensure(t.Context(), host.Name); err == nil {
				t.Fatal("unreadable candidate became fresh absence")
			}
			if m.pendingRestart(host.Name).selected != nil || len(fr.recordedStarts()) != 0 {
				t.Fatal("failed candidate inspection authorized a start or attachment")
			}
		})
	}
}
