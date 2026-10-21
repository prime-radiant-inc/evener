package sshconn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

const stoppedOldProtocol = "evener-appwire-v5"

// stoppedProtocolInstallation models the CLI boundary: an imposed protocol is
// checked before JSON is emitted, while launch-check --json describes the disk
// binary's own contract. A successful push replaces that contract.
type stoppedProtocolInstallation struct {
	stoppedInstallation
	protocol, target     string
	builds, descriptions int
	interruptWrite       bool
	describe             func() ([]byte, error)
}

func newStoppedProtocolInstallation(t *testing.T) (*Manager, *stoppedProtocolInstallation) {
	t.Helper()
	s := &stoppedProtocolInstallation{
		protocol: stoppedOldProtocol,
		target:   "/opt/evener/bin/evener",
	}
	s.stoppedInstallation = stoppedInstallation{version: "old-build", flags: []string{"api-log"}}
	m := s.manager(t, Options{controllerVersionOverride: "controller-build", BuildBinary: func(ctx context.Context, goos, goarch, path string) error {
		s.builds++
		return writeStageBinary(ctx, goos, goarch, path)
	}})
	fr := m.runner.(*fakeRunner)
	run := fr.runFn
	refused, absent := exitStatus(t, 1), exitStatus(t, 127)
	fr.runFn = func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
		if i := slices.Index(argv, "launch-check"); i > 0 {
			if argv[i-1] != s.target {
				if argv[i-1] == "evener" {
					return nil, absent
				}
				t.Fatalf("launch-check addressed %q, installed executable is %q", argv[i-1], s.target)
			}
			switch args := argv[i+1:]; {
			case slices.Equal(args, []string{"--protocol", appwire.ProtocolVersion, "--json"}):
				if s.protocol != appwire.ProtocolVersion {
					// cmd/evener/internal/launchcheck rejects before constructing JSON.
					return fmt.Appendf(nil, "unsupported appwire protocol %q (supported %q)", appwire.ProtocolVersion, s.protocol), refused
				}
			case slices.Equal(args, []string{"--json"}):
				s.descriptions++
				if s.describe != nil {
					return s.describe()
				}
			default:
				t.Fatalf("unsupported launch-check arguments: %v", args)
			}
			return json.Marshal(map[string]any{"hub_endpoint_schema": 1, "protocol": s.protocol, "version": s.version, "launch_flags": s.flags})
		}
		command := strings.Join(argv, " ")
		switch {
		case strings.Contains(command, executableProbeRemote("evener")):
			return nil, refused
		case strings.Contains(command, "[ -f ") && strings.Contains(command, ".local/bin/evener"):
			return []byte(s.target + "\n"), nil
		case strings.Contains(command, "test -d /opt/evener/bin"):
			return nil, nil
		case strings.Contains(command, "evener_resolve"):
			return []byte(s.target + "\n"), nil
		case strings.Contains(command, "cat >"):
			s.deploys++
			if stdin != nil {
				if _, err := io.Copy(io.Discard, stdin); err != nil {
					return nil, err
				}
			}
			if s.interruptWrite {
				return nil, errors.New("transport interrupted before atomic rename")
			}
			s.protocol, s.version = appwire.ProtocolVersion, "controller-build"
			return nil, nil
		default:
			return run(ctx, argv, stdin)
		}
	}
	recordConnectionOperations(t, m)
	return m, s
}

func TestEnsureStoppedProtocolOldRepairsAndStarts(t *testing.T) {
	m, s := newStoppedProtocolInstallation(t)
	ch, err := m.Ensure(t.Context(), "alpha")
	if err != nil || ch == nil {
		t.Fatalf("Connect: channel=%v err=%v builds=%d writes=%d starts=%d", ch, err, s.builds, s.deploys, s.starts)
	}
	if s.builds != 1 || s.deploys != 1 || s.starts != 1 || s.descriptions != 1 || s.protocol != appwire.ProtocolVersion || m.pendingRestart("alpha").active() {
		t.Fatalf("repair: builds=%d writes=%d starts=%d descriptions=%d protocol=%q pending=%+v", s.builds, s.deploys, s.starts, s.descriptions, s.protocol, m.pendingRestart("alpha"))
	}
}

