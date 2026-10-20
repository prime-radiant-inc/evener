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
		opts.BuildBinary = func(ctx context.Context, goos, arch, out string) error {
			effects.builds++
			return writeStageBinary(ctx, goos, arch, out)
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
				return []byte(`unsupported appwire protocol`), errors.New("exit status 1")
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
	for _, mode := range []string{"invalid-request", "empty-protocol", "omitted-protocol", "malformed", "malformed-frame", "eof", "timeout", "authentication", "transport"} {
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
			if ch != nil || err == nil || errors.Is(err, ErrProtocolIncompatible) || m.Attached("alpha") {
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

func TestEnsureLiveProtocolMismatchRecovers(t *testing.T) {
	for _, mode := range []string{"response", "structured", "server"} {
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
							return appwire.ResponseMessage(request.ID, appwire.InitializeResponse{ProtocolVersion: "old-protocol"})
						case "server":
							// Exercise the actual server's structured refusal, with an
							// old client's protocol to produce the opposite mismatch.
							server := appserver.NewServer(appserver.ServerConfig{})
							return server.NewConnection("peer").HandleMessage(t.Context(), appwire.RequestMessage(request.ID, appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: "evener-appwire-v6"}))
						default:
							return appwire.ErrorMessage(request.ID, appwire.WireError{Code: appwire.CodeInvalidRequest, Data: appwire.ErrorData{EvenerErrorInfo: appwire.ErrorUpgradeRequired}})
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
