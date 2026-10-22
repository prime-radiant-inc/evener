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
	"strings"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubedge"
	"primeradiant.com/evener/execsupport/shellquote"
	"primeradiant.com/evener/internal/hubendpoint"
)

// TOML, cwd resolution and endpoint inspection execute on the real filesystem.
// The existing service and all launch boundaries are modeled, no hub is started.
func TestBootstrapRelativeRootServiceContext(t *testing.T) {
	resolver := buildConfigIdentityResolver(t)
	for _, platform := range []string{"linux", "darwin"} {
		for _, live := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/live=%t", platform, live), func(t *testing.T) {
				root := t.TempDir()
				login, service := filepath.Join(root, "login"), filepath.Join(root, "service")
				for _, dir := range []string{login, service, filepath.Join(service, "hub-state")} {
					if err := os.MkdirAll(dir, 0700); err != nil {
						t.Fatal(err)
					}
				}
				config := filepath.Join(root, "hub.toml")
				if err := os.WriteFile(config, []byte("addr = \"127.0.0.1:0\"\nhub_state_root = \"hub-state\"\n"), 0600); err != nil {
					t.Fatal(err)
				}
				token := filepath.Join(service, "hub-state", hubedge.TokenFileName)
				if err := os.WriteFile(token, []byte("fixture-credential"), 0600); err != nil {
					t.Fatal(err)
				}
				fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
					t.Errorf("unexpected remote command: %v", argv)
					return nil, fmt.Errorf("unexpected remote command: %v", argv)
				}}
				model := modelSelectedProcess(t, fr, resolver, "", "")
				model.stopped = !live
				base := fr.runFn
				starts, serviceReads := 0, 0
				intercepted := errors.New("intercepted forbidden fresh launch")
				pid := "0"
				if live {
					pid = "4242"
				}
				env := []string{"HOME=" + root}
				fr.runFn = func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
					remote := strings.Join(argv[12:], " ")
					switch remote {
					case supervisorShowRemote(supervisor{kind: supervisorSystemd, label: "evener-hub.service"}):
						return fmt.Appendf(nil, "Id=evener-hub.service\nMainPID=%s\nUser=dev\nExecStart={ path=%s ; argv[]=%s hub --config %s ; ignore_errors=no ; }\nWorkingDirectory=%s\n", pid, resolver, resolver, config, service), nil
					case launchdDefinitionRemote("1000", "com.example.evener-hub"):
						return fmt.Appendf(nil, "pid = %s\nprogram = %s\nworking directory = %s\narguments = {\n%s\nhub\n--config\n%s\n}\nHOME => %s\n", pid, resolver, service, resolver, config, root), nil
					case "getent passwd dev | cut -d: -f6":
						return []byte(root + "\n"), nil
					case "pwd":
						return []byte(login + "\n"), nil
					case freshOwnerEnvironmentRemote:
						return []byte("HOME=" + root + "\x00"), nil
					}
					if strings.Contains(remote, "nohup ") || strings.HasPrefix(remote, "systemctl start ") || strings.HasPrefix(remote, "launchctl kickstart ") {
						starts++
						return nil, intercepted
					}
					if strings.Contains(remote, " hub endpoint --json") || strings.Contains(remote, "evener_resolve") || strings.HasSuffix(remote, " && pwd -P") {
						if strings.HasPrefix(remote, ownerContext(service, env)) {
							serviceReads++
						}
						cmd := exec.CommandContext(ctx, "/bin/sh", "-c", remote)
						cmd.Dir = login
						cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + root}
						return cmd.Output()
					}
					return base(ctx, argv, in)
				}
				host := hostreg.Host{Name: "fixture", SSH: "dev@fixture.invalid", EvenerPath: resolver, ConfigPath: config}
				m := newTestManager(t, testRegistry(t, host), fr, Options{})
				facts := selectedRestartFacts()
				facts.OS = platform
				if platform == "darwin" {
					facts.UID = "1000"
				}
				report, err := m.resolveHostEndpoint(t.Context(), host, facts)
				if err != nil || report.State != "stopped" || report.HubStateRoot != filepath.Join(login, "hub-state") {
					t.Fatalf("login-context selection: %+v err=%v", report, err)
				}
				err = m.bootstrapHub(t.Context(), host, facts, "", "")
				if !errors.Is(err, errOwnerRootMismatch) || starts != 0 || serviceReads == 0 {
					t.Fatalf("same config service bypassed: err=%v starts=%d serviceReads=%d", err, starts, serviceReads)
				}
				if data, err := os.ReadFile(token); err != nil || string(data) != "fixture-credential" {
					t.Fatalf("existing service credential changed: err=%v", err)
				}
				if _, err := os.Stat(filepath.Join(login, "hub-state")); !os.IsNotExist(err) {
					t.Fatalf("second state/credentials created: %v", err)
				}
				if m.pendingRestart(host.Name).active() || len(fr.recordedStarts()) != 0 {
					t.Fatal("refused bootstrap retained a launch")
				}
				// Recheck the same config in the service context: only its cwd-relative root differs.
				cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", shellquote.RemoteWord(resolver)+" hub endpoint --json --config "+shellquote.RemoteWord(config))
				cmd.Dir = service
				cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + root}
				out, err := cmd.Output()
				if err != nil {
					t.Fatal(err)
				}
				serviceReport, err := hubendpoint.DecodeReport(bytes.NewReader(out))
				if err != nil || serviceReport.ConfigPath != report.ConfigPath || serviceReport.HubStateRoot != filepath.Join(service, "hub-state") {
					t.Fatalf("service-context selection: %+v err=%v", serviceReport, err)
				}
			})
		}
	}
}
