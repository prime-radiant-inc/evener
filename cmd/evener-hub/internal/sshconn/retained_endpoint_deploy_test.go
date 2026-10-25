package sshconn

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/internal/hubendpoint"
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
				if !strings.HasSuffix(mode, "-at-write") {
					want, terminal := ErrEndpointMigration, true
					switch mode {
					case "missing-command", "command-failed":
						want, terminal = ErrSSHStart, false
					case "malformed":
						want = ErrPreflightDecode
					}
					if !errors.Is(err, want) || isTerminal(err) != terminal || errors.Is(err, ErrEndpointMigration) != errors.Is(want, ErrEndpointMigration) {
						t.Fatalf("unexpected report failure classification: %v", err)
					}
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
	for _, mode := range []string{"live", "live-unresponsive", "live-unresponsive-health-returns", "live-unresponsive-pid-mismatch", "live-boot-mismatch", "live-build-mismatch", "live-sha-mismatch", "live-trailing-space", "live-different-directory", "live-argv-mismatch", "live-process-mismatch", "live-config-mismatch", "live-report-missing", "live-report-malformed", "live-report-schema", "stopped", "stopped-config-mismatch", "stopped-report-missing", "stopped-report-malformed", "stopped-report-schema", "target-mismatch", "write-failed", "post-write-capability", "generation-at-write", "binding-at-write"} {
		t.Run(mode, func(t *testing.T) {
			host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
			realPaths := mode == "live-trailing-space" || mode == "live-different-directory"
			var cwd, target, alias, childPID, otherPID string
			if realPaths {
				if runtime.GOOS != "linux" {
					t.Skip("real /proc cwd evidence requires Linux")
				}
				root := t.TempDir()
				cwd, target, alias = filepath.Join(root, "physical "), filepath.Join(root, "evener"), filepath.Join(root, "alias", "evener")
				for _, dir := range []string{cwd, filepath.Join(root, "physical"), filepath.Dir(alias)} {
					if err := os.Mkdir(dir, 0o755); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(target, []byte("predecessor"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, alias); err != nil {
					t.Fatal(err)
				}
				child := func(dir string) string {
					cmd := exec.Command("sleep", "60")
					cmd.Dir = dir
					if err := cmd.Start(); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
					return strconv.Itoa(cmd.Process.Pid)
				}
				childPID, otherPID = child(cwd), child(filepath.Join(root, "physical"))
				host.EvenerPath = alias
			}
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
			selected.initialUnavailable = strings.HasPrefix(mode, "live-unresponsive")
			if realPaths {
				selected.exe = target
				selected.endpointAlias = alias + " hub endpoint --json"
			}
			base := fr.runFn
			fr.runFn = func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
				remote := strings.Join(argv[12:], " ")
				if realPaths {
					run := func(command string) ([]byte, error) {
						cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
						cmd.Stdin = in
						return cmd.Output()
					}
					switch {
					case remote == "readlink /proc/4242/cwd":
						pid := childPID
						if repair && mode == "live-different-directory" {
							pid = otherPID
						}
						return run("readlink /proc/" + pid + "/cwd")
					case strings.HasSuffix(remote, " && pwd -P"), strings.HasPrefix(remote, "test -d "), remote == resolveDeployCommand(alias), remote == resolveRetainedDeployCommand(alias), remote == resolveDeployCommand(target), remote == createTargetCommand(target):
						return run(remote)
					case strings.Contains(remote, "cat >") && repair:
						writes++
						out, err := run(remote)
						if err == nil {
							selected.executableAbsent = false
						}
						return out, err
					case strings.HasPrefix(remote, ownerContext(cwd, []string{"HOME=/home/dev"})):
						remote = ownerContext("/home/dev", []string{"HOME=/home/dev"}) + strings.TrimPrefix(remote, ownerContext(cwd, []string{"HOME=/home/dev"}))
						argv = rawCommandArgv(Options{}, host, remote)
					}
				}
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
				if repair && strings.HasPrefix(mode, "stopped") && remote == processAliveRemote("4242") {
					return []byte(pidGoneMarker + "\n"), nil
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
					if proc && (mode == "live-unresponsive-pid-mismatch" || mode == "live-boot-mismatch" || mode == "live-build-mismatch" || mode == "live-sha-mismatch") {
						var report hubendpoint.Report
						if err := json.Unmarshal(out, &report); err != nil || report.Running == nil {
							t.Fatalf("live fixture report: %s, %v", out, err)
						}
						switch mode {
						case "live-unresponsive-pid-mismatch":
							report.Running.PID++
						case "live-boot-mismatch":
							report.Running.BootID = "different-boot"
						case "live-build-mismatch":
							report.Running.Version = "different-build"
						case "live-sha-mismatch":
							report.Running.BackendGitSHA = "different-sha"
						}
						return json.Marshal(report)
					}
					return out, err
				}
				out, err := base(ctx, argv, in)
				if realPaths && strings.HasPrefix(remote, "systemctl show --property=Id,") {
					out = []byte(strings.ReplaceAll(string(out), "WorkingDirectory=/home/dev\n", "WorkingDirectory="+cwd+"\n"))
				}
				return out, err
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
			} else if realPaths {
				t.Logf("initial interrupted deployment: %v", err)
			}
			retained := m.pendingRestart(host.Name)
			if retained.selected == nil || !retained.deployment {
				t.Fatal("missing recorded owner")
			}
			if realPaths {
				if retained.selected.cwd != cwd || retained.installTarget != target {
					t.Fatalf("recorded paths: cwd=%q target=%q", retained.selected.cwd, retained.installTarget)
				}
				if err := os.Chmod(target, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			repair, selected.executableAbsent = true, true
			selected.initialUnavailable = mode == "live-unresponsive"
			selected.stopped = strings.HasPrefix(mode, "stopped")
			if mode == "target-mismatch" {
				host.EvenerPath = "/other/bin/evener"
				m.setResolvedTarget(host.Name, host.EvenerPath)
			}
			_, _, err = m.deployForOperation(t.Context(), host, Preflight{Host: host.Name, OS: "linux", Arch: "amd64", UID: "1000", ExecutableMissing: true}, retained)
			if mode == "live" || mode == "live-unresponsive" || mode == "live-unresponsive-health-returns" || mode == "live-trailing-space" || mode == "stopped" {
				if err != nil || writes != 2 || postReads == 0 || m.pendingRestart(host.Name).active() {
					t.Fatalf("repair: err=%v writes=%d proc=%d post=%d pending=%+v", err, writes, procReads, postReads, m.pendingRestart(host.Name))
				}
				if (mode == "live" || strings.HasPrefix(mode, "live-unresponsive")) && procReads < 2 || mode == "stopped" && procReads != 0 {
					t.Fatalf("wrong resolver authority: proc=%d mode=%s", procReads, mode)
				}
				if mode == "live" && missingResolutions != 2 {
					// Both pre/post-build destination probes refuse the absent file
					// before resolving its existing parent directory.
					t.Fatalf("unexpected absent executable resolutions: missing-resolutions=%d", missingResolutions)
				}
				if realPaths {
					data, readErr := os.ReadFile(target)
					link, linkErr := os.Readlink(alias)
					if readErr != nil || string(data) != "staged-binary" || linkErr != nil || link != target || procReads < 2 {
						t.Fatalf("real repair: target=%q err=%v symlink=%q err=%v proc=%d", data, readErr, link, linkErr, procReads)
					}
				}
			} else if err == nil || !m.pendingRestart(host.Name).deployment || !reflect.DeepEqual(m.pendingRestart(host.Name).selected, retained.selected) {
				t.Fatalf("refusal lost pending repair: err=%v writes=%d pending=%+v", err, writes, m.pendingRestart(host.Name))
			}
			if mode == "live-different-directory" && !errors.Is(err, errOwnerSelectionMismatch) {
				t.Fatalf("cwd mismatch lost typed refusal: %v", err)
			}
			if (mode == "post-write-capability" || strings.Contains(mode, "-report-")) && (!errors.Is(err, ErrEndpointMigration) || !isTerminal(err)) {
				t.Fatalf("post-write capability failure is not terminal: %v", err)
			}
			if mode == "live-different-directory" || mode == "live-argv-mismatch" || mode == "live-process-mismatch" || mode == "live-config-mismatch" || strings.HasPrefix(mode, "live-report-") || mode == "target-mismatch" || mode == "generation-at-write" || mode == "binding-at-write" || strings.HasPrefix(mode, "live-") && strings.HasSuffix(mode, "-mismatch") {
				if writes != 1 {
					t.Fatalf("refused repair wrote: %d", writes)
				}
			}
			if (mode == "live-different-directory" || mode == "live-argv-mismatch" || mode == "live-process-mismatch") && procReads != 0 {
				t.Fatalf("unproven process reached executable resolver: proc=%d", procReads)
			}
			if starts != 0 || len(fr.recordedStarts()) != 0 {
				t.Fatal("repair refusal or deploy-only success started or attached a hub")
			}
		})
	}
}
