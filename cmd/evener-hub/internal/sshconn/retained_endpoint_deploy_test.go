package sshconn

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

func TestRetainedDeployRepairRequiresRealEndpointEvidence(t *testing.T) {
	for _, mode := range []string{"valid", "missing-command", "command-failed", "malformed", "missing-schema", "wrong-schema", "owner-at-write", "generation-at-write", "binding-at-write"} {
		t.Run(mode, func(t *testing.T) {
			host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
			corrupt, repair, boundary := false, false, false
			writes, reads := 0, 0
			refused := errors.New("recorded entry changed")
			guard := func() error {
				if boundary && mode == "binding-at-write" {
					return refused
				}
				return nil
			}
			fr := deployRunner(t, func(int) ([]byte, error) {
				if corrupt {
					return []byte("not json"), nil
				}
				return launchContract("newsha"), nil
			}, func(int) ([]byte, error) {
				return []byte(`{"version":"oldsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180","started_at":"2026-01-01T00:00:00Z"}`), nil
			})
			base := fr.runFn
			fr.runFn = func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
				remote := strings.Join(argv[12:], " ")
				if strings.Contains(remote, "cat >") {
					writes++
					if !repair {
						return nil, errors.New("interrupted recorded write")
					}
					corrupt = false
				}
				if repair && strings.Contains(remote, " hub endpoint --json") {
					reads++
					switch mode {
					case "missing-command":
						return nil, &RunError{Err: exitStatus(t, 127)}
					case "command-failed":
						return nil, &RunError{Err: exitStatus(t, 2)}
					case "malformed":
						return []byte("not a report"), nil
					case "missing-schema", "wrong-schema":
						out, err := base(ctx, argv, in)
						schema := `"schema_version":0`
						if mode == "wrong-schema" {
							schema = `"schema_version":2`
						}
						return []byte(strings.ReplaceAll(string(out), `"schema_version":1`, schema)), err
					case "owner-at-write":
						if boundary {
							out, err := base(ctx, argv, in)
							return []byte(strings.ReplaceAll(string(out), "/home/dev/.local/state/evener", "/srv/changed-state")), err
						}
					}
				}
				out, err := base(ctx, argv, in)
				if repair && boundary && mode == "generation-at-write" && strings.Contains(remote, "cat /proc/sys/kernel/random/boot_id") {
					return []byte("changed-kernel-boot\n99999\n"), nil
				}
				return out, err
			}
			m := newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "newsha", BuildBinary: func(ctx context.Context, goos, goarch, path string) error {
				if repair {
					boundary = true
				}
				return writeStageBinary(ctx, goos, goarch, path)
			}})
			host, _ = m.reg.Get(host.Name)
			facts, err := m.preflight(t.Context(), host)
			if err != nil {
				t.Fatal(err)
			}
			intent := m.deploymentIntent(false, hubIdentity{})
			intent.binding = newEffectBinding(host, guard)
			if _, _, err := m.deployForOperation(t.Context(), host, facts, intent); err == nil {
				t.Fatal("interrupted write succeeded")
			}
			retained := m.pendingRestart(host.Name)
			if !retained.deployment || retained.selected == nil || writes != 1 {
				t.Fatalf("missing recorded owner: writes=%d pending=%+v", writes, retained)
			}
			corrupt, repair = true, true
			_, after, err := m.deployForOperation(t.Context(), host, Preflight{Host: host.Name, OS: "linux", Arch: "amd64", Home: "/home/dev", UID: "1000"}, retained)
			if mode == "valid" {
				if err != nil || writes != 2 || reads < 2 || after.HubEndpointSchema != 1 || m.pendingRestart(host.Name).active() {
					t.Fatalf("repair: err=%v writes=%d reads=%d facts=%+v pending=%+v", err, writes, reads, after, m.pendingRestart(host.Name))
				}
			} else {
				if err == nil || writes != 1 || !reflect.DeepEqual(m.pendingRestart(host.Name), retained) {
					t.Fatalf("refusal lost retained intent: err=%v writes=%d reads=%d pending=%+v", err, writes, reads, m.pendingRestart(host.Name))
				}
				if !strings.HasSuffix(mode, "-at-write") && (!errors.Is(err, ErrEndpointMigration) || !isTerminal(err)) {
					t.Fatalf("report failure is not terminal migration: %v", err)
				}
				if mode == "binding-at-write" && !errors.Is(err, refused) {
					t.Fatalf("lost binding failure: %v", err)
				}
			}
			if len(fr.recordedStarts()) != 0 {
				t.Fatal("deployment repair attached a bridge")
			}
		})
	}
}

