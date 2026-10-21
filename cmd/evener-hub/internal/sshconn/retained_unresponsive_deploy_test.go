package sshconn

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/internal/hubendpoint"
)

func TestRetainedUnresponsiveDeployment(t *testing.T) {
	for _, mode := range []string{"unavailable", "health-returns", "health-disappears", "generation-missing", "generation-mismatch", "argv-mismatch", "environment-mismatch", "definition-mismatch", "config-mismatch", "root-mismatch", "report-missing", "report-malformed", "report-schema", "generation-at-write", "binding-at-write", "healthy-boot-mismatch", "healthy-build-mismatch", "healthy-sha-mismatch", "healthy-pid-mismatch", "healthy-build-at-write", "image-missing", "image-unsupported"} {
		t.Run(mode, func(t *testing.T) {
			host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
			repair, boundary := false, false
			writes, imageProbes, starts := 0, 0, 0
			fr, selected := deployRunnerForFixture(t, "linux", "amd64", func(int) ([]byte, error) {
				return launchContract("newsha"), nil
			}, func(int) ([]byte, error) {
				return []byte(`{"version":"oldsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180","started_at":"2026-01-01T00:00:00Z"}`), nil
			})
			selected.initialUnavailable = !strings.HasPrefix(mode, "healthy-") && mode != "health-disappears"
			base := fr.runFn
			fr.runFn = func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
				remote := strings.Join(argv[12:], " ")
				if remote == "/proc/4242/exe launch-check --json" {
					imageProbes++
					if repair && mode == "image-missing" {
						return nil, errors.New("mapped original image unavailable")
					}
					if repair && mode == "image-unsupported" {
						return []byte(strings.ReplaceAll(goodLaunchCheck, `"hub_endpoint_schema":1`, `"hub_endpoint_schema":0`)), nil
					}
				}
				if repair {
					switch remote {
					case ownerGenerationRemote("4242"):
						if mode == "generation-missing" {
							return nil, errors.New("process birth unavailable")
						}
						if mode == "generation-mismatch" || boundary && mode == "generation-at-write" {
							return []byte("kernel-boot\n99999\n"), nil
						}
					case hubArgvRemote("4242"):
						if mode == "argv-mismatch" {
							return []byte(host.EvenerPath + "\x00hub\x00--log-file\x00/tmp/changed.log\x00"), nil
						}
					case ownerEnvironmentRemote("4242"):
						if mode == "environment-mismatch" {
							return []byte("HOME=/home/dev\x00XDG_CONFIG_HOME=/home/dev/.config\x00"), nil
						}
					case ownerContext("/home/dev", []string{"HOME=/home/dev", "XDG_CONFIG_HOME=/home/dev/.config"}) + host.EvenerPath + " hub endpoint --json":
						// Explicit XDG_CONFIG_HOME selects the same file, but changes frozen launch intent.
						return base(ctx, rawCommandArgv(Options{}, host, ownerContext("/home/dev", []string{"HOME=/home/dev"})+host.EvenerPath+" hub endpoint --json"), in)
					case serviceDefinitionRemote(supervisor{kind: supervisorSystemd, label: "evener-hub.service"}):
						if mode == "definition-mismatch" {
							return []byte("9999 567\n"), nil
						}
					}
				}
				if strings.Contains(remote, "cat >") {
					writes++
					if !repair {
						return nil, errors.New("interrupted recorded write")
					}
				}
				if strings.HasPrefix(remote, "systemctl restart ") || strings.HasPrefix(remote, "systemctl start ") {
					starts++
				}
				out, err := base(ctx, argv, in)
				if repair && strings.Contains(remote, " hub endpoint --json") {
					switch mode {
					case "report-missing":
						return nil, exitStatus(t, 127)
					case "report-malformed":
						return []byte("invalid endpoint report"), nil
					case "report-schema":
						return []byte(strings.ReplaceAll(string(out), `"schema_version":1`, `"schema_version":2`)), err
					case "config-mismatch", "root-mismatch":
						if strings.HasPrefix(remote, ownerContext("/home/dev", []string{"HOME=/home/dev"})) {
							oldPath, newPath := "/home/dev/.config/evener/hub.toml", "/other/hub.toml"
							if mode == "root-mismatch" {
								oldPath, newPath = "/home/dev/.local/state/evener", "/other/state"
							}
							return []byte(strings.ReplaceAll(string(out), oldPath, newPath)), err
						}
					}
					if strings.HasPrefix(mode, "healthy-") && (mode != "healthy-build-at-write" || boundary) {
						var report hubendpoint.Report
						if err := json.Unmarshal(out, &report); err != nil || report.Running == nil {
							t.Fatalf("healthy fixture report: %s, %v", out, err)
						}
						switch mode {
						case "healthy-boot-mismatch":
							report.Running.BootID = "different-boot"
						case "healthy-build-mismatch", "healthy-build-at-write":
							report.Running.Version = "different-build"
						case "healthy-sha-mismatch":
							report.Running.BackendGitSHA = "different-sha"
						case "healthy-pid-mismatch":
							report.Running.PID++
						}
						return json.Marshal(report)
					}
				}
				return out, err
			}
			refused := errors.New("operation binding changed")
			guard := func() error {
				if boundary && mode == "binding-at-write" {
					return refused
				}
				return nil
			}
			m := newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "newsha", BuildBinary: func(ctx context.Context, goos, goarch, file string) error {
				boundary = repair
				return writeStageBinary(ctx, goos, goarch, file)
			}})
			host, _ = m.reg.Get(host.Name)
			facts, err := m.preflight(t.Context(), host)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := m.DeployForOperation(t.Context(), host, facts, false, guard); err == nil || writes != 1 {
				t.Fatalf("initial interruption: err=%v writes=%d", err, writes)
			}
			retained := m.pendingRestart(host.Name)
			if retained.selected == nil || !retained.deployment || retained.writeAcknowledged || (retained.selected.report.Running == nil) != selected.initialUnavailable || selected.initialUnavailable && imageProbes != 1 {
				t.Fatalf("initial owner/capability evidence not retained: image-probes=%d pending=%+v", imageProbes, retained)
			}
			repair = true
			selected.initialUnavailable = mode != "health-returns" && !strings.HasPrefix(mode, "healthy-")
			if strings.HasPrefix(mode, "image-") {
				// Without a cached original-image witness, the existing capability probe must succeed again.
				m.mu.Lock()
				delete(m.ownerWitnesses, host.Name)
				m.mu.Unlock()
			}
			_, after, err := m.DeployForOperation(t.Context(), host, facts, false, guard)
			if mode == "unavailable" || mode == "health-returns" || mode == "health-disappears" {
				if err != nil || writes != 2 || after.Version != "newsha" || after.HubEndpointSchema != 1 || m.pendingRestart(host.Name).active() {
					t.Fatalf("independently revalidated retry: err=%v writes=%d after=%+v pending=%+v", err, writes, after, m.pendingRestart(host.Name))
				}
			} else if err == nil || writes != 1 || !reflect.DeepEqual(retained, m.pendingRestart(host.Name)) {
				t.Fatalf("missing/mismatched evidence replayed write or lost intent: err=%v writes=%d pending=%+v", err, writes, m.pendingRestart(host.Name))
			}
			if mode == "binding-at-write" && !errors.Is(err, refused) {
				t.Fatalf("lost recorder refusal: %v", err)
			}
			if strings.HasSuffix(mode, "-at-write") && !boundary {
				t.Fatal("write-boundary negative refused before its intended check")
			}
			if err != nil && strings.Contains(err.Error(), "unexpected remote command") {
				t.Fatalf("fixture gap is not evidence refusal: %v", err)
			}
			if starts != 0 || len(fr.recordedStarts()) != 0 {
				t.Fatal("deploy-only retry started or attached a hub")
			}
		})
	}
}
