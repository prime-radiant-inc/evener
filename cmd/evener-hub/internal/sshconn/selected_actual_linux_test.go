//go:build linux

package sshconn

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/internal/hubendpoint"
)

// Opt-in only: run the compiled test in unshare -Urn with loopback up. This
// executes the actual hub and controller commands, not SSH transport or systemd.
func TestRemoteRestartActualDisposableHub(t *testing.T) {
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
	root := t.TempDir()
	home, configDir, state := filepath.Join(root, "home"), filepath.Join(root, "config"), filepath.Join(root, "hub-state")
	pathDir := filepath.Join(root, "bin")
	for _, dir := range []string{home, filepath.Join(configDir, "evener"), state, pathDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	// This is deliberately a supervisorless private PATH, not a stub systemctl.
	// Every executable that exists here is the host's actual command.
	for _, tool := range []string{"awk", "stat", "cat", "readlink", "ps", "id", "dirname", "basename", "realpath", "nohup", "lsof", "sed", "cksum", "sort", "grep", "uname", "env", "od"} {
		target, err := exec.LookPath(tool)
		if err != nil {
			if tool == "lsof" {
				continue
			}
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(pathDir, tool)); err != nil {
			t.Fatal(err)
		}
	}
	config := filepath.Join(configDir, "evener", "hub.toml")
	content := "addr = \"127.0.0.1:0\"\nhub_state_root = " + strconv.Quote(state) + "\nrun_dir = " + strconv.Quote(filepath.Join(root, "run")) + "\npast_index_db = " + strconv.Quote(filepath.Join(state, "index.db")) + "\nstate_glob = " + strconv.Quote(filepath.Join(root, "projects", "*")) + "\nplugin_auto_upgrade = false\n"
	if err := os.WriteFile(config, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	env := []string{"PATH=" + pathDir, "HOME=" + home, "XDG_CONFIG_HOME=" + configDir, "XDG_STATE_HOME=" + filepath.Join(root, "state"), "XDG_CACHE_HOME=" + filepath.Join(root, "cache")}
	log, err := os.OpenFile(filepath.Join(root, "hub.log"), os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	child := exec.Command(binary, "hub") // Actual default-path configuration, no config flag.
	child.Dir, child.Env, child.Stdout, child.Stderr = root, env, log, log
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	childDone := make(chan error, 1)
	go func() { childDone <- child.Wait() }()
	t.Cleanup(func() {
		_ = child.Process.Kill()
		select {
		case <-childDone:
		case <-time.After(5 * time.Second):
			t.Error("original hub did not exit")
		}
	})
	endpoint := func() (hubendpoint.Report, error) {
		cmd := exec.Command(binary, "hub", "endpoint", "--json")
		cmd.Dir, cmd.Env = root, env
		out, err := cmd.Output()
		if err != nil {
			return hubendpoint.Report{}, err
		}
		return hubendpoint.DecodeReport(bytes.NewReader(out))
	}
	var before hubendpoint.Report
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		before, err = endpoint()
		if err == nil && before.Running != nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil || before.Running == nil {
		t.Fatal("actual hub did not publish a verified endpoint, logs remain private")
	}
	// Kill only PIDs independently returned by this private selected endpoint.
	t.Cleanup(func() {
		report, err := endpoint()
		if err == nil && report.Running != nil {
			pid := report.Running.PID
			_ = syscall.Kill(pid, syscall.SIGTERM)
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				if err := syscall.Kill(pid, 0); err != nil {
					return
				}
				time.Sleep(20 * time.Millisecond)
			}
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Error("successor hub did not exit after TERM")
		}
	})
	host := hostreg.Host{Name: "actual", SSH: "root@disposable", EvenerPath: binary}
	fr := &fakeRunner{runFn: func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
		remote := strings.Join(argv[12:], " ")
		cmd := exec.CommandContext(ctx, "/bin/bash", "-c", remote)
		cmd.Dir, cmd.Env, cmd.Stdin = root, env, in
		return cmd.CombinedOutput()
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{})
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	facts := Preflight{OS: "linux", UID: "0", LaunchCheckKnown: true, HubEndpointSchema: 1, Version: before.Running.Version}
	if _, err := m.RestartForOperation(ctx, host, facts, nil); err != nil {
		t.Fatal(err)
	}
	after, err := endpoint()
	if err != nil || after.Running == nil {
		t.Fatalf("successor endpoint unavailable: %v", err)
	}
	if after.Running.PID == before.Running.PID || after.Running.BootID == before.Running.BootID || after.Running.RequestedListenAddr != "127.0.0.1:0" || after.Running.ConfigPath != config || after.HubStateRoot != state || m.pendingRestart(host.Name).selected != nil {
		t.Fatal("actual successor did not preserve selected default config/root/port-zero intent")
	}
	if len(fr.recordedStarts()) != 0 {
		t.Fatal("restart attempted credential-bearing attachment")
	}
	t.Logf("actual default-config Linux hub replaced PID %d with PID %d, new boot, selected root, requested port zero, pending cleared, no attach", before.Running.PID, after.Running.PID)
	// Only the SSH transport boundary is replaced. Ensure runs its actual
	// preflight, selected endpoint command and real credential-bearing bridge.
	fr.startFn = func(ctx context.Context, argv []string, stderr io.Writer) (Stdio, error) {
		remote := strings.Join(argv[12:], " ")
		local := append([]string{"/usr/bin/env", "-i"}, env...)
		local = append(local, "/bin/bash", "-c", "CDPATH='' cd -P -- "+strconv.Quote(root)+" && "+remote)
		return (execRunner{}).Start(ctx, local, stderr)
	}
	ch, err := m.Ensure(ctx, host.Name)
	if err != nil {
		t.Fatal(err)
	}
	if ch.Client() == nil || len(fr.recordedStarts()) != 1 {
		t.Fatal("Ensure did not initialize the actual bridge")
	}
	attached, err := endpoint()
	if err != nil || attached.Running == nil || attached.Running.PID != after.Running.PID || attached.Running.BootID != after.Running.BootID {
		t.Fatal("Ensure changed the selected successor instead of attaching")
	}
	t.Log("actual Ensure selected the successor and initialized its bridge without another restart")
}