func TestRetainedTargetReinstallEvidence(t *testing.T) {
	for _, mode := range []string{"live", "live-argv-mismatch", "live-process-mismatch", "live-config-mismatch", "live-report-missing", "live-report-malformed", "live-report-schema", "stopped", "stopped-config-mismatch", "stopped-report-missing", "stopped-report-malformed", "stopped-report-schema", "target-mismatch", "write-failed", "post-write-capability", "generation-at-write", "binding-at-write"} {
		t.Run(mode, func(t *testing.T) {
			host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
			repair, boundary := false, false
			writes, procReads, postReads, starts, missingResolutions := 0, 0, 0, 0, 0
			var selected *selectedProcessFixture
			fr, selected := deployRunnerForFixture(t, "linux", "amd64", func(int) ([]byte, error) {
				if repair && !selected.executableAbsent && mode == "post-write-capability" {
					return []byte(strings.ReplaceAll(string(launchContract("newsha")), `"hub_endpoint_schema":1`, `"hub_endpoint_schema":0`)), nil
				}
				return launchContract("newsha"), nil
			}, func(int) ([]byte, error) {
				if repair && strings.HasPrefix(mode, "stopped") {
					return nil, errors.New("recorded predecessor stopped")
				}
				return []byte(`{"version":"oldsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180","started_at":"2026-01-01T00:00:00Z"}`), nil
			})
			base := fr.runFn
			fr.runFn = func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
				remote := strings.Join(argv[12:], " ")
				if strings.Contains(remote, "systemctl restart") || strings.Contains(remote, "systemctl start") {
					starts++
				}
				if repair && strings.HasPrefix(remote, "if test -x ") {
					if selected.executableAbsent {
						return nil, exitStatus(t, 1)
					}
					return nil, nil
				}
				if repair && selected.executableAbsent && remote == resolveDeployCommand(selected.exe) {
					missingResolutions++
				}
				if repair && mode == "live-argv-mismatch" && remote == hubArgvRemote("4242") {
					return []byte("/opt/evener/bin/./evener\x00hub\x00"), nil
				}
				if repair && strings.Contains(remote, "cat /proc/sys/kernel/random/boot_id") {
					if strings.HasPrefix(mode, "stopped") {
						return nil, errors.New("recorded predecessor no longer exists")
					}
					if mode == "live-process-mismatch" || boundary && mode == "generation-at-write" {
						return []byte("other-boot\n99999\n"), nil
					}
				}
				if strings.Contains(remote, "cat >") {
					writes++
					if !repair || mode == "write-failed" {
						return nil, errors.New("interrupted recorded write")
					}
					selected.executableAbsent = false
				}
				if repair && strings.Contains(remote, " hub endpoint --json") {
					proc := strings.Contains(remote, "/proc/4242/exe hub endpoint --json")
					if proc {
						procReads++
					} else {
						postReads++
						if selected.executableAbsent {
							t.Fatal("read endpoint from absent target")
						}
					}
					out, err := base(ctx, argv, in)
					switch {
					case strings.HasSuffix(mode, "-report-missing"):
						return nil, exitStatus(t, 127)
					case strings.HasSuffix(mode, "-report-malformed"):
						return []byte("not a report"), nil
					case strings.HasSuffix(mode, "-report-schema"):
						return []byte(strings.ReplaceAll(string(out), `"schema_version":1`, `"schema_version":2`)), err
					}
					if mode == "live-config-mismatch" && proc || mode == "stopped-config-mismatch" && !proc {
						out = []byte(strings.ReplaceAll(string(out), "/home/dev/.local/state/evener", "/srv/changed-state"))
					}
					return out, err
				}
				return base(ctx, argv, in)
			}
			guard := func() error {
				if boundary && mode == "binding-at-write" {
					return errors.New("recorded binding changed")
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
			intent := m.deploymentIntent(false, hubIdentity{})
			intent.binding = newEffectBinding(host, guard)
			if _, _, err := m.deployForOperation(t.Context(), host, facts, intent); err == nil {
				t.Fatal("initial write did not retain a failed deployment")
			}
			retained := m.pendingRestart(host.Name)
			if retained.selected == nil || !retained.deployment {
				t.Fatal("missing recorded owner")
			}
			repair, selected.executableAbsent = true, true
			selected.stopped = strings.HasPrefix(mode, "stopped")
			if mode == "live" {
				if argv, err := m.recoverHubArgv(t.Context(), host, "4242"); err == nil || argv != nil || missingResolutions != 1 {
					t.Fatalf("ordinary argv recovery accepted absent executable: argv=%q err=%v missing-resolutions=%d", argv, err, missingResolutions)
				}
			}
			if mode == "target-mismatch" {
				host.EvenerPath = "/other/bin/evener"
				m.setResolvedTarget(host.Name, host.EvenerPath)
			}
			_, _, err = m.deployForOperation(t.Context(), host, Preflight{Host: host.Name, OS: "linux", Arch: "amd64", UID: "1000", ExecutableMissing: true}, retained)
			if mode == "live" || mode == "stopped" {
				if err != nil || writes != 2 || postReads == 0 || m.pendingRestart(host.Name).active() {
					t.Fatalf("repair: err=%v writes=%d proc=%d post=%d pending=%+v", err, writes, procReads, postReads, m.pendingRestart(host.Name))
				}
				if mode == "live" && procReads < 2 || mode == "stopped" && procReads != 0 {
					t.Fatalf("wrong resolver authority: proc=%d mode=%s", procReads, mode)
				}
				if mode == "live" && missingResolutions != 2 {
					// Ordinary recovery and deploy's existing-file probe both refuse.
					// Only deploy's existing parent-directory resolver may name the write.
					t.Fatalf("unexpected absent executable resolutions: missing-resolutions=%d", missingResolutions)
				}
			} else if err == nil || !m.pendingRestart(host.Name).deployment || !reflect.DeepEqual(m.pendingRestart(host.Name).selected, retained.selected) {
				t.Fatalf("refusal lost pending repair: err=%v writes=%d pending=%+v", err, writes, m.pendingRestart(host.Name))
			}
			if (mode == "post-write-capability" || strings.Contains(mode, "-report-")) && (!errors.Is(err, ErrEndpointMigration) || !isTerminal(err)) {
				t.Fatalf("post-write capability failure is not terminal: %v", err)
			}
			if mode == "live-argv-mismatch" || mode == "live-process-mismatch" || mode == "live-config-mismatch" || strings.HasPrefix(mode, "live-report-") || mode == "target-mismatch" || mode == "generation-at-write" || mode == "binding-at-write" {
				if writes != 1 {
					t.Fatalf("refused repair wrote: %d", writes)
				}
			}
			if (mode == "live-argv-mismatch" || mode == "live-process-mismatch") && procReads != 0 {
				t.Fatalf("unproven process reached executable resolver: proc=%d", procReads)
			}
			if starts != 0 || len(fr.recordedStarts()) != 0 {
				t.Fatal("repair refusal or deploy-only success started or attached a hub")
			}
		})
	}
}
