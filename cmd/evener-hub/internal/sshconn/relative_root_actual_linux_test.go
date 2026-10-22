//go:build linux

package sshconn

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/internal/hubendpoint"
)

// Like TestRemoteRestartActualDisposableHub, this replaces only SSH transport.
// Run in unshare -Urn with loopback up, never against a host service.
func TestRemoteRelativeStateRootActualDisposableHub(t *testing.T) {
	binary := os.Getenv("EVENER_ACTUAL_RESTART_BIN")
	if binary == "" {
		t.Skip("requires an isolated actual-hub binary and network namespace")
	}
	currentNS, err := os.Readlink("/proc/self/ns/net")
	if err != nil || currentNS == os.Getenv("EVENER_PARENT_NET_NS") || os.Getenv("EVENER_PARENT_NET_NS") == "" {
		t.Fatal("actual hub requires its own network namespace")
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagLoopback == 0 {
			t.Fatal("namespace has a non-loopback interface")
		}
	}
	image, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"relative-restart", "relative-deploy", "relative-bootstrap", "absolute-restart"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			state := filepath.Join(root, "hub-state")
			home, configDir, pathDir := filepath.Join(root, "home"), filepath.Join(root, "config"), filepath.Join(root, "bin")
			for _, dir := range []string{home, filepath.Join(configDir, "evener"), pathDir} {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			// A private PATH omits all supervisors. These are the real OS tools,
			// not command fixtures, and process evidence comes from real /proc.
			for _, tool := range []string{"awk", "stat", "cat", "readlink", "ps", "id", "dirname", "basename", "realpath", "nohup", "lsof", "sed", "cksum", "sort", "grep", "uname", "env", "od", "mkdir", "chmod", "mv", "rm", "mktemp", "wc", "tr"} {
				actual, err := exec.LookPath(tool)
				if err != nil {
					if tool == "lsof" {
						continue
					}
					t.Fatal(err)
				}
				if err := os.Symlink(actual, filepath.Join(pathDir, tool)); err != nil {
					t.Fatal(err)
				}
			}
			target := filepath.Join(pathDir, "evener")
			if err := os.WriteFile(target, image, 0o700); err != nil {
				t.Fatal(err)
			}
			configured := "hub-state"
			if mode == "absolute-restart" {
				configured = state
			}
			config := filepath.Join(configDir, "evener", "hub.toml")
			content := fmt.Sprintf("addr = %q\nhub_state_root = %q\nrun_dir = %q\npast_index_db = %q\nstate_glob = %q\nplugin_auto_upgrade = false\n", "127.0.0.1:0", configured, filepath.Join(root, "run"), filepath.Join(root, "index.db"), filepath.Join(root, "projects", "*"))
			if err := os.WriteFile(config, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			env := []string{"PATH=" + pathDir, "HOME=" + home, "XDG_CONFIG_HOME=" + configDir, "XDG_STATE_HOME=" + filepath.Join(root, "state"), "XDG_CACHE_HOME=" + filepath.Join(root, "cache")}
			endpoint := func() (hubendpoint.Report, error) {
				cmd := exec.Command(target, "hub", "endpoint", "--json")
				cmd.Dir, cmd.Env = root, env
				out, err := cmd.Output()
				if err != nil {
					return hubendpoint.Report{}, err
				}
				return hubendpoint.DecodeReport(bytes.NewReader(out))
			}
			// This also cleans up a healthy bootstrap whose successor proof failed.
			t.Cleanup(func() {
				report, err := endpoint()
				if err == nil && report.Running != nil {
					_ = syscall.Kill(report.Running.PID, syscall.SIGTERM)
					deadline := time.Now().Add(5 * time.Second)
					for time.Now().Before(deadline) {
						report, err = endpoint()
						if err == nil && report.State == "stopped" {
							return
						}
						time.Sleep(20 * time.Millisecond)
					}
					t.Error("actual hub did not release its selected lock after TERM")
				}
			})
			var before hubendpoint.Report
			if mode != "relative-bootstrap" {
				log, err := os.Create(filepath.Join(root, "hub.log"))
				if err != nil {
					t.Fatal(err)
				}
				defer log.Close()
				child := exec.Command(target, "hub")
				child.Dir, child.Env, child.Stdout, child.Stderr = root, env, log, log
				if err := child.Start(); err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() { done <- child.Wait() }()
				t.Cleanup(func() {
					_ = child.Process.Kill()
					select {
					case <-done:
					case <-time.After(5 * time.Second):
						t.Error("original ad-hoc hub did not exit")
					}
				})
				deadline := time.Now().Add(15 * time.Second)
				for time.Now().Before(deadline) {
					before, err = endpoint()
					if err == nil && before.Running != nil {
						break
					}
					time.Sleep(50 * time.Millisecond)
				}
				if err != nil || before.Running == nil {
					t.Fatalf("actual ad-hoc startup unavailable: %v", err)
				}
			}
			host := hostreg.Host{Name: "relative-root", SSH: "root@disposable", EvenerPath: target}
			starts, writes := 0, 0
			fr := &fakeRunner{runFn: func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
				remote := strings.Join(argv[12:], " ")
				if strings.Contains(remote, "nohup ") {
					starts++
				}
				if strings.Contains(remote, "cat >") {
					writes++
				}
				cmd := exec.CommandContext(ctx, "/bin/bash", "-c", remote)
				cmd.Dir, cmd.Env, cmd.Stdin = root, env, in
				return cmd.CombinedOutput()
			}}
			m := newTestManager(t, testRegistry(t, host), fr, Options{BuildBinary: func(_ context.Context, _, _, out string) error {
				return os.WriteFile(out, image, 0o700)
			}})
			ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
			defer cancel()
			facts, err := m.preflight(ctx, host)
			if err != nil {
				t.Fatalf("real preflight: %v", err)
			}
			if mode == "relative-deploy" {
				_, facts, err = m.DeployForOperation(ctx, host, facts, true, nil)
			}
			if err == nil {
				_, err = m.RestartForOperation(ctx, host, facts, nil)
			}
			after, endpointErr := endpoint()
			if err != nil || endpointErr != nil || after.Running == nil {
				t.Fatalf("actual managed lifecycle: err=%v endpointErr=%v state=%s root=%q healthy=%t starts=%d pending=%t", err, endpointErr, after.State, after.HubStateRoot, after.Running != nil, starts, m.pendingRestart(host.Name).selected != nil)
			}
			if after.HubStateRoot != state || after.Running.HubStateRoot != state || after.Running.ConfigPath != config || after.Running.RequestedListenAddr != "127.0.0.1:0" || starts != 1 || m.pendingRestart(host.Name).selected != nil {
				t.Fatalf("successor root/intent/pending: report=%+v starts=%d pending=%+v", after, starts, m.pendingRestart(host.Name))
			}
			if before.Running != nil && (before.HubStateRoot != state || before.Running.HubStateRoot != state || before.Running.PID == after.Running.PID || before.Running.BootID == after.Running.BootID) {
				t.Fatal("restart did not replace the verified absolute-root ad-hoc owner")
			}
			wantWrites := 0
			if mode == "relative-deploy" {
				wantWrites = 1
			}
			if writes != wantWrites || len(fr.recordedStarts()) != 0 {
				t.Fatalf("unexpected deployment or attach: writes=%d want=%d attaches=%d", writes, wantWrites, len(fr.recordedStarts()))
			}
			if _, err := os.Stat(filepath.Join(state, "hub.lock")); err != nil {
				t.Fatal(err)
			}
			t.Logf("real root=%q PID=%d boot=%s, starts=%d writes=%d, pending settled", after.HubStateRoot, after.Running.PID, after.Running.BootID, starts, writes)
		})
	}
}
