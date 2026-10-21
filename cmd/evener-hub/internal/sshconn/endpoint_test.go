package sshconn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/internal/hubendpoint"
)

// Protocol filtering would hide the capability of an upgradeable installed binary.
func TestEndpointCapabilityProbeIsUnfiltered(t *testing.T) {
	host := hostreg.Host{Name: "selected", SSH: "remote", EvenerPath: "/opt/evener"}
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		if got := strings.Join(argv[12:], " "); got != "/opt/evener launch-check --json" {
			return nil, fmt.Errorf("unexpected remote argv: %v", argv)
		}
		return []byte(`{"protocol":"older-protocol","version":"old","launch_flags":["api-log"],"hub_endpoint_schema":1}`), nil
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{})
	got, err := m.probeLaunchCheck(t.Context(), host)
	if err != nil || got.Protocol != "older-protocol" || got.HubEndpointSchema != 1 {
		t.Fatalf("contract=%+v, err=%v", got, err)
	}
}

func remoteEndpointFixture(config, bound, boot string) hubendpoint.Report {
	return hubendpoint.Report{SchemaVersion: 1, State: "running", ConfigPath: config, ConfigExists: true, HubStateRoot: "/srv/state", ConfiguredListenAddr: "0.0.0.0:0", ListenSource: "config", PublicURL: "https://public.example.test", Running: &hubendpoint.Record{SchemaVersion: 1, BootID: boot, PID: 4242, StartedAt: time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC), ConfigPath: config, HubStateRoot: "/srv/state", RequestedListenAddr: "0.0.0.0:0", ListenSource: "config", BoundAddr: bound, LocalURL: "http://" + bound, LoopbackURL: "http://" + bound, Version: "dev"}}
}

func TestEndpointResolverSelectionAndGeneration(t *testing.T) {
	for _, selector := range []string{"", "/srv/custom hub.toml"} {
		t.Run(selector, func(t *testing.T) {
			host := hostreg.Host{Name: "selected", SSH: "remote", ConfigPath: selector, Addr: "127.0.0.1:9999"}
			config := selector
			if config == "" {
				config = "/home/dev/.config/evener/hub.toml"
			}
			calls := 0
			fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
				want := []string{"/opt/selected/evener", "hub", "endpoint", "--json"}
				if selector != "" {
					want = append(want, "--config", "'/srv/custom hub.toml'")
				}
				if !slices.Equal(argv[12:], want) {
					return nil, fmt.Errorf("unexpected remote argv: %v", argv[12:])
				}
				calls++
				if calls == 1 {
					return json.Marshal(remoteEndpointFixture(config, "127.0.0.1:19371", "boot-one"))
				}
				return json.Marshal(remoteEndpointFixture(config, "127.0.0.1:19372", "boot-two"))
			}}
			m := newTestManager(t, testRegistry(t, host), fr, Options{})
			m.setResolvedTarget(host.Name, "/opt/selected/evener")
			facts := Preflight{LaunchCheckKnown: true, HubEndpointSchema: 1, Protocol: "older-protocol"}
			a, err := m.resolveHostEndpoint(t.Context(), host, facts)
			if err != nil {
				t.Fatal(err)
			}
			b, err := m.resolveHostEndpoint(t.Context(), host, facts)
			if err != nil {
				t.Fatal(err)
			}
			if a.Running.LoopbackURL != "http://127.0.0.1:19371" || a.Running.BootID != "boot-one" || b.Running.LoopbackURL != "http://127.0.0.1:19372" || b.Running.BootID != "boot-two" || host.Addr != "127.0.0.1:9999" {
				t.Fatalf("reports=%+v,%+v host=%+v", a, b, host)
			}
		})
	}
}