func TestEnsureStoppedProtocolOldRecoversUnlandedWrite(t *testing.T) {
	m, s := newStoppedProtocolInstallation(t)
	host, _ := m.reg.Get("alpha")
	facts, err := m.preflight(t.Context(), host)
	if err != nil {
		t.Fatal(err)
	}
	s.interruptWrite = true
	_, _, err = m.DeployForOperation(t.Context(), host, facts, false, nil)
	if err == nil || s.deploys != 1 || s.starts != 0 || s.protocol != stoppedOldProtocol || !m.pendingRestart("alpha").deployment {
		t.Fatalf("interrupted write: err=%v writes=%d starts=%d protocol=%q pending=%+v", err, s.deploys, s.starts, s.protocol, m.pendingRestart("alpha"))
	}
	s.interruptWrite = false
	ch, err := m.Ensure(t.Context(), "alpha")
	if err != nil || ch == nil || s.builds != 2 || s.deploys != 2 || s.starts != 1 || s.protocol != appwire.ProtocolVersion || m.pendingRestart("alpha").active() {
		t.Fatalf("recovery: channel=%v err=%v builds=%d writes=%d starts=%d protocol=%q pending=%+v", ch, err, s.builds, s.deploys, s.starts, s.protocol, m.pendingRestart("alpha"))
	}
}

func TestEnsureStoppedProtocolSelfDescriptionMustSucceedAndDecode(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		nonzero    bool
		want       error
	}{
		{name: "malformed", body: `{`, want: ErrPreflightDecode},
		{name: "missing-protocol", body: `{"version":"old-build"}`, want: ErrPreflightDecode},
		{name: "missing-version", body: `{"hub_endpoint_schema":1,"protocol":"evener-appwire-v5"}`, want: ErrPreflightDecode},
		{name: "nonzero-contract", body: `{"hub_endpoint_schema":1,"protocol":"evener-appwire-v5","version":"old-build","launch_flags":["api-log"]}`, nonzero: true, want: ErrSSHStart},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, s := newStoppedProtocolInstallation(t)
			var describeErr error
			if tc.nonzero {
				describeErr = exitStatus(t, 1)
			}
			s.describe = func() ([]byte, error) { return []byte(tc.body), describeErr }
			ch, err := m.Ensure(t.Context(), "alpha")
			if !errors.Is(err, tc.want) || ch != nil || s.builds != 0 || s.deploys != 0 || s.starts != 0 || s.descriptions != 1 || m.pendingRestart("alpha").active() {
				t.Fatalf("refusal: channel=%v err=%v builds=%d writes=%d starts=%d descriptions=%d pending=%+v", ch, err, s.builds, s.deploys, s.starts, s.descriptions, m.pendingRestart("alpha"))
			}
		})
	}
}

func TestCollectPreflightStoppedProtocolUsesDiscoveredExecutable(t *testing.T) {
	m, s := newStoppedProtocolInstallation(t)
	s.target = "/home/dev/.local/bin/evener"
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	result, err := m.collectPreflight(t.Context(), host)
	if err != nil || result.launchErr != nil || !result.facts.LaunchCheckKnown || result.facts.ExecutableMissing || result.facts.Protocol != stoppedOldProtocol || result.facts.Version != "old-build" || !slices.Equal(result.facts.LaunchFlags, []string{"api-log"}) {
		t.Fatalf("discovered contract: result=%+v err=%v", result, err)
	}
	if s.descriptions != 1 || m.resolvedTarget(host.Name) != s.target || s.builds != 0 || s.deploys != 0 || s.starts != 0 {
		t.Fatalf("discovery: target=%q descriptions=%d builds=%d writes=%d starts=%d", m.resolvedTarget(host.Name), s.descriptions, s.builds, s.deploys, s.starts)
	}
}

func TestProbeLaunchCheckStillRejectsStoppedProtocolOld(t *testing.T) {
	m, s := newStoppedProtocolInstallation(t)
	host, _ := m.reg.Get("alpha")
	_, err := m.probeStartupLaunchCheck(t.Context(), host)
	if !errors.Is(err, ErrProtocolIncompatible) || s.descriptions != 0 {
		t.Fatalf("strict deployment probe: err=%v descriptions=%d", err, s.descriptions)
	}
}
