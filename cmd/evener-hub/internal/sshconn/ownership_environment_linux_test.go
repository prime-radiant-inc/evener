//go:build linux

package sshconn

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

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
			for _, name := range []string{"sh", "od", "awk", "cat", "xargs", "grep"} {
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
				for _, name := range []string{"sh", "od", "awk", "cat", "xargs", "grep"} {
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

type ownerEnvironmentCase struct {
	name      string
	input     []byte
	want      []byte
	wantError bool
	duplicate bool
	inputPath string
	tools     map[string]string
	full      bool
	privacy   bool
}

// The explicit selected indices are independent of the production parser. No
// shell filter or decoder computes the expected bytes.
func ownerEnvironmentRecords(name string, records []string, selected ...int) ownerEnvironmentCase {
	tc := ownerEnvironmentCase{name: name}
	for _, record := range records {
		tc.input = append(tc.input, record...)
		tc.input = append(tc.input, 0)
	}
	for _, index := range selected {
		tc.want = append(tc.want, records[index]...)
		tc.want = append(tc.want, 0)
	}
	return tc
}

const (
	ownerPrivateSentinel  = "reader-private-sentinel-9f65"
	ownerSelectedSentinel = "reader-selected-sentinel-2a71"
)

func ownerEnvironmentCases() []ownerEnvironmentCase {
	allBytes := make([]byte, 255)
	for i := range allBytes {
		allBytes[i] = byte(i + 1)
	}
	duplicates := ownerEnvironmentRecords("duplicates", []string{"HOME=first", "OTHER=ignored", "HOME=second", "XDG_STATE_HOME=", "XDG_STATE_HOME=third"}, 0, 2, 3, 4)
	duplicates.duplicate = true
	manyRecords := make([]string, 1000)
	indices := make([]int, len(manyRecords))
	for i := range manyRecords {
		manyRecords[i] = "HOME=/record-" + strconv.Itoa(i)
		indices[i] = i
	}
	many := ownerEnvironmentRecords("many_records", manyRecords, indices...)
	many.duplicate = true
	privacy := ownerEnvironmentRecords("child_argv_private", []string{"PRIVATE=" + ownerPrivateSentinel + "\nHOME=/forged", "HOME=" + ownerSelectedSentinel}, 1)
	privacy.privacy = true
	cases := []ownerEnvironmentCase{
		ownerEnvironmentRecords("selected", []string{"HOME=/owned space\nsecond=part", "XDG_CONFIG_HOME=/config", "XDG_STATE_HOME=/state=a b", "EVENER_HUB_LISTEN_ADDR=0.0.0.0:0", "OTHER=" + ownerPrivateSentinel}, 0, 1, 2, 3),
		ownerEnvironmentRecords("absent", []string{"PRIVATE=" + ownerPrivateSentinel + "\nHOME=/forged\nXDG_STATE_HOME=/forged", "HOME_SUFFIX=no", "PREFIX_HOME=no", "HOM=no", "HOME =no", "XDG_CONFIG_HOMES=no", "EVENER_HUB_LISTEN_ADDR_EXTRA=no", "HOME"}),
		ownerEnvironmentRecords("empty_input", nil),
		ownerEnvironmentRecords("empty_values", []string{"HOME=", "XDG_CONFIG_HOME=", "XDG_STATE_HOME=", "EVENER_HUB_LISTEN_ADDR="}, 0, 1, 2, 3),
		duplicates,
		ownerEnvironmentRecords("all_non_nul_bytes", []string{"OTHER=" + string(allBytes), "HOME=" + string(allBytes), "XDG_CONFIG_HOME=" + string(allBytes), "XDG_STATE_HOME=" + string(allBytes), "EVENER_HUB_LISTEN_ADDR=" + string(allBytes)}, 1, 2, 3, 4),
		ownerEnvironmentRecords("literal_and_framing", []string{"PRIVATE=" + ownerPrivateSentinel + "\nEND\n72 79 77 69 61 0\nHOME=forged", "HOME=\r\n\t'\"\\$(not-a-command);`false`\nEND\n", "", "HOME_SUFFIX=\nEND\n"}, 1),
		ownerEnvironmentRecords("long_records", []string{"OTHER=" + strings.Repeat("q", 1<<20) + "\nHOME=forged", "HOME=" + strings.Repeat("x", 1<<20), "XDG_STATE_HOME=/after-long"}, 1, 2),
		many,
		privacy,
		{name: "real_proc", inputPath: "/proc/$$/environ", want: []byte("HOME=" + ownerSelectedSentinel + "\x00")},
		{name: "truncated_selected", input: []byte("HOME=/unfinished"), want: []byte("HOME=/unfinished"), wantError: true},
		{name: "truncated_unrelated", input: []byte("PRIVATE=" + ownerPrivateSentinel), wantError: true},
		{name: "truncated_after_record", input: []byte("HOME=/complete\x00PRIVATE=" + ownerPrivateSentinel), want: []byte("HOME=/complete\x00"), wantError: true},
		{name: "raw_marker_not_completion", input: []byte("HOME=/complete\x00END\n"), want: []byte("HOME=/complete\x00"), wantError: true},
		{name: "missing", inputPath: "./missing", wantError: true},
		{name: "closed_input", inputPath: "/proc/self/fd/9", wantError: true},
		// Opening a directory succeeds, but the real encoder gets a kernel read error.
		{name: "real_read_error", inputPath: "./tools", wantError: true},
	}
	// Fault wrappers keep parsing real. They either run the real tool before a
	// failing exit, or inject encoder bytes/status, never a selected parser result.
	complete := ownerEnvironmentRecords("upstream_after_complete", []string{"HOME=/complete", "OTHER=" + ownerPrivateSentinel}, 0)
	complete.wantError = true
	complete.tools = map[string]string{"od": "\"$REAL_OD\" \"$@\"\nexit 23\n"}
	cases = append(cases, complete,
		ownerEnvironmentCase{name: "upstream_mid_record", want: []byte("HOME=x"), wantError: true, tools: map[string]string{"od": "printf '72 79 77 69 61 120\\n'\nexit 23\n"}},
		ownerEnvironmentCase{name: "upstream_unavailable", wantError: true, tools: map[string]string{"od": "exit 127\n"}},
		ownerEnvironmentCase{name: "encoder_success_mid_record", want: []byte("HOME=x"), wantError: true, tools: map[string]string{"od": "printf '72 79 77 69 61 120\\n'\n"}},
	)
	for _, malformed := range []struct{ name, encoded string }{
		{"out_of_range", "256"}, {"negative", "-1"}, {"non_numeric", "oops"},
		{"duplicate_completion", "END\\nEND"}, {"after_completion", "END\\n72"},
	} {
		cases = append(cases, ownerEnvironmentCase{name: malformed.name, wantError: true, tools: map[string]string{"od": "printf '%b\\n' '" + malformed.encoded + "'\n"}})
	}
	for _, tool := range []string{"awk", "cat"} {
		tc := ownerEnvironmentRecords(tool+"_failure_after_output", []string{"HOME=/complete"}, 0)
		tc.wantError = true
		tc.tools = map[string]string{tool: "\"$REAL_" + strings.ToUpper(tool) + "\" \"$@\"\nexit 29\n"}
		cases = append(cases, tc)
	}
	for _, size := range []int{1, 1 << 20} {
		cases = append(cases, ownerEnvironmentCase{name: "real_output_failure_" + strconv.Itoa(size), input: []byte("HOME=" + strings.Repeat("x", size) + "\x00"), full: true, wantError: true})
	}
	return cases
}

type ownerEnvironmentExecutor func(*testing.T, map[string][]byte) ([]byte, []byte, error)

func testOwnerEnvironmentByteStream(t *testing.T, execute ownerEnvironmentExecutor) {
	t.Helper()
	for _, tc := range ownerEnvironmentCases() {
		t.Run(tc.name, func(t *testing.T) {
			inputPath := tc.inputPath
			if inputPath == "" {
				inputPath = "./environ"
			}
			// Substitute only the input-open path. All parsing, framing and status
			// handling execute the actual production-generated command. real_proc
			// additionally exercises its unchanged /proc/PID/environ path.
			remote := ownerEnvironmentRemote("$$")
			if strings.Count(remote, "/proc/$$/environ") != 1 {
				t.Fatal("expected one environment input path")
			}
			remote = strings.Replace(remote, "/proc/$$/environ", inputPath, 1)
			if tc.full {
				remote += " > /dev/full"
			}
			var probe strings.Builder
			probe.WriteString("exec 9<&-\n" + remote + "\nstatus=$?\n")
			files := map[string][]byte{"environ": tc.input}
			for tool, body := range tc.tools {
				files["tools/"+tool] = []byte("#!/bin/sh\n" + body)
			}
			if tc.privacy {
				files["private-patterns"] = []byte(ownerPrivateSentinel + "\n" + ownerSelectedSentinel + "\n")
				for _, tool := range []string{"od", "awk", "cat"} {
					files["tools/"+tool] = []byte("#!/bin/sh\n/bin/cat /proc/$$/cmdline > " + tool + ".argv\nexec \"$REAL_" + strings.ToUpper(tool) + "\" \"$@\"\n")
					probe.WriteString("test -s " + tool + ".argv || exit 90\ngrep -F -f private-patterns " + tool + ".argv > /dev/null\n[ \"$?\" -eq 1 ] || exit 91\n")
				}
			}
			probe.WriteString("exit \"$status\"\n")
			files["probe.sh"] = []byte(probe.String())
			// This script is a private file, not sh -c argv. Only sentinel values
			// become inherited environment. Never inspect the runner's environment.
			files["run.sh"] = []byte("umask 077\n" +
				"REAL_OD=$(command -v od); REAL_AWK=$(command -v awk); REAL_CAT=$(command -v cat)\n" +
				"export REAL_OD REAL_AWK REAL_CAT\nPATH=\"$PWD/tools:$PATH\"; export PATH\n" +
				"HOME=" + ownerSelectedSentinel + "; PRIVATE=" + ownerPrivateSentinel + "; export HOME PRIVATE\n" +
				"exec /bin/sh ./probe.sh\n")
			out, stderr, err := execute(t, files)
			if (err != nil) != tc.wantError {
				t.Fatalf("command error=%v, wantError=%t, stdout=%d bytes, stderr=%d bytes", err, tc.wantError, len(out), len(stderr))
			}
			if !bytes.Equal(out, tc.want) {
				t.Fatalf("selected bytes differ: got %d bytes, want %d", len(out), len(tc.want))
			}
			if bytes.Contains(out, []byte(ownerPrivateSentinel)) || bytes.Contains(stderr, []byte(ownerPrivateSentinel)) || bytes.Contains(stderr, []byte(ownerSelectedSentinel)) {
				t.Fatal("sentinel crossed a private boundary")
			}
			if err == nil {
				if len(stderr) != 0 {
					t.Fatalf("successful command wrote %d stderr bytes", len(stderr))
				}
				_, decodeErr := decodeOwnerEnvironment(out)
				if tc.duplicate {
					if !errors.Is(decodeErr, errOwnerEvidence) {
						t.Fatalf("duplicate decoder error=%v, want errOwnerEvidence", decodeErr)
					}
				} else if decodeErr != nil {
					t.Fatalf("decode selected environment: %v", decodeErr)
				}
			}
		})
	}
}

func TestRemoteOwnerEnvironmentByteStream(t *testing.T) {
	testOwnerEnvironmentByteStream(t, func(t *testing.T, files map[string][]byte) ([]byte, []byte, error) {
		t.Helper()
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, "tools"), 0o700); err != nil {
			t.Fatal(err)
		}
		for name, data := range files {
			if err := os.WriteFile(filepath.Join(root, name), data, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		cmd := exec.CommandContext(t.Context(), "/bin/sh", "./run.sh")
		cmd.Dir = root
		cmd.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
		cmd.WaitDelay = time.Second
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		return out, stderr.Bytes(), err
	})
}

// This opt-in path runs the same generated-command cases, not SSH or a deploy.
// It must use the cached unchanged image: never install or rebuild to hide a
// BusyBox feature difference. Default tests remain offline and Docker-free.
func TestRemoteOwnerEnvironmentAlpine(t *testing.T) {
	if testing.Short() || os.Getenv("EVENER_SSH_E2E") != "1" || os.Getenv("EVENER_SSH_E2E_CONTAINER") != "1" {
		t.Skip("requires EVENER_SSH_E2E=1 and EVENER_SSH_E2E_CONTAINER=1 without -short")
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	// No inherited Docker config, SSH override, provider variable or credential.
	cleanEnv := []string{"PATH=/usr/bin:/bin", "LC_ALL=C", "HOME=" + t.TempDir()}
	image := "evener-e2e-ssh:local"
	inspect := func() []byte {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), docker, "image", "inspect", image, "--format", "{{.Id}} {{.Os}}/{{.Architecture}}")
		cmd.Env = cleanEnv
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("inspect cached image: %v", err)
		}
		return out
	}
	before := inspect()
	t.Logf("actual cached image: %s", bytes.TrimSpace(before))
	testOwnerEnvironmentByteStream(t, func(t *testing.T, files map[string][]byte) ([]byte, []byte, error) {
		t.Helper()
		var archive bytes.Buffer
		tw := tar.NewWriter(&archive)
		if err := tw.WriteHeader(&tar.Header{Name: "tools/", Mode: 0o700, Typeflag: tar.TypeDir}); err != nil {
			t.Fatal(err)
		}
		names := make([]string, 0, len(files))
		for name := range files {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o700, Size: int64(len(files[name]))}); err != nil {
				t.Fatal(err)
			}
			if _, err := tw.Write(files[name]); err != nil {
				t.Fatal(err)
			}
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		name := fmt.Sprintf("evener-reader-%d-%d", os.Getpid(), time.Now().UnixNano())
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			// --rm normally did this. Also reap the exact owned container after a
			// cancelled Docker client; no unrelated container or process is touched.
			rm := exec.CommandContext(ctx, docker, "rm", "-f", name)
			rm.Env = cleanEnv
			_ = rm.Run()
			list := exec.CommandContext(ctx, docker, "ps", "-a", "--filter", "name=^/"+name+"$", "--format", "{{.Names}}")
			list.Env = cleanEnv
			out, err := list.Output()
			if err != nil || len(bytes.TrimSpace(out)) != 0 {
				t.Errorf("owned container cleanup failed: %v, remaining output=%d bytes", err, len(out))
			}
		})
		cmd := exec.CommandContext(t.Context(), "/usr/bin/env", "-u", "EVENER_SSH_E2E_HOST", docker,
			"run", "--rm", "--pull=never", "--name", name, "--network", "none", "--user", "65534:65534",
			"--cap-drop", "ALL", "--security-opt", "no-new-privileges", "-i", "--entrypoint", "/usr/bin/env", image,
			"-i", "PATH=/usr/bin:/bin", "LC_ALL=C", "/bin/sh", "-c",
			"umask 077; mkdir /tmp/reader-proof && cd /tmp/reader-proof && tar -xf - && exec /bin/sh ./run.sh")
		cmd.Env = cleanEnv
		cmd.Stdin = &archive
		cmd.WaitDelay = time.Second
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		return out, stderr.Bytes(), err
	})
	if after := inspect(); !bytes.Equal(before, after) {
		t.Fatal("cached image identity changed")
	}
}