func TestEndpointResolverRejectsInvalidReport(t *testing.T) {
	for _, kind := range []string{"malformed", "schema", "wrong-selected-config", "wrong-record-config", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			report := remoteEndpointFixture("/srv/hub.toml", "127.0.0.1:19371", "boot-one")
			switch kind {
			case "schema":
				report.SchemaVersion = 2
			case "wrong-selected-config":
				report.ConfigPath = "/other.toml"
				report.Running.ConfigPath = "/other.toml"
			case "wrong-record-config":
				report.Running.ConfigPath = "/other.toml"
			}
			out, _ := json.Marshal(report)
			if kind == "malformed" {
				out = []byte("{")
			}
			if kind == "oversized" {
				out = append(out, []byte(strings.Repeat(" ", 65536))...)
			}
			host := hostreg.Host{Name: "selected", SSH: "remote", ConfigPath: "/srv/hub.toml", Addr: "127.0.0.1:9999"}
			fr := &fakeRunner{runFn: func(context.Context, []string, io.Reader) ([]byte, error) { return out, nil }}
			m := newTestManager(t, testRegistry(t, host), fr, Options{})
			_, err := m.resolveHostEndpoint(t.Context(), host, Preflight{LaunchCheckKnown: true, HubEndpointSchema: 1})
			if !errors.Is(err, ErrPreflightDecode) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestEndpointResolverConfigSpellingCompatibility(t *testing.T) {
	for _, tc := range []struct{ name, selector, report, record string }{
		{"canonical-report-relative-selector-and-record", "hub.toml", "/srv/hub.toml", "hub.toml"},
		{"old-relative-report", "hub.toml", "hub.toml", "hub.toml"},
		{"old-absolute-report", "/srv/hub.toml", "/srv/hub.toml", "/srv/hub.toml"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report := remoteEndpointFixture(tc.report, "127.0.0.1:19371", "boot-one")
			report.Running.ConfigPath = tc.record
			host := hostreg.Host{Name: "selected", SSH: "remote", ConfigPath: tc.selector}
			fr := &fakeRunner{runFn: func(context.Context, []string, io.Reader) ([]byte, error) { return json.Marshal(report) }}
			m := newTestManager(t, testRegistry(t, host), fr, Options{})
			got, err := m.resolveHostEndpoint(t.Context(), host, selectedRestartFacts())
			if err != nil || got.ConfigPath != tc.report || len(fr.recordedRuns()) != 1 {
				t.Fatalf("compatible report rejected or context-free lookup attempted: report=%+v err=%v", got, err)
			}
		})
	}
}

func TestEndpointResolverConfigLookupCancellation(t *testing.T) {
	host := hostreg.Host{Name: "selected", SSH: "remote", ConfigPath: "/srv/alias.toml"}
	report := remoteEndpointFixture("/srv/hub.toml", "127.0.0.1:19371", "boot-one")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		if strings.Join(argv[12:], " ") == resolveDeployCommand(host.ConfigPath) {
			cancel()
			return []byte("/srv/hub.toml\n"), nil
		}
		return json.Marshal(report)
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{})
	_, err := m.resolveHostEndpoint(ctx, host, selectedRestartFacts())
	if !errors.Is(err, context.Canceled) || len(fr.recordedRuns()) != 2 {
		t.Fatalf("cancellation lost during config lookup: commands=%d err=%v", len(fr.recordedRuns()), err)
	}
}

func TestEndpointResolverConfigAliasResolutionCount(t *testing.T) {
	for _, tc := range []struct {
		name, record string
		want         []string
	}{
		{"identical", "/srv/selected-alias.toml", []string{"/srv/selected-alias.toml"}},
		{"distinct", "/srv/running-alias.toml", []string{"/srv/selected-alias.toml", "/srv/running-alias.toml"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := hostreg.Host{Name: "selected", SSH: "remote", ConfigPath: "/srv/selected-alias.toml"}
			report := remoteEndpointFixture("/srv/hub.toml", "127.0.0.1:19371", "boot-one")
			report.Running.ConfigPath = tc.record
			var resolved []string
			fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
				remote := strings.Join(argv[12:], " ")
				for _, alias := range []string{"/srv/selected-alias.toml", "/srv/running-alias.toml"} {
					if remote == resolveDeployCommand(alias) {
						resolved = append(resolved, alias)
						return []byte("/srv/hub.toml\n"), nil
					}
				}
				if remote == "evener hub endpoint --json --config /srv/selected-alias.toml" {
					return json.Marshal(report)
				}
				return nil, fmt.Errorf("unexpected remote command: %s", remote)
			}}
			m := newTestManager(t, testRegistry(t, host), fr, Options{})
			for call := range 2 {
				resolved = nil
				if _, err := m.resolveHostEndpoint(t.Context(), host, selectedRestartFacts()); err != nil {
					t.Fatal(err)
				}
				if !slices.Equal(resolved, tc.want) {
					t.Fatalf("call %d resolved=%v, want %v", call, resolved, tc.want)
				}
			}
		})
	}
}

