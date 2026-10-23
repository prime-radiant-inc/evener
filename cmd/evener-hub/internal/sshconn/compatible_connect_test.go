package sshconn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/internal/appserver"
)

// The recorder and process boundaries both count attempted mutations. A source
// refusal cannot make a deploy decision look like a successful read-only attach.
type connectEffects struct{ builds, writes, records int }

func compatibleConnectManager(t *testing.T, fr *fakeRunner, opts Options) (*Manager, *connectEffects) {
	t.Helper()
	effects := &connectEffects{}
	if opts.BuildBinary != nil {
		build := opts.BuildBinary
		opts.BuildBinary = func(ctx context.Context, goos, arch, out string) error {
			effects.builds++
			return build(ctx, goos, arch, out)
		}
	}
	run := fr.runFn
	fr.runFn = func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
		command := strings.Join(argv, " ")
		if strings.Contains(command, "cat >") || strings.Contains(command, "systemctl restart") || strings.Contains(command, "nohup") || strings.Contains(command, "install.sh") || strings.Contains(command, "kill -") {
			effects.writes++
		}
		return run(ctx, argv, stdin)
	}
	m := newTestManager(t, testRegistry(t, hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}), fr, opts)
	hook := func(hostreg.Host) (func(error), error) {
		effects.records++
		return func(error) {}, nil
	}
	m.SetEnsureDeployHook(hook)
	m.SetEnsureRestartHook(hook)
	return m, effects
}

func requireReadOnlyConnect(t *testing.T, m *Manager, effects *connectEffects) {
	t.Helper()
	ch, err := m.Ensure(t.Context(), "alpha")
	if err != nil || ch == nil {
		t.Fatalf("attach: channel=%v err=%v effects=%+v", ch, err, effects)
	}
	if *effects != (connectEffects{}) {
		t.Fatalf("compatible Connect mutated the host: %+v", effects)
	}
	again, err := m.Ensure(t.Context(), "alpha")
	if err != nil || again != ch {
		t.Fatalf("repeat Connect replaced channel: %v, %v", again, err)
	}
}

func TestEnsureCompatibleBuildDifferenceDoesNotDeploy(t *testing.T) {
	for _, version := range []string{"controller-clean", "dev", "abc123-dirty"} {
		for _, source := range []string{"configured", "unavailable", "disabled", "cross-platform"} {
			t.Run(version+"/"+source, func(t *testing.T) {
				opts := Options{controllerVersionOverride: version, BuildBinary: writeStageBinary}
				overrides := map[string][]byte{"launch-check": []byte(`{"protocol":"` + appwire.ProtocolVersion + `","version":"remote-build","launch_flags":["api-log"]}`)}
				switch source {
				case "unavailable":
					opts.BuildBinary = nil
				case "disabled":
					opts.DeployDisabled = true
				case "cross-platform":
					opts.OwnExecutable = true
					overrides["uname -s"], overrides["uname -m"] = []byte("Darwin"), []byte("arm64")
				}
				fr := &fakeRunner{runFn: cannedRun(overrides), startFn: goodStartFn(t)}
				m, effects := compatibleConnectManager(t, fr, opts)
				requireReadOnlyConnect(t, m, effects)
			})
		}
	}
}

func TestEnsureCompatibleLiveHubWinsOverDiskFacts(t *testing.T) {
	for _, launch := range []string{
		`{"protocol":"disk-other","version":"disk-build","launch_flags":["api-log"]}`,
		`{"protocol":"` + appwire.ProtocolVersion + `","version":"disk-build","launch_flags":[]}`,
		`malformed-contract`,
	} {
		t.Run(launch, func(t *testing.T) {
			fr := &fakeRunner{runFn: cannedRun(map[string][]byte{"launch-check": []byte(launch)}), startFn: goodStartFn(t)}
			m, effects := compatibleConnectManager(t, fr, Options{DeployDisabled: true})
			requireReadOnlyConnect(t, m, effects)
		})
	}
}

