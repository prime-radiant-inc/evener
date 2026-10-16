//go:build linux

package sshconn

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// Execute the production-generated filter with actual implementations. In
// particular, BusyBox awk does not implement GNU awk's NUL record separator.
func TestRemoteOwnerEnvironmentRealTools(t *testing.T) {
	for _, implementation := range []string{"host", "busybox"} {
		t.Run(implementation, func(t *testing.T) {
			tools := t.TempDir()
			busybox := ""
			if implementation == "busybox" {
				var err error
				busybox, err = exec.LookPath("busybox")
				if err != nil {
					t.Skip("BusyBox unavailable, real Alpine SSH run remains required")
				}
			}
			for _, name := range []string{"sh", "awk", "xargs", "grep"} {
				binary := busybox
				if binary == "" {
					var err error
					binary, err = exec.LookPath(name)
					if err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Symlink(binary, filepath.Join(tools, name)); err != nil {
					t.Fatal(err)
				}
			}
			for _, tc := range []struct {
				name string
				env  []string
				want string
			}{
				{"values", []string{
					"PRIVATE_TEST_SECRET=opaque\nHOME=/forged-home",
					"HOME=/owned space\nsecond=part",
					"XDG_CONFIG_HOME=",
					"XDG_STATE_HOME=/state=a b",
					"EVENER_HUB_LISTEN_ADDR=0.0.0.0:0",
					"HOME_SUFFIX=must-not-cross-boundary",
				}, "HOME=/owned space\nsecond=part\x00XDG_CONFIG_HOME=\x00XDG_STATE_HOME=/state=a b\x00EVENER_HUB_LISTEN_ADDR=0.0.0.0:0\x00"},
				{"absent", []string{"PRIVATE_TEST_SECRET=opaque\nHOME=/forged-home", "HOME_SUFFIX=not-a-home"}, ""},
				{"empty", []string{"HOME=", "EVENER_HUB_LISTEN_ADDR="}, "HOME=\x00EVENER_HUB_LISTEN_ADDR=\x00"},
				{"literal_bytes", []string{"HOME=/raw\xff\r\n\t=a", "PRIVATE_TEST_SECRET=opaque"}, "HOME=/raw\xff\r\n\t=a\x00"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					cmd := exec.CommandContext(t.Context(), filepath.Join(tools, "sh"), "-c", ownerEnvironmentRemote("$$"))
					cmd.Env = append([]string{"PATH=" + tools}, tc.env...)
					var stderr bytes.Buffer
					cmd.Stderr = &stderr
					out, err := cmd.Output()
					if err != nil {
						t.Fatalf("real filter failed: %v", err)
					}
					if bytes.Contains(out, []byte("opaque")) || bytes.Contains(stderr.Bytes(), []byte("opaque")) {
						t.Fatal("unrelated secret crossed filter boundary")
					}
					if string(out) != tc.want {
						t.Fatalf("NUL-separated selected values differ, got %d bytes, want %d", len(out), len(tc.want))
					}
				})
			}
			t.Run("child_argv_private", func(t *testing.T) {
				observed := t.TempDir()
				capture := filepath.Join(observed, "child-cmdlines")
				// These real child wrappers read their own kernel cmdline, then
				// exec the actual selected implementation without altering input.
				for _, name := range []string{"sh", "awk", "xargs", "grep"} {
					script := "#!/bin/sh\n/bin/cat /proc/$$/cmdline >> '" + capture + "'\nexec '" + filepath.Join(tools, name) + "' \"$@\"\n"
					if err := os.WriteFile(filepath.Join(observed, name), []byte(script), 0o700); err != nil {
						t.Fatal(err)
					}
				}
				cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", ownerEnvironmentRemote("$$"))
				cmd.Env = []string{"PATH=" + observed, "PRIVATE_TEST_SECRET=sentinel-child-argv-only", "HOME=/selected"}
				out, err := cmd.Output()
				if err != nil || string(out) != "HOME=/selected\x00" {
					t.Fatalf("observed real filter failed, error=%v", err)
				}
				argv, err := os.ReadFile(capture)
				if err != nil {
					t.Fatal(err)
				}
				if bytes.Contains(argv, []byte("sentinel-child-argv-only")) {
					t.Fatal("unrelated sentinel environment value became a real child's kernel-visible command argument")
				}
			})
			t.Run("missing", func(t *testing.T) {
				cmd := exec.CommandContext(t.Context(), filepath.Join(tools, "sh"), "-c", ownerEnvironmentRemote("2147483647"))
				cmd.Env = []string{"PATH=" + tools}
				if out, err := cmd.Output(); err == nil || len(out) != 0 {
					t.Fatal("missing process environment became successful selected values")
				}
			})
			t.Run("unreadable", func(t *testing.T) {
				executable, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				child := exec.Command(executable, "-test.run=^TestRemoteOwnerEnvironmentUnreadableHelper$")
				child.Env = []string{"EVENER_OWNER_ENV_HELPER=1", "HOME=/owned-but-unreadable"}
				stdin, err := child.StdinPipe()
				if err != nil {
					t.Fatal(err)
				}
				stdout, err := child.StdoutPipe()
				if err != nil {
					t.Fatal(err)
				}
				if err := child.Start(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					_ = stdin.Close()
					if err := child.Wait(); err != nil {
						t.Errorf("owned unreadable helper: %v", err)
					}
				})
				ready, err := bufio.NewReader(stdout).ReadString('\n')
				if err != nil || ready != "ready\n" {
					t.Fatalf("owned unreadable helper did not become ready: %v", err)
				}
				pid := strconv.Itoa(child.Process.Pid)
				if _, err := os.ReadFile("/proc/" + pid + "/environ"); !errors.Is(err, os.ErrPermission) {
					t.Fatalf("owned non-dumpable process did not deny environment read: %v", err)
				}
				cmd := exec.CommandContext(t.Context(), filepath.Join(tools, "sh"), "-c", ownerEnvironmentRemote(pid))
				cmd.Env = []string{"PATH=" + tools}
				if out, err := cmd.Output(); err == nil || len(out) != 0 {
					t.Fatal("unreadable process environment became successful selected values")
				}
			})
		})
	}
}

func TestRemoteOwnerEnvironmentUnreadableHelper(t *testing.T) {
	if os.Getenv("EVENER_OWNER_ENV_HELPER") != "1" {
		return
	}
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	fmt.Println("ready")
	_, _ = io.Copy(io.Discard, os.Stdin)
}

// Keep shell expansion out of the filter's values, including a literal quote.
func TestRemoteOwnerEnvironmentLiteralValues(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", ownerEnvironmentRemote("$$"))
	want := "HOME=/tmp/'$(not-a-command)'=literal\nHOME=/still-a-value"
	cmd.Env = []string{"PATH=/usr/bin:/bin", want}
	out, err := cmd.Output()
	if err != nil || string(out) != want+"\x00" || strings.Count(string(out), "\x00") != 1 {
		t.Fatalf("literal value or NUL boundary changed, error=%v", err)
	}
}