func TestEndpointResolverCapabilityAndNoLoopback(t *testing.T) {
	host := hostreg.Host{Name: "selected", SSH: "remote"}
	report := remoteEndpointFixture("/srv/hub.toml", "192.0.2.1:19371", "boot-one")
	report.Running.LoopbackURL = ""
	fr := &fakeRunner{runFn: func(context.Context, []string, io.Reader) ([]byte, error) { return json.Marshal(report) }}
	m := newTestManager(t, testRegistry(t, host), fr, Options{})
	for _, schema := range []int{0, 2} {
		_, err := m.resolveHostEndpoint(t.Context(), host, Preflight{LaunchCheckKnown: true, HubEndpointSchema: schema})
		if !errors.Is(err, ErrEndpointMigration) || !isTerminal(err) || len(fr.recordedRuns()) != 0 {
			t.Fatalf("err=%v commands=%v", err, fr.recordedRuns())
		}
	}
	got, err := m.resolveHostEndpoint(t.Context(), host, Preflight{LaunchCheckKnown: true, HubEndpointSchema: 1})
	if err != nil || got.Running == nil || got.Running.LoopbackURL != "" {
		t.Fatalf("report=%+v,err=%v", got, err)
	}
}

func TestEndpointAttachmentDoesNotOverrideListener(t *testing.T) {
	host := hostreg.Host{Name: "selected", SSH: "remote", ConfigPath: "/srv/hub.toml", Addr: "0.0.0.0:0"}
	got := channelArgv(Options{}, host)
	if !slices.Equal(got[12:], []string{"evener", "hub", "attach", "--stdio", "--config", "/srv/hub.toml"}) {
		t.Fatalf("remote argv=%v", got[12:])
	}
	if !slices.Equal(hubBootstrapArgv(Options{}, host, "/opt/evener"), []string{"/opt/evener", "hub", "--config", "/srv/hub.toml", "--addr", "0.0.0.0:0"}) {
		t.Fatal("bootstrap lost listener intent")
	}
}

func TestEndpointInstalledUnsupportedStopsEnsureBeforeMutation(t *testing.T) {
	host := hostreg.Host{Name: "selected", SSH: "remote"}
	fr := &fakeRunner{runFn: cannedRun(map[string][]byte{"launch-check": []byte(`{"protocol":"older-protocol","version":"old","launch_flags":["api-log"]}`)}), startFn: goodStartFn(t)}
	m := newTestManager(t, testRegistry(t, host), fr, Options{BuildBinary: writeStageBinary})
	if ch, err := m.Ensure(t.Context(), host.Name); err != nil || ch == nil {
		t.Fatalf("read-only attach: channel=%v err=%v", ch, err)
	}
	_, err := m.resolveOwnedEndpoint(t.Context(), host, Preflight{LaunchCheckKnown: true})
	if !errors.Is(err, ErrEndpointMigration) || !isTerminal(err) {
		t.Fatalf("err=%v, terminal=%v", err, isTerminal(err))
	}
	for _, argv := range fr.recordedRuns() {
		remote := strings.Join(argv[12:], " ")
		if strings.Contains(remote, "api/health") || strings.Contains(remote, "systemctl") || strings.Contains(remote, "kill") {
			t.Fatalf("unsupported installation reached lifecycle: %v", argv)
		}
	}
	if len(fr.recordedStarts()) != 1 {
		t.Fatal("read-only attachment was not attempted exactly once")
	}
}

func TestEndpointFallbackFailureIsNotAbsence(t *testing.T) {
	host := hostreg.Host{Name: "selected", SSH: "remote"}
	transport := errors.New("transport sentinel")
	for _, tc := range []struct {
		name string
		out  []byte
		err  error
	}{
		{"missing capability", []byte(`{"protocol":"older-protocol","version":"old"}`), nil},
		{"unsupported capability", []byte(`{"protocol":"older-protocol","version":"old","hub_endpoint_schema":2}`), nil},
		{"malformed contract", []byte("{"), nil},
		{"transport", nil, transport},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := cannedRun(nil)
			fr := &fakeRunner{runFn: func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
				joined := strings.Join(argv[12:], " ")
				switch {
				case joined == "evener launch-check --json":
					return nil, exitStatus(t, 127)
				case joined == executableProbeRemote("evener"):
					return nil, exitStatus(t, 1)
				case strings.HasPrefix(joined, `if [ -n "${HOME-}" ]`):
					return []byte("/home/dev/.local/bin/evener\n"), nil
				case strings.HasPrefix(joined, "/home/dev/.local/bin/evener launch-check"):
					return tc.out, tc.err
				default:
					return base(ctx, argv, in)
				}
			}}
			m := newTestManager(t, testRegistry(t, host), fr, Options{BuildBinary: writeStageBinary})
			pf, err := m.preflight(t.Context(), host)
			if pf.ExecutableMissing || err == nil {
				t.Fatalf("facts=%+v,err=%v", pf, err)
			}
			if tc.err != nil && !errors.Is(err, transport) {
				t.Fatalf("transport was hidden: %v", err)
			}
			if tc.err == nil && (!errors.Is(err, ErrEndpointMigration) || !isTerminal(err)) {
				t.Fatalf("installed fallback must require migration: %v", err)
			}
		})
	}
}