func TestEnsureCompatibleDiscoveredBridgeSurvivesRejectedLaunchCheck(t *testing.T) {
	const target = "/home/dev/.local/bin/evener"
	absent := exitStatus(t, 1)
	fr := &fakeRunner{startFn: goodStartFn(t)}
	fr.runFn = func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
		command := strings.Join(argv, " ")
		switch {
		case strings.Contains(command, "launch-check"):
			if strings.Contains(command, target) {
				return []byte(`unsupported appwire protocol`), exitStatus(t, 1)
			}
			return nil, absent
		case strings.Contains(command, "command -v evener"):
			return nil, absent
		case strings.Contains(command, "/.local/bin/evener"):
			return []byte(target + "\n"), nil
		default:
			return cannedRun(nil)(ctx, argv, stdin)
		}
	}
	m, effects := compatibleConnectManager(t, fr, Options{DeployDisabled: true})
	host, _ := m.reg.Get("alpha")
	host.EvenerPath = ""
	if err := m.UpdateHost(host, nil); err != nil {
		t.Fatal(err)
	}
	requireReadOnlyConnect(t, m, effects)
	starts := fr.recordedStarts()
	if len(starts) != 1 || !strings.Contains(strings.Join(starts[0], " "), target) {
		t.Fatalf("bridge did not use discovered executable: %v", starts)
	}
}

func TestEnsureAmbiguousInitializeFailureDoesNotDeploy(t *testing.T) {
	for _, mode := range []string{"invalid-request", "response-malformed", "empty-protocol", "omitted-protocol", "malformed", "malformed-frame", "eof", "timeout", "authentication", "transport"} {
		t.Run(mode, func(t *testing.T) {
			fr := &fakeRunner{runFn: cannedRun(nil)}
			fr.startFn = func(_ context.Context, _ []string, stderr io.Writer) (Stdio, error) {
				switch mode {
				case "timeout":
					return newSilentBridge().stdio, nil
				case "authentication":
					_, _ = io.WriteString(stderr, "Permission denied (publickey).")
					return nil, errors.New("spawn refused")
				case "transport":
					return nil, errors.New("connection reset")
				case "malformed-frame":
					inR, inW := io.Pipe()
					outR, outW := io.Pipe()
					s := &fakeStdio{inW: inW, outR: outR, inR: inR, outW: outW, waitDone: make(chan struct{})}
					go func() { _, _ = io.Copy(io.Discard, inR) }()
					go func() {
						_, _ = io.WriteString(outW, "Content-Length: 1\r\n\r\n{")
						_ = outW.Close()
					}()
					return s, nil
				}
				return initializeReplyBridge(func(request appwire.Request) appwire.Message {
					switch mode {
					case "invalid-request":
						return appwire.ErrorMessage(request.ID, appwire.InvalidRequest("unsupported appwire protocol"))
					case "response-malformed":
						// A successful handshake that names no usable protocol is
						// a malformed response, not proof of an older host.
						return appwire.ResponseMessage(request.ID, appwire.InitializeResponse{ProtocolVersion: "old-protocol"})
					case "empty-protocol":
						return appwire.ResponseMessage(request.ID, map[string]any{"protocolVersion": ""})
					case "omitted-protocol":
						return appwire.ResponseMessage(request.ID, map[string]any{})
					case "malformed":
						return appwire.ResponseMessage(request.ID, []int{1})
					default:
						return appwire.Message{}
					}
				}), nil
			}
			m, effects := compatibleConnectManager(t, fr, Options{BuildBinary: writeStageBinary, initializeTimeout: 20 * time.Millisecond})
			ch, err := m.Ensure(t.Context(), "alpha")
			if ch != nil || err == nil || errors.Is(err, ErrProtocolIncompatible) || errors.Is(err, ErrHostProtocolNewer) || m.Attached("alpha") {
				t.Fatalf("ambiguous response: channel=%v err=%v", ch, err)
			}
			if *effects != (connectEffects{}) {
				t.Fatalf("ambiguous failure mutated the host: %+v", effects)
			}
			if len(fr.recordedStarts()) != 1 {
				t.Fatalf("bridge attempts=%d, want one", len(fr.recordedStarts()))
			}
		})
	}
}

// legacyIncompatibleRefusal is a captured pre-v7 server's initialize refusal:
// a frozen peer's wire text, not a copy of this controller's server copy.
func legacyIncompatibleRefusal(wanted string) appwire.WireError {
	return appwire.InvalidRequest(
		`protocol version "` + appwire.ProtocolVersion + `" is incompatible; want "` + wanted + `"`)
}

