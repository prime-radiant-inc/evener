//go:build linux

package sshconn

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// These are actual Linux kernel/process tests. The disposable executable is a
// synthetic schema responder, not Evener or evidence of a real remote service.
// Only endpoint JSON and absent supervisor listings remain command fixtures.
func TestRemoteOwnerLinuxOriginalImage(t *testing.T) {
	t.Run("capable running image, unsupported disk", func(t *testing.T) { testOwnerLinuxImage(t, "1", "0") })
	t.Run("old running image, capable disk", func(t *testing.T) { testOwnerLinuxImage(t, "0", "1") })
}

func testOwnerLinuxImage(t *testing.T, originalSchema, installedSchema string) {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "main.go")
	program := `package main
import("fmt";"io";"os";"syscall")
var schema="1"
func main(){
 if len(os.Args)>1 && os.Args[1]=="launch-check" { fmt.Printf("{\"protocol\":\"fixture\",\"version\":\"fixture\",\"hub_endpoint_schema\":%s}\n",schema);return }
 f,e:=os.OpenFile(os.Args[3],os.O_RDWR|os.O_CREATE,0600);if e!=nil{panic(e)};defer f.Close()
 if e=syscall.Flock(int(f.Fd()),syscall.LOCK_EX);e!=nil{panic(e)}
 fmt.Println("ready");io.Copy(io.Discard,os.Stdin)
}`
	if err := os.WriteFile(source, []byte(program), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "evener")
	replacement := filepath.Join(root, "replacement")
	for _, build := range []struct{ target, schema string }{{binary, originalSchema}, {replacement, installedSchema}} {
		cmd := exec.CommandContext(t.Context(), "go", "build", "-o", build.target, "-ldflags=-X main.schema="+build.schema, source)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + root, "GOCACHE=" + os.Getenv("GOCACHE"), "GOMODCACHE=" + os.Getenv("GOMODCACHE"), "GOTOOLCHAIN=local"}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build fixture: %v: %s", err, out)
		}
	}
	config := filepath.Join(root, "hub.toml")
	// The lock path is a daemon argument solely in this synthetic executable.
	// Production validateHubArgv permits arbitrary flag values, as before.
	// t.Context is canceled before Cleanup. Close stdin and reap explicitly so
	// a successful child shutdown is not raced by CommandContext's SIGKILL.
	cmd := exec.Command(binary, "hub", "--fixture-lock", filepath.Join(root, "hub.lock"), "--config="+config)
	cmd.Env = []string{"HOME=" + root}
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = input.Close()
		if err := cmd.Wait(); err != nil {
			t.Errorf("reap fixture: %v", err)
		}
	})
	ready, err := bufio.NewReader(output).ReadString('\n')
	if err != nil || ready != "ready\n" {
		t.Fatalf("child readiness=%q err=%v", ready, err)
	}
	pid := strconv.Itoa(cmd.Process.Pid)
	lockBefore, err := os.Stat(filepath.Join(root, "hub.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, binary); err != nil {
		t.Fatal(err)
	}
	diskOut, err := exec.CommandContext(t.Context(), binary, "launch-check", "--json").Output()
	if err != nil {
		t.Fatal(err)
	}
	var disk struct {
		Schema int `json:"hub_endpoint_schema"`
	}
	if err := json.Unmarshal(diskOut, &disk); err != nil || strconv.Itoa(disk.Schema) != installedSchema {
		t.Fatalf("disk contract=%s err=%v", diskOut, err)
	}
	currentUser, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	host := hostreg.Host{Name: "selected", SSH: currentUser.Username + "@disposable", EvenerPath: binary, ConfigPath: config}
	report := remoteEndpointFixture(config, "127.0.0.1:19371", "fixture-boot")
	report.HubStateRoot, report.Running.HubStateRoot, report.Running.PID = root, root, cmd.Process.Pid
	report.State, report.Reason, report.Running = "unavailable", "unreachable", nil
	endpoint := binary + " hub endpoint --json --config " + config
	fr := &fakeRunner{runFn: func(ctx context.Context, argv []string, _ io.Reader) ([]byte, error) {
		remote := strings.Join(argv[12:], " ")
		if remote == endpoint {
			return json.Marshal(report)
		}
		if strings.HasSuffix(remote, "; "+endpoint) {
			return json.Marshal(report)
		}
		switch remote {
		case "systemctl list-units --type=service --all --no-legend --plain", "systemctl --user list-units --type=service --all --no-legend --plain":
			return nil, nil
		}
		// Never dispatch a real service-manager operation or an SSH connection.
		if strings.Contains(remote, "systemctl") || strings.Contains(remote, "launchctl") || strings.Contains(remote, "kill") || strings.Contains(remote, "curl") {
			return nil, fmt.Errorf("forbidden external operation: %s", remote)
		}
		return exec.CommandContext(ctx, "/bin/sh", "-c", remote).CombinedOutput()
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{})
	got, err := m.resolveOwnedEndpoint(t.Context(), host, Preflight{OS: "linux", LaunchCheckKnown: true, HubEndpointSchema: 1})
	if originalSchema == "0" {
		if !errors.Is(err, ErrEndpointMigration) {
			t.Fatalf("old mapped image, new disk: err=%v", err)
		}
		t.Log("actual old Linux mapped image was refused despite capable replacement pathname")
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if got.pid != pid || got.supervisor.kind != supervisorNone {
		t.Fatalf("owner=%+v", got)
	}
	lockAfter, err := os.Stat(filepath.Join(root, "hub.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(lockBefore, lockAfter) {
		t.Fatal("observer changed lock inode")
	}
	t.Log("actual Linux lock PID, effective user, NUL argv, cwd, start ticks and original-image capability survived atomic pathname replacement; disk schema is 0, running image schema is 1")
}
