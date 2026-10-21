package sshconn

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

func TestOwnerPhysicalContext(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("live cwd observation uses /proc")
	}
	resolver := buildConfigIdentityResolver(t)
	for _, phase := range []string{"context", "live", "stopped-bootstrap"} {
		for _, missing := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/missing=%t", phase, missing), func(t *testing.T) {
				root := t.TempDir()
				logical, physical := filepath.Join(root, "srv"), filepath.Join(root, "data")
				for _, dir := range []string{logical, filepath.Join(physical, "inner")} {
					if err := os.MkdirAll(dir, 0o700); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Symlink(filepath.Join(physical, "inner"), filepath.Join(logical, "link")); err != nil {
					t.Fatal(err)
				}
				cwd := logical + "/link/.." // Never filepath.Clean this kernel pathname.
				config := filepath.Join(physical, "hub.toml")
				if missing {
					config = filepath.Join(root, "selected.toml")
				}
				state := filepath.Join(root, "state")
				writeConfig := func(file string) {
					t.Helper()
					if err := os.WriteFile(file, fmt.Appendf(nil, "addr = \"127.0.0.1:0\"\nhub_state_root = %q\n", state), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				writeConfig(config)
				if missing {
					// Logical cd would incorrectly accept this file, even though the
					// physical service directory has no relative hub.toml.
					if err := os.Symlink(config, filepath.Join(logical, "hub.toml")); err != nil {
						t.Fatal(err)
					}
				} else {
					writeConfig(filepath.Join(logical, "hub.toml")) // Different file, same contents.
				}
				actual, err := filepath.EvalSymlinks(cwd)
				if err != nil || actual != physical {
					t.Fatalf("kernel cwd lookup: got %q, err=%v, want %q", actual, err, physical)
				}
				_, err = os.Stat(cwd + "/hub.toml")
				if missing && !os.IsNotExist(err) || !missing && err != nil {
					t.Fatalf("physical relative config existence: missing=%t err=%v", missing, err)
				}
				var childPID string
				if phase == "live" {
					child := exec.Command("sleep", "60")
					child.Dir = cwd
					if err := child.Start(); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
					childPID = strconv.Itoa(child.Process.Pid)
				}
				fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
					return nil, fmt.Errorf("unexpected remote command: %v", argv)
				}}
				fixture := modelSelectedProcess(t, fr, resolver, "", "")
				fixture.stopped = phase != "live"
				base := fr.runFn
				serviceStarts, adHocStarts, contextReads := 0, 0, 0
				stop := errors.New("intercepted selected service start")
				fr.runFn = func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
					remote := strings.Join(argv[12:], " ")
					switch {
					case remote == "systemctl start evener-hub.service":
						serviceStarts++
						return nil, stop
					case strings.Contains(remote, "nohup "):
						adHocStarts++
						return nil, stop
					case remote == supervisorShowRemote(supervisor{kind: supervisorSystemd, label: "evener-hub.service"}):
						pid := "0"
						if phase == "live" {
							pid = "4242"
						}
						return fmt.Appendf(nil, "Id=evener-hub.service\nMainPID=%s\nUser=dev\nExecStart={ path=%s ; argv[]=%s hub --config hub.toml ; ignore_errors=no ; }\nWorkingDirectory=%s\n", pid, resolver, resolver, cwd), nil
					case remote == hubArgvRemote("4242"):
						return []byte(resolver + "\x00hub\x00--config\x00hub.toml\x00"), nil
					case remote == "readlink /proc/4242/cwd":
						return exec.CommandContext(ctx, "readlink", "/proc/"+childPID+"/cwd").Output()
					case remote == ownerEnvironmentRemote("4242"), remote == freshOwnerEnvironmentRemote:
						return []byte("HOME=" + root + "\x00"), nil
					case remote == "getent passwd dev | cut -d: -f6", remote == "pwd":
						return []byte(root + "\n"), nil
					case strings.Contains(remote, " hub endpoint --json"), strings.Contains(remote, "evener_resolve"), strings.HasSuffix(remote, " && pwd -P"):
						if strings.Contains(remote, " || exit; unset ") {
							contextReads++
						}
						cmd := exec.CommandContext(ctx, "/bin/sh", "-c", remote)
						cmd.Dir = root
						cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + root}
						var stderr bytes.Buffer
						cmd.Stderr = &stderr
						out, err := cmd.Output()
						if err != nil {
							return out, fmt.Errorf("real endpoint command: %w, stderr=%s", err, stderr.String())
						}
						return out, nil
					}
					return base(ctx, argv, in)
				}
				host := hostreg.Host{Name: "fixture", SSH: "dev@fixture.invalid", EvenerPath: resolver, ConfigPath: config}
				m := newTestManager(t, testRegistry(t, host), fr, Options{})
				report, err := m.resolveHostEndpoint(t.Context(), host, selectedRestartFacts())
				if err != nil || !report.ConfigExists || report.ConfigPath != config {
					t.Fatalf("actual selected config: %+v, err=%v", report, err)
				}
				switch phase {
				case "context":
					err = m.checkOwnerConfig(t.Context(), host, []string{resolver, "hub", "--config", "hub.toml"}, cwd, []string{"HOME=" + root}, report)
				case "live":
					var owner selectedOwner
					owner, err = m.resolveLinuxOwner(t.Context(), host, report, "4242", false)
					if !missing && (err != nil || owner.supervisor.kind != supervisorSystemd || owner.cwd != physical) {
						t.Fatalf("live physical service association: owner=%+v err=%v", owner, err)
					}
				case "stopped-bootstrap":
					err = m.bootstrapHub(t.Context(), host, selectedRestartFacts(), "", "")
					if !missing && (!errors.Is(err, stop) || serviceStarts != 1 || adHocStarts != 0) {
						t.Fatalf("physical service bootstrap: err=%v service=%d adhoc=%d", err, serviceStarts, adHocStarts)
					}
				}
				if missing {
					if err == nil || errors.Is(err, stop) || serviceStarts != 0 || adHocStarts != 0 {
						t.Fatalf("missing physical config did not refuse: err=%v service=%d adhoc=%d", err, serviceStarts, adHocStarts)
					}
				} else if phase == "context" && err != nil {
					t.Fatalf("physical context rejected matching relative config: %v", err)
				}
				if contextReads == 0 || strings.Contains(fmt.Sprint(err), "unexpected remote command") {
					t.Fatalf("physical resolver was not exercised: reads=%d err=%v", contextReads, err)
				}
			})
		}
	}
}