func TestEnsureOlderProtocolHostRecovers(t *testing.T) {
	for _, mode := range []string{"response", "legacy"} {
		for _, source := range []string{"configured", "disabled", "unavailable"} {
			t.Run(mode+"/"+source, func(t *testing.T) {
				fr := deployRunner(t, func(int) ([]byte, error) { return launchContract("controller-build"), nil }, func(call int) ([]byte, error) {
					started := "2026-01-01T00:00:00Z"
					if call > 0 {
						started = "2026-01-02T00:00:00Z"
					}
					return []byte(fmt.Sprintf(`{"version":"controller-build","mobile_api_version":1,"hub_addr":"127.0.0.1:9180","started_at":%q}`, started)), nil
				})
				attempts := 0
				fr.startFn = func(context.Context, []string, io.Writer) (Stdio, error) {
					attempts++
					if attempts > 1 {
						return newFakeBridge(appwire.ProtocolVersion).stdio, nil
					}
					return initializeReplyBridge(func(request appwire.Request) appwire.Message {
						switch mode {
						case "response":
							return appwire.ResponseMessage(request.ID, appwire.InitializeResponse{ProtocolVersion: "evener-appwire-v5"})
						default:
							return appwire.ErrorMessage(request.ID, legacyIncompatibleRefusal("evener-appwire-v5"))
						}
					}), nil
				}
				opts := Options{controllerVersionOverride: "controller-build", BuildBinary: writeStageBinary}
				switch source {
				case "disabled":
					opts.DeployDisabled = true
				case "unavailable":
					opts.BuildBinary = nil
				}
				m, effects := compatibleConnectManager(t, fr, opts)
				ch, err := m.Ensure(t.Context(), "alpha")
				if source != "configured" {
					if ch != nil || !errors.Is(err, ErrProtocolIncompatible) || *effects != (connectEffects{}) {
						t.Fatalf("unrecoverable mismatch: ch=%v err=%v effects=%+v", ch, err, effects)
					}
					return
				}
				if err != nil || ch == nil || attempts != 2 || effects.builds != 1 || effects.records != 1 || effects.writes != 2 {
					t.Fatalf("recovery: ch=%v err=%v attempts=%d effects=%+v", ch, err, attempts, effects)
				}
			})
		}
	}
}

func TestEnsureNewerProtocolHostIsNotDowngraded(t *testing.T) {
	for _, mode := range []string{"structured", "server", "response", "legacy"} {
		for _, source := range []string{"configured", "disabled", "unavailable"} {
			t.Run(mode+"/"+source, func(t *testing.T) {
				fr := deployRunner(t, func(int) ([]byte, error) { return launchContract("controller-build"), nil }, func(call int) ([]byte, error) {
					started := "2026-01-01T00:00:00Z"
					if call > 0 {
						started = "2026-01-02T00:00:00Z"
					}
					return []byte(fmt.Sprintf(`{"version":"controller-build","mobile_api_version":1,"hub_addr":"127.0.0.1:9180","started_at":%q}`, started)), nil
				})
				attempts := 0
				fr.startFn = func(context.Context, []string, io.Writer) (Stdio, error) {
					attempts++
					return initializeReplyBridge(func(request appwire.Request) appwire.Message {
						switch mode {
						case "structured":
							return appwire.ErrorMessage(request.ID, appwire.WireError{Code: appwire.CodeInvalidRequest, Data: appwire.ErrorData{EvenerErrorInfo: appwire.ErrorUpgradeRequired}})
						case "server":
							// The actual server's structured refusal to an older
							// client is what a newer host sends this controller.
							server := appserver.NewServer(appserver.ServerConfig{})
							return server.NewConnection("peer").HandleMessage(t.Context(), appwire.RequestMessage(request.ID, appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: "evener-appwire-v6"}))
						case "response":
							return appwire.ResponseMessage(request.ID, appwire.InitializeResponse{ProtocolVersion: "evener-appwire-v99"})
						default:
							return appwire.ErrorMessage(request.ID, legacyIncompatibleRefusal("evener-appwire-v99"))
						}
					}), nil
				}
				opts := Options{controllerVersionOverride: "controller-build", BuildBinary: writeStageBinary}
				switch source {
				case "disabled":
					opts.DeployDisabled = true
				case "unavailable":
					opts.BuildBinary = nil
				}
				m, effects := compatibleConnectManager(t, fr, opts)
				ch, err := m.Ensure(t.Context(), "alpha")
				if ch != nil || !errors.Is(err, ErrHostProtocolNewer) || !isTerminal(err) || *effects != (connectEffects{}) || attempts != 1 || m.Attached("alpha") {
					t.Fatalf("newer host: ch=%v err=%v terminal=%v attempts=%d effects=%+v", ch, err, isTerminal(err), attempts, effects)
				}
			})
		}
	}
}

