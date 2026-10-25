package sshconn

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

func TestIncompatibleDeployRequiresRealEndpointEvidence(t *testing.T) {
	for _, mode := range []string{"valid", "missing-command", "command-failed", "malformed", "missing-schema", "wrong-schema", "write-boundary-change", "ordinary-deploy"} {
		t.Run(mode, func(t *testing.T) {
			host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
			fr := deployRunner(t, func(call int) ([]byte, error) {
				if call == 0 {
					return []byte("not json"), nil
				}
				return []byte(`{"hub_endpoint_schema":1,"protocol":"evener-appwire-v7","version":"newsha","launch_flags":["api-log"]}`), nil
			}, func(int) ([]byte, error) {
				return []byte(`{"version":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
			})
			requireProtocolUpgrade(t, fr)
			base := fr.runFn
			reads, writes, lifecycle := 0, 0, 0
			fr.runFn = func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
				remote := strings.Join(argv[12:], " ")
				if strings.Contains(remote, "cat >") {
					writes++
				}
				if strings.Contains(remote, "systemctl restart") || strings.Contains(remote, "systemctl start") || strings.Contains(remote, "kill ") || strings.Contains(remote, "nohup ") {
					lifecycle++
				}
				if remote == host.EvenerPath+" hub endpoint --json" {
					reads++
					switch mode {
					case "missing-command":
						return []byte("unknown command: endpoint"), &RunError{Err: exitStatus(t, 127)}
					case "command-failed":
						return nil, &RunError{Err: exitStatus(t, 2)}
					case "malformed":
						return []byte("not a report"), nil
					case "missing-schema", "wrong-schema":
						out, err := base(ctx, argv, in)
						version := `"schema_version":0`
						if mode == "wrong-schema" {
							version = `"schema_version":2`
						}
						return []byte(strings.ReplaceAll(string(out), `"schema_version":1`, version)), err
					case "write-boundary-change":
						if reads > 1 {
							out, err := base(ctx, argv, in)
							return []byte(strings.ReplaceAll(string(out), "/home/dev/.local/state/evener", "/srv/other-state")), err
						}
					}
				}
				return base(ctx, argv, in)
			}
			m := newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "newsha", BuildBinary: writeStageBinary})
			testDeployRecorder(t, m)
			testRestartRecorder(t, m)
			if mode == "ordinary-deploy" {
				_, err := m.deploySelectedWithAuthority(t.Context(), host, Preflight{OS: "linux"}, false)
				if !errors.Is(err, ErrEndpointMigration) || !isTerminal(err) || reads != 0 || writes != 0 || lifecycle != 0 {
					t.Fatalf("ordinary deploy: err=%v reads=%d writes=%d lifecycle=%d", err, reads, writes, lifecycle)
				}
				return
			}
			ch, err := m.Ensure(t.Context(), host.Name)
			if mode == "valid" {
				if err != nil || ch == nil || ch.Preflight().HubEndpointSchema != 1 || reads < 2 || writes != 1 || lifecycle != 1 {
					t.Fatalf("upgrade: channel=%v err=%v reads=%d writes=%d lifecycle=%d", ch, err, reads, writes, lifecycle)
				}
				return
			}
			if mode == "write-boundary-change" {
				if err == nil || reads < 2 {
					t.Fatalf("changed owner was not rejected: err=%v reads=%d", err, reads)
				}
			} else {
				want, terminal := ErrEndpointMigration, true
				switch mode {
				case "missing-command", "command-failed":
					want, terminal = ErrSSHStart, false
				case "malformed":
					want = ErrPreflightDecode
				}
				if !errors.Is(err, want) || isTerminal(err) != terminal || errors.Is(err, ErrEndpointMigration) != errors.Is(want, ErrEndpointMigration) || reads != 1 {
					t.Fatalf("bad report: err=%v reads=%d", err, reads)
				}
			}
			if ch != nil || writes != 0 || lifecycle != 0 || len(fr.recordedStarts()) != 1 || m.pendingRestart(host.Name).active() {
				t.Fatalf("refusal effects: channel=%v writes=%d lifecycle=%d bridges=%d pending=%+v", ch, writes, lifecycle, len(fr.recordedStarts()), m.pendingRestart(host.Name))
			}
		})
	}
}
