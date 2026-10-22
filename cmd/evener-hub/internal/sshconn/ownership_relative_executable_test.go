package sshconn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/execsupport/shellquote"
)

// Files, executable resolution and the atomic deployment write are real. Process
// identity, endpoint reports and supervisor discovery are strict modeled inputs.
func TestOwnerRelativeExecutableDeployOnly(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		for _, different := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/different=%t", goos, different), func(t *testing.T) {
				root := t.TempDir()
				project, login := filepath.Join(root, "project"), filepath.Join(root, "login")
				for _, dir := range []string{project, login} {
					if err := os.Mkdir(dir, 0700); err != nil {
						t.Fatal(err)
					}
				}
				project, err := filepath.EvalSymlinks(project)
				if err != nil {
					t.Fatal(err)
				}
				target, decoy := filepath.Join(project, "evener"), filepath.Join(login, "evener")
				for name, data := range map[string]string{target: "predecessor", decoy: "login-installation"} {
					if err := os.WriteFile(name, []byte(data), 0755); err != nil {
						t.Fatal(err)
					}
				}
				arch := "amd64"
				if goos == "darwin" {
					arch = "arm64"
				}
				fr, selected := deployRunnerForFixture(t, goos, arch, func(int) ([]byte, error) {
					data, err := os.ReadFile(target)
					if err != nil {
						return nil, err
					}
					version := "oldsha"
					if string(data) == "staged-binary" {
						version = "newsha"
					}
					return launchContract(version), nil
				}, func(int) ([]byte, error) {
					return []byte(`{"version":"oldsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180","started_at":"2026-01-01T00:00:00Z"}`), nil
				})
				selected.exe, selected.adhoc = target, true
				base := fr.runFn
				writes, effects, cwdReads := 0, 0, 0
				cwd := project
				if different {
					cwd = login
				}
				fr.runFn = func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
					remote := strings.Join(argv[12:], " ")
					switch remote {
					case hubArgvRemote("4242"):
						return []byte("./evener\x00hub\x00"), nil
					case "readlink /proc/4242/cwd":
						cwdReads++
						return []byte(cwd + "\n"), nil
					case "lsof -a -p 4242 -d cwd -Fn":
						cwdReads++
						return []byte("p4242\nfcwd\nn" + cwd + "\n"), nil
					case ownerContext(cwd, []string{"HOME=/home/dev"}) + "./evener hub endpoint --json":
						forward := append([]string(nil), argv[:12]...)
						return base(ctx, append(forward, shellquote.RemoteWord(target)+" hub endpoint --json"), in)
					case pushBinaryRemote(target, int64(len("staged-binary"))):
						writes++
					case resolveDeployCommand("./evener"), resolveDeployCommand(project + "/./evener"), resolveDeployCommand(login + "/./evener"), resolveDeployCommand(target), createTargetCommand(target), "test -d " + shellquote.RemoteWord(project):
					default:
						if strings.HasPrefix(remote, "kill ") || strings.HasPrefix(remote, "nohup ") || strings.Contains(remote, "kickstart ") || strings.HasPrefix(remote, "systemctl start ") || strings.HasPrefix(remote, "systemctl restart ") {
							effects++
						}
						return base(ctx, argv, in)
					}
					cmd := exec.CommandContext(ctx, "/bin/sh", "-c", remote)
					cmd.Dir, cmd.Stdin = login, in
					return cmd.Output()
				}
				host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: target}
				m := newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "newsha", BuildBinary: writeStageBinary})
				host, _ = m.reg.Get(host.Name)
				facts, err := m.preflight(t.Context(), host)
				if err != nil {
					t.Fatal(err)
				}
				got, after, err := m.DeployForOperation(t.Context(), host, facts, false, nil)
				if different {
					if !errors.Is(err, errOwnerSelectionMismatch) || writes != 0 {
						t.Fatalf("different executable accepted: err=%v writes=%d", err, writes)
					}
				} else if err != nil || got != target || after.Version != "newsha" || writes != 1 || cwdReads == 0 {
					t.Fatalf("relative owner deploy: target=%q version=%q err=%v writes=%d cwdReads=%d", got, after.Version, err, writes, cwdReads)
				}
				if !different {
					owner := m.ownerWitnesses[host.Name]
					if len(owner.argv) != 2 || owner.argv[0] != "./evener" || owner.cwd != project {
						t.Fatalf("owner launch spelling changed: argv=%q cwd=%q", owner.argv, owner.cwd)
					}
				}
				want := "staged-binary"
				if different {
					want = "predecessor"
				}
				for name, content := range map[string]string{target: want, decoy: "login-installation"} {
					if data, err := os.ReadFile(name); err != nil || string(data) != content {
						t.Fatalf("installation %q = %q, err=%v", name, data, err)
					}
				}
				if effects != 0 || len(fr.recordedStarts()) != 0 || m.pendingRestart(host.Name).active() {
					t.Fatalf("deploy-only changed lifecycle: effects=%d pending=%+v", effects, m.pendingRestart(host.Name))
				}
			})
		}
	}
}