func TestEnsureAncientDiskAndRunningHubUpgrades(t *testing.T) {
	diskProtocol, diskVersion := "evener-appwire-v5", "old-build"
	diskSchema := 0
	runningVersion := "old-build"
	fr := deployRunner(t, func(int) ([]byte, error) {
		return []byte(fmt.Sprintf(`{"hub_endpoint_schema":%d,"protocol":%q,"version":%q,"launch_flags":["api-log"]}`, diskSchema, diskProtocol, diskVersion)), nil
	}, func(call int) ([]byte, error) {
		started := "2026-01-01T00:00:00Z"
		if call > 0 {
			started = "2026-01-02T00:00:00Z"
		}
		return []byte(fmt.Sprintf(`{"version":%q,"mobile_api_version":1,"hub_addr":"127.0.0.1:9180","started_at":%q}`, runningVersion, started)), nil
	})
	run := fr.runFn
	fr.runFn = func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
		out, err := run(ctx, argv, stdin)
		if err == nil {
			joined := strings.Join(argv, " ")
			switch {
			case strings.Contains(joined, "cat >"):
				diskProtocol, diskVersion, diskSchema = appwire.ProtocolVersion, "controller-build", 1
			case strings.Contains(joined, "systemctl restart"):
				runningVersion = "controller-build"
			}
		}
		return out, err
	}
	attempts := 0
	fr.startFn = func(context.Context, []string, io.Writer) (Stdio, error) {
		attempts++
		if attempts > 1 {
			return newFakeBridge(appwire.ProtocolVersion).stdio, nil
		}
		return initializeReplyBridge(func(request appwire.Request) appwire.Message {
			return appwire.ErrorMessage(request.ID, legacyIncompatibleRefusal("evener-appwire-v5"))
		}), nil
	}
	m, effects := compatibleConnectManager(t, fr, Options{controllerVersionOverride: "controller-build", BuildBinary: writeStageBinary})
	ch, err := m.Ensure(t.Context(), "alpha")
	if err != nil || ch == nil || attempts != 2 || effects.builds != 1 || effects.records != 1 || effects.writes != 2 {
		t.Fatalf("ancient host upgrade: ch=%v err=%v attempts=%d effects=%+v disk=%s/%s running=%s",
			ch, err, attempts, effects, diskProtocol, diskVersion, runningVersion)
	}
	if diskProtocol != appwire.ProtocolVersion || diskVersion != "controller-build" || runningVersion != "controller-build" {
		t.Fatalf("fixture did not model the upgrade: disk=%s/%s running=%s", diskProtocol, diskVersion, runningVersion)
	}
}

func TestEnsureLiveOlderHubWithNewerDiskRefuses(t *testing.T) {
	fr := deployRunner(t, func(int) ([]byte, error) {
		return []byte(`{"hub_endpoint_schema":1,"protocol":"evener-appwire-v99","version":"newer-build","launch_flags":["api-log"]}`), nil
	}, func(int) ([]byte, error) {
		return []byte(`{"version":"old-running","mobile_api_version":1,"hub_addr":"127.0.0.1:9180","started_at":"2026-01-01T00:00:00Z"}`), nil
	})
	attempts := 0
	fr.startFn = func(context.Context, []string, io.Writer) (Stdio, error) {
		attempts++
		return initializeReplyBridge(func(request appwire.Request) appwire.Message {
			return appwire.ErrorMessage(request.ID, legacyIncompatibleRefusal("evener-appwire-v5"))
		}), nil
	}
	m, effects := compatibleConnectManager(t, fr, Options{controllerVersionOverride: "controller-build", BuildBinary: writeStageBinary})
	ch, err := m.Ensure(t.Context(), "alpha")
	if ch != nil || !errors.Is(err, ErrHostProtocolNewer) || !isTerminal(err) || *effects != (connectEffects{}) || attempts != 1 || m.Attached("alpha") {
		t.Fatalf("live older hub with newer disk: ch=%v err=%v terminal=%v attempts=%d effects=%+v", ch, err, isTerminal(err), attempts, effects)
	}
}