func TestEndpointCapabilityCancellationWinsCompletedOutput(t *testing.T) {
	host := hostreg.Host{Name: "selected", SSH: "remote"}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	fr := &fakeRunner{runFn: func(context.Context, []string, io.Reader) ([]byte, error) {
		cancel()
		return []byte(goodLaunchCheck), nil
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{})
	_, err := m.probeLaunchCheck(ctx, host)
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrEndpointMigration) {
		t.Fatalf("err=%v", err)
	}
}

func TestEndpointCapabilityFailureClasses(t *testing.T) {
	host := hostreg.Host{Name: "selected", SSH: "remote"}
	transport := errors.New("transport sentinel")
	for _, tc := range []struct {
		name                      string
		launchErr, probeErr, want error
	}{
		{"installed command unsupported", exitStatus(t, 2), nil, ErrEndpointMigration},
		{"verified absent", exitStatus(t, 127), exitStatus(t, 1), ErrExecutableMissing},
		{"transport", transport, nil, ErrSSHStart},
		{"ssh status", exitStatus(t, 255), nil, ErrSSHStart},
		{"probe transport", exitStatus(t, 2), transport, ErrSSHStart},
		{"cancelled", context.Canceled, nil, context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
				switch strings.Join(argv[12:], " ") {
				case "evener launch-check --json":
					return nil, tc.launchErr
				case "if command -v evener >/dev/null 2>&1; then exit 0; else exit 1; fi":
					return nil, tc.probeErr
				default:
					return nil, fmt.Errorf("unexpected argv: %v", argv)
				}
			}}
			m := newTestManager(t, testRegistry(t, host), fr, Options{})
			_, err := m.probeLaunchCheck(t.Context(), host)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err=%v, want class %v", err, tc.want)
			}
			if errors.Is(tc.want, ErrSSHStart) || errors.Is(tc.want, context.Canceled) {
				if errors.Is(err, ErrEndpointMigration) || errors.Is(err, ErrExecutableMissing) || isTerminal(err) {
					t.Fatalf("transport became terminal: %v", err)
				}
			}
		})
	}
}

func TestEndpointResolverTransportAndCancellation(t *testing.T) {
	host := hostreg.Host{Name: "selected", SSH: "remote"}
	transport := errors.New("endpoint transport sentinel")
	for _, cancelled := range []bool{false, true} {
		t.Run(strconv.FormatBool(cancelled), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			fr := &fakeRunner{runFn: func(context.Context, []string, io.Reader) ([]byte, error) {
				if cancelled {
					cancel()
				}
				return nil, transport
			}}
			m := newTestManager(t, testRegistry(t, host), fr, Options{})
			_, err := m.resolveHostEndpoint(ctx, host, Preflight{LaunchCheckKnown: true, HubEndpointSchema: 1})
			want := transport
			if cancelled {
				want = context.Canceled
			}
			if !errors.Is(err, want) || isTerminal(err) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestEndpointCapabilityRefresh(t *testing.T) {
	host := hostreg.Host{Name: "selected", SSH: "remote"}
	for _, schema := range []int{0, 1, 2} {
		t.Run(strconv.Itoa(schema), func(t *testing.T) {
			fr := &fakeRunner{runFn: func(context.Context, []string, io.Reader) ([]byte, error) {
				return []byte(fmt.Sprintf(`{"protocol":"evener-appwire-v7","version":"new","launch_flags":["api-log"],"hub_endpoint_schema":%d}`, schema)), nil
			}}
			m := newTestManager(t, testRegistry(t, host), fr, Options{})
			got, err := m.refreshLaunchContract(t.Context(), host, Preflight{LaunchCheckKnown: true, HubEndpointSchema: 1, Version: "old"})
			if schema != 1 {
				if !errors.Is(err, ErrEndpointMigration) {
					t.Fatalf("err=%v", err)
				}
				return
			}
			if err != nil || got.HubEndpointSchema != 1 || got.Version != "new" || !got.LaunchCheckKnown {
				t.Fatalf("facts=%+v err=%v", got, err)
			}
		})
	}
}