func TestDeployRevalidatesInstalledProtocolBeforeWriting(t *testing.T) {
	diskProtocol := appwire.ProtocolVersion
	fr := deployRunner(t, func(int) ([]byte, error) {
		return []byte(fmt.Sprintf(`{"hub_endpoint_schema":1,"protocol":%q,"version":"controller-build","launch_flags":["api-log"]}`, diskProtocol)), nil
	}, func(int) ([]byte, error) {
		return []byte(`{"version":"old-running","mobile_api_version":1,"hub_addr":"127.0.0.1:9180","started_at":"2026-01-01T00:00:00Z"}`), nil
	})
	attempts := 0
	fr.startFn = func(context.Context, []string, io.Writer) (Stdio, error) {
		attempts++
		if attempts > 1 {
			return newFakeBridge(appwire.ProtocolVersion).stdio, nil
		}
		return initializeReplyBridge(func(request appwire.Request) appwire.Message {
			return appwire.ErrorMessage(request.ID, legacyIncompatibleRefusal("evener-appwire-v5"))
		}), nil
	}
	m, effects := compatibleConnectManager(t, fr, Options{
		controllerVersionOverride: "controller-build",
		BuildBinary: func(ctx context.Context, goos, goarch, out string) error {
			// Another controller installs a newer build while this one stages.
			diskProtocol = "evener-appwire-v99"
			return writeStageBinary(ctx, goos, goarch, out)
		},
	})
	ch, err := m.Ensure(t.Context(), "alpha")
	if ch != nil || !errors.Is(err, ErrHostProtocolNewer) || !isTerminal(err) || effects.writes != 0 || effects.builds != 1 {
		t.Fatalf("pre-write revalidation: ch=%v err=%v terminal=%v effects=%+v", ch, err, isTerminal(err), effects)
	}
}

func TestPendingDeploymentReplayFollowsProtocolDirection(t *testing.T) {
	for _, tc := range []struct {
		name     string
		protocol string
		version  string
		want     bool
	}{
		{"older", "evener-appwire-v5", "build", true},
		{"older-different-version", "evener-appwire-v5", "older-build", true},
		{"unparsable", "incompatible-protocol", "build", true},
		{"current", appwire.ProtocolVersion, "build", false},
		{"newer", "evener-appwire-v99", "build", false},
		{"newer-different-version", "evener-appwire-v99", "newer-build", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pending := pendingRestartState{deployment: true, restart: true, writeAcknowledged: true, expectedVersion: "build"}
			facts := Preflight{LaunchCheckKnown: true, Protocol: tc.protocol, Version: tc.version}
			if got := pending.needsDeployment(facts); got != tc.want {
				t.Fatalf("needsDeployment(%s, %s) = %v, want %v", tc.protocol, tc.version, got, tc.want)
			}
		})
	}
}

func TestPendingDeploymentDoesNotOverwriteNewerDisk(t *testing.T) {
	diskProtocol, diskVersion := "evener-appwire-v99", "newer-build"
	fr := deployRunner(t, func(int) ([]byte, error) {
		return []byte(fmt.Sprintf(`{"hub_endpoint_schema":1,"protocol":%q,"version":%q,"launch_flags":["api-log"]}`, diskProtocol, diskVersion)), nil
	}, func(int) ([]byte, error) {
		return nil, errors.New("no listener")
	})
	run := fr.runFn
	fr.runFn = func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
		if strings.Contains(strings.Join(argv, " "), "lsof -ti :9180") {
			return []byte(noListenerMarker), nil
		}
		return run(ctx, argv, stdin)
	}
	m, effects := compatibleConnectManager(t, fr, Options{controllerVersionOverride: "controller-build", BuildBinary: writeStageBinary})
	host, _ := m.reg.Get("alpha")
	m.mu.Lock()
	m.pendingRestarts[host.Name] = pendingRestartState{
		deployment: true, restart: true, writeAcknowledged: true, expectedVersion: "controller-build",
	}
	m.mu.Unlock()

	ch, err := m.ensureOnce(t.Context(), host, false)
	if ch != nil || !errors.Is(err, ErrHostProtocolNewer) || !isTerminal(err) || *effects != (connectEffects{}) ||
		!m.pendingRestart(host.Name).active() || diskProtocol != "evener-appwire-v99" || diskVersion != "newer-build" {
		t.Fatalf("newer pending disk: ch=%v err=%v terminal=%v effects=%+v pending=%+v disk=%s/%s",
			ch, err, isTerminal(err), effects, m.pendingRestart(host.Name), diskProtocol, diskVersion)
	}
}

func initializeReplyBridge(reply func(appwire.Request) appwire.Message) *fakeStdio {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	s := &fakeStdio{inW: inW, outR: outR, inR: inR, outW: outW, waitDone: make(chan struct{})}
	go func() {
		defer outW.Close()
		transport := appwire.NewStreamTransport(&stdioReadWriter{in: outW, out: inR})
		for {
			message, err := transport.Recv(context.Background())
			if err != nil {
				return
			}
			if message.Request != nil {
				response := reply(*message.Request)
				if response.Response == nil && response.Error == nil {
					return
				}
				if err := transport.Send(context.Background(), response); err != nil {
					return
				}
			}
		}
	}()
	return s
}

func launchContract(version string) []byte {
	return []byte(launchCheckJSON(version))
}
