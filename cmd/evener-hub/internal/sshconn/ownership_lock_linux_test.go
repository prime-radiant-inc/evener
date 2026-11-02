//go:build linux

package sshconn

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/build"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// These are injected observations, not kernel churn. Shell functions intercept
// only stat/awk: real stat observes the test-owned file and real awk filters
// numbered proc fixtures with the production predicate. Counters live outside
// command substitutions, so assertions count actual reads rather than SSH calls.
func TestRemoteSelectedLockRetryInjected(t *testing.T) {
	for _, shell := range []string{"/bin/sh", "/usr/bin/busybox"} {
		if _, err := os.Stat(shell); err != nil {
			t.Logf("optional shell %s unavailable: %v", shell, err)
			continue
		}
		t.Run(filepath.Base(shell), func(t *testing.T) {
			for _, tc := range []struct {
				name         string
				owners       []string
				wantPID      string
				reads        int
				readError    int
				statError    int
				replaceAt    int
				wantExitCode int
			}{
				{name: "first read success", owners: []string{"4242"}, wantPID: "4242", reads: 1},
				{name: "empty then owner", owners: []string{"", "4242"}, wantPID: "4242", reads: 2},
				{name: "third read success", owners: []string{"", "", "4242"}, wantPID: "4242", reads: 3},
				{name: "exhaustion cannot take fourth success", owners: []string{"", "", "", "4242"}, reads: 3},
				{name: "read error terminal", owners: []string{"4242", "4242"}, reads: 1, readError: 1, wantExitCode: 23},
				{name: "second read error terminal", owners: []string{"", "4242", "4242"}, reads: 2, readError: 2, wantExitCode: 23},
				{name: "initial stat error terminal", statError: 1, wantExitCode: 24},
				{name: "pre-read stat error terminal", statError: 2, wantExitCode: 24},
				{name: "acceptance stat error terminal", owners: []string{"4242", "4242"}, reads: 1, statError: 3, wantExitCode: 24},
				{name: "malformed cannot retry to success", owners: []string{"bogus", "4242"}, reads: 1},
				{name: "zero cannot retry to success", owners: []string{"0", "4242"}, reads: 1},
				{name: "negative cannot retry to success", owners: []string{"-1", "4242"}, reads: 1},
				{name: "multiple cannot retry to success", owners: []string{"4242\n5151", "4242"}, reads: 1},
				{name: "duplicate cannot manufacture uniqueness", owners: []string{"4242\n4242", "4242"}, reads: 1},
				{name: "blank output is not empty", owners: []string{"blank", "4242"}, reads: 1},
				{name: "replacement between attempts", owners: []string{"", "4242"}, reads: 1, replaceAt: 4, wantExitCode: 1},
				{name: "replacement at acceptance", owners: []string{"4242"}, reads: 1, replaceAt: 3, wantExitCode: 1},
			} {
				t.Run(tc.name, func(t *testing.T) {
					dir := t.TempDir()
					root := filepath.Join(dir, "selected root")
					if err := os.Mkdir(root, 0700); err != nil {
						t.Fatal(err)
					}
					lock := filepath.Join(root, "hub.lock")
					const sentinel = "test-owned lock bytes\n"
					for _, file := range []string{lock, filepath.Join(dir, "replacement")} {
						if err := os.WriteFile(file, []byte(sentinel), 0600); err != nil {
							t.Fatal(err)
						}
					}
					before, err := os.Stat(lock)
					if err != nil {
						t.Fatal(err)
					}
					var stat unix.Stat_t
					if err := unix.Stat(lock, &stat); err != nil {
						t.Fatal(err)
					}
					key := fmt.Sprintf("%02x:%02x:%d", unix.Major(stat.Dev), unix.Minor(stat.Dev), stat.Ino)
					for i, owners := range tc.owners {
						// Unrelated, shared and POSIX records must never satisfy this lookup.
						var rows strings.Builder
						fmt.Fprintf(&rows, "1: FLOCK ADVISORY WRITE 5151 00:00:1 0 EOF\n2: FLOCK ADVISORY READ 5151 %s 0 EOF\n3: POSIX ADVISORY WRITE 5151 %s 0 EOF\n", key, key)
						if owners != "" && owners != "blank" {
							for pid := range strings.SplitSeq(owners, "\n") {
								fmt.Fprintf(&rows, "4: FLOCK ADVISORY WRITE %s %s 0 EOF\n", pid, key)
							}
						}
						if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d.rows", i+1)), []byte(rows.String()), 0600); err != nil {
							t.Fatal(err)
						}
					}
					// Blank awk stdout deliberately tests byte-empty vs a nonempty
					// malformed response. Error fixtures also emit plausible evidence.
					prefix := fmt.Sprintf(`
stat() {
 n=0; if [ -f "$fixture/stats" ]; then read -r n < "$fixture/stats"; fi
 n=$((n+1)); printf '%%s\n' "$n" > "$fixture/stats"
 if [ "$n" = %d ]; then command stat "$@"; return 24; fi
 if [ "$n" = %d ]; then mv "$fixture/replacement" "$fixture/selected root/hub.lock" || return; fi
 command stat "$@"
}
awk() {
 n=0; if [ -f "$fixture/reads" ]; then read -r n < "$fixture/reads"; fi
 n=$((n+1)); printf '%%s\n' "$n" > "$fixture/reads"
 [ "$#" -eq 4 ] && [ "$4" = /proc/locks ] || return 25
 if [ "$n" = %d ]; then printf '4242\n'; return 23; fi
 if [ "$n" = %d ]; then printf '\n'; return; fi
 command awk "$1" "$2" "$3" "$fixture/$n.rows"
}
`, tc.statError, tc.replaceAt, tc.readError, indexSelectedLockBlank(tc.owners))
					calls := 0
					fr := &fakeRunner{runFn: func(ctx context.Context, argv []string, _ io.Reader) ([]byte, error) {
						calls++
						args := []string{"-c", prefix + strings.Join(argv[12:], " ")}
						if shell == "/usr/bin/busybox" {
							args = append([]string{"sh"}, args...)
						}
						cmd := exec.CommandContext(ctx, shell, args...)
						cmd.Env = append(selectedLockToolEnv(t), "PATH=/usr/bin:/bin", "fixture="+dir)
						return cmd.Output()
					}}
					m := newTestManager(t, testRegistry(t), fr, Options{})
					got, err := m.selectedLockPID(t.Context(), hostreg.Host{Name: "injected", SSH: "fixture"}, root)
					if got != tc.wantPID || (tc.wantPID == "" && !errors.Is(err, errOwnerEvidence)) || (tc.wantPID != "" && err != nil) {
						t.Errorf("PID=%q want=%q error=%v", got, tc.wantPID, err)
					}
					if tc.wantExitCode != 0 {
						var exit *exec.ExitError
						if !errors.As(err, &exit) || exit.ExitCode() != tc.wantExitCode {
							t.Errorf("error=%v want exit code %d", err, tc.wantExitCode)
						}
					}
					reads := 0
					if data, err := os.ReadFile(filepath.Join(dir, "reads")); err == nil {
						reads, err = strconv.Atoi(strings.TrimSpace(string(data)))
						if err != nil {
							t.Fatal(err)
						}
					} else if !errors.Is(err, os.ErrNotExist) {
						t.Fatal(err)
					}
					if reads != tc.reads || calls != 1 {
						t.Errorf("global reads=%d want=%d, transport calls=%d want=1", reads, tc.reads, calls)
					}
					after, statErr := os.Stat(lock)
					contents, readErr := os.ReadFile(lock)
					if statErr != nil || readErr != nil || string(contents) != sentinel || (tc.replaceAt == 0 && !os.SameFile(before, after)) {
						t.Fatalf("selected file changed: stat=%v read=%v bytes=%q", statErr, readErr, contents)
					}
				})
			}
		})
	}
}

func indexSelectedLockBlank(owners []string) int {
	for i, owner := range owners {
		if owner == "blank" {
			return i + 1
		}
	}
	return 0
}

func TestRemoteSelectedLockTransportAndCancellation(t *testing.T) {
	transportErr := errors.New("injected transport failure")
	for _, phase := range []string{"transport", "before", "after"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			fr := &fakeRunner{runFn: func(context.Context, []string, io.Reader) ([]byte, error) {
				calls++
				if phase == "transport" {
					return []byte("4242\n"), transportErr
				}
				cancel()
				return []byte("4242\n"), nil
			}}
			m := newTestManager(t, testRegistry(t), fr, Options{})
			if phase == "before" {
				cancel()
			}
			got, err := m.selectedLockPID(ctx, hostreg.Host{Name: "injected", SSH: "fixture"}, "/selected")
			wantErr, wantCalls := context.Canceled, 1
			switch phase {
			case "before":
				wantCalls = 0
			case "transport":
				wantErr = transportErr
				if !errors.Is(err, errOwnerEvidence) {
					t.Errorf("transport failure lost unavailable evidence type: %v", err)
				}
			}
			if got != "" || !errors.Is(err, wantErr) || calls != wantCalls {
				t.Fatalf("PID=%q error=%v calls=%d want error=%v calls=%d", got, err, calls, wantErr, wantCalls)
			}
		})
	}
}

// Real kernel locks and real tools, not canned proc rows. The helper only holds
// a disposable FLOCK while stdin remains open, then exits without a signal.
// Global /proc/locks can omit or duplicate evidence during ambient churn, so
// even the static success control needs explicit opt-in, not just our churn.
func TestRemoteSelectedLockRealTools(t *testing.T) {
	if testing.Short() || os.Getenv("EVENER_SSH_E2E") != "1" || os.Getenv("EVENER_SSH_E2E_CONTAINER") != "1" {
		t.Skip("actual kernel observations require EVENER_SSH_E2E=1 and EVENER_SSH_E2E_CONTAINER=1; Alpine also needs the cached image")
	}
	binary := buildSelectedLockHelper(t)
	t.Run("host", func(t *testing.T) {
		testSelectedLockRealTools(t, binary, "", t.TempDir())
	})
	t.Run("alpine", func(t *testing.T) {
		env := selectedLockToolEnv(t)
		docker := func(ctx context.Context, args ...string) *exec.Cmd {
			cmd := exec.CommandContext(ctx, "docker", args...)
			cmd.Env = env
			return cmd
		}
		name := fmt.Sprintf("evener-lock-proof-%d-%d", os.Getpid(), time.Now().UnixNano())
		t.Logf("owned disposable container=%s", name)
		t.Cleanup(func() {
			out, removeErr := docker(context.Background(), "rm", "-f", name).CombinedOutput()
			remaining, listErr := docker(context.Background(), "container", "ls", "--all", "--filter", "name=^/"+name+"$", "--format", "{{.ID}}").Output()
			if listErr != nil || len(bytes.TrimSpace(remaining)) != 0 {
				t.Errorf("owned container absence not established: remove=%v output=%s list=%v remaining IDs=%q", removeErr, out, listErr, remaining)
			}
		})
		args := []string{"run", "--pull=never", "--rm", "-d", "--network", "none", "--name", name, "--entrypoint", "/usr/bin/env", "evener-e2e-ssh:local"}
		args = append(args, selectedLockContainerEnv()...)
		args = append(args, "/bin/sh", "-c", "umask 077; mkdir -p /tmp/lock-proof/home /tmp/lock-proof/config /tmp/lock-proof/state /tmp/lock-proof/cache /tmp/lock-proof/run /tmp/lock-proof/tmp && exec sleep 120")
		out, err := docker(t.Context(), args...).CombinedOutput()
		if err != nil {
			t.Fatalf("start cached Alpine: %v: %s", err, out)
		}
		if out, err := docker(t.Context(), "cp", binary, name+":/tmp/lock-helper").CombinedOutput(); err != nil {
			t.Fatalf("upload lock holder: %v: %s", err, out)
		}
		inspect := append([]string{"exec", name, "/usr/bin/env"}, selectedLockContainerEnv()...)
		inspect = append(inspect, "/bin/sh", "-c", "cat /etc/alpine-release; /bin/busybox | head -n 1")
		if out, err := docker(t.Context(), inspect...).CombinedOutput(); err != nil {
			t.Fatalf("read unchanged Alpine capability: %v: %s", err, out)
		} else {
			t.Logf("actual unchanged container tools: %s", out)
		}
		testSelectedLockRealTools(t, "/tmp/lock-helper", name, "/tmp/selected-lock-proof")
	})
}

// Unlike hubtestenv.Redirect, this leaves the parent test environment alone.
// An allowlist scrubs every Evener override (and shell/Docker startup override).
// Resolve Go's usual cache defaults before replacing HOME/XDG, as the shared
// test environment helpers do, so builds keep the machine's warm caches.
func selectedLockToolEnv(t *testing.T) []string {
	t.Helper()
	cache := os.Getenv("GOCACHE")
	if cache == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			t.Fatal(err)
		}
		cache = filepath.Join(base, "go-build")
	}
	modules := os.Getenv("GOMODCACHE")
	if modules == "" {
		modules = filepath.Join(filepath.SplitList(build.Default.GOPATH)[0], "pkg", "mod")
	}
	root := t.TempDir()
	env := []string{"PATH=" + os.Getenv("PATH"), "GOCACHE=" + cache, "GOMODCACHE=" + modules, "GOPATH=" + build.Default.GOPATH}
	for _, item := range []struct{ key, dir string }{
		{"HOME", "home"}, {"XDG_CONFIG_HOME", "config"}, {"XDG_STATE_HOME", "state"},
		{"XDG_CACHE_HOME", "cache"}, {"XDG_RUNTIME_DIR", "run"}, {"TMPDIR", "tmp"},
	} {
		dir := filepath.Join(root, item.dir)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		env = append(env, item.key+"="+dir)
	}
	return env
}

func selectedLockContainerEnv() []string {
	return []string{"-i", "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=/tmp/lock-proof/home", "XDG_CONFIG_HOME=/tmp/lock-proof/config", "XDG_STATE_HOME=/tmp/lock-proof/state", "XDG_CACHE_HOME=/tmp/lock-proof/cache", "XDG_RUNTIME_DIR=/tmp/lock-proof/run", "TMPDIR=/tmp/lock-proof/tmp"}
}

func buildSelectedLockHelper(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	const source = `package main
import("fmt";"io";"os";"os/exec";"sync";"syscall";"time")
func main(){
 if err:=os.MkdirAll(os.Args[1],0700);err!=nil{panic(err)}
 f,err:=os.OpenFile(os.Args[1]+"/hub.lock",os.O_RDWR|os.O_CREATE,0600);if err!=nil{panic(err)};defer f.Close()
 if err:=syscall.Flock(int(f.Fd()),syscall.LOCK_EX);err!=nil{panic(err)}
 stat,err:=f.Stat();if err!=nil{panic(err)};sys:=stat.Sys().(*syscall.Stat_t)
 var files []*os.File
 done:=make(chan struct{});var wg sync.WaitGroup
 if len(os.Args)>2 && os.Args[2]=="churn" {
  for i:=0;i<512;i++ { p,err:=os.OpenFile(fmt.Sprintf("%s/%d.lock",os.Args[1],i),os.O_RDWR|os.O_CREATE,0600);if err!=nil{panic(err)};if err:=syscall.Flock(int(p.Fd()),syscall.LOCK_EX);err!=nil{panic(err)};files=append(files,p) }
  for worker:=0;worker<4;worker++ { wg.Add(1);go func(worker int){defer wg.Done();for {for i:=worker;i<len(files);i+=4 {select {case <-done:return;default:};fd:=int(files[i].Fd());if err:=syscall.Flock(fd,syscall.LOCK_UN);err!=nil{panic(err)};if err:=syscall.Flock(fd,syscall.LOCK_EX);err!=nil{panic(err)}}}}(worker) }
  wg.Add(1);go func(){defer wg.Done();for {select {case <-done:return;default:};if err:=exec.Command("/bin/true").Run();err!=nil{panic(err)};time.Sleep(time.Millisecond)}}()
 }
 fmt.Printf("%d %d %d %d\n",os.Getpid(),sys.Dev,sys.Ino,f.Fd())
 io.Copy(io.Discard,os.Stdin)
 close(done);wg.Wait();for _,p:=range files{p.Close()}
}`
	src, binary := filepath.Join(dir, "holder.go"), filepath.Join(dir, "holder")
	if err := os.WriteFile(src, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", binary, src)
	cmd.Env = append(selectedLockToolEnv(t), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build real kernel lock holder: %v: %s", err, out)
	}
	return binary
}

func testSelectedLockRealTools(t *testing.T, binary, container, root string) {
	t.Helper()
	env := selectedLockToolEnv(t)
	command := func(ctx context.Context, argv ...string) *exec.Cmd {
		var cmd *exec.Cmd
		if container != "" {
			args := append([]string{"exec", "-i", container, "/usr/bin/env"}, selectedLockContainerEnv()...)
			cmd = exec.CommandContext(ctx, "docker", append(args, argv...)...)
		} else {
			cmd = exec.CommandContext(ctx, argv[0], argv[1:]...)
		}
		cmd.Env = env
		return cmd
	}
	var selectedFD string
	start := func(dir, mode string) string {
		cmd := command(context.Background(), binary, dir, mode)
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
				t.Errorf("reap owned lock holder: %v", err)
			}
		})
		ready, err := bufio.NewReader(output).ReadString('\n')
		fields := strings.Fields(ready)
		if err != nil || len(fields) != 4 {
			t.Fatalf("real lock holder readiness=%q error=%v", ready, err)
		}
		if pid, err := strconv.Atoi(fields[0]); err != nil || pid <= 0 {
			t.Fatalf("invalid holder PID %q", fields[0])
		}
		selectedFD = fields[3]
		t.Logf("actual held root=%s pid/device/inode/fd=%s", dir, strings.TrimSpace(ready))
		return fields[0]
	}
	selected := root + "/selected"
	pid := start(selected, "hold")
	fd := selectedFD
	other := start(root+"/unrelated", "hold")
	if pid == other {
		t.Fatal("unrelated holder PID equals selected")
	}
	run := func(script string) ([]byte, error) {
		return command(t.Context(), "/bin/sh", "-c", script).CombinedOutput()
	}
	before, err := run("stat -Lc '%d %i %s %f' " + selected + "/hub.lock")
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr []byte
	var status error
	m := newTestManager(t, testRegistry(t), &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		cmd := command(t.Context(), "/bin/sh", "-c", strings.Join(argv[12:], " "))
		var errout bytes.Buffer
		cmd.Stderr = &errout
		stdout, status = cmd.Output()
		stderr = bytes.Clone(errout.Bytes())
		return stdout, status
	}}, Options{})
	host := hostreg.Host{Name: "selected", SSH: "disposable"}
	probe := func(phase string, count int, allowUnavailable bool) {
		selectedCount, exhausted, rejected := 0, 0, 0
		for i := range count {
			got, err := m.selectedLockPID(t.Context(), host, selected)
			if allowUnavailable && got == "" && errors.Is(err, errOwnerEvidence) && status == nil {
				if len(stdout) == 0 {
					exhausted++
				} else {
					rejected++
					if rejected == 1 {
						t.Logf("first rejected nonempty observation, status=%v stdout=%q stderr=%q", status, stdout, stderr)
					}
				}
				continue
			}
			if err != nil || got != pid {
				rows, _ := run("cat /proc/" + pid + "/fdinfo/" + fd + "; cat /proc/" + pid + "/stat; stat -Lc '%d %i %s %f' " + selected + "/hub.lock; cat /proc/locks")
				t.Fatalf("%s probe %d selected PID=%q want=%s error=%v\nraw status=%v stdout=%q stderr=%q\ncommand=%s\nindependent fdinfo/generation/stat/kernel=%s", phase, i, got, pid, err, status, stdout, stderr, selectedLockRemote(selected), rows)
			}
			selectedCount++
		}
		t.Logf("%s: selected=%d, exhausted empty=%d, rejected nonempty=%d of %d; finite retries do not guarantee discovery", phase, selectedCount, exhausted, rejected, count)
	}
	probe("static control", 1, false)
	heldBefore, err := run("cat /proc/" + pid + "/fdinfo/" + fd)
	if err != nil || !strings.Contains(string(heldBefore), "FLOCK") {
		t.Fatalf("independent selected held-lock evidence: %q (%v)", heldBefore, err)
	}
	if !testing.Short() && os.Getenv("EVENER_SSH_E2E") == "1" && os.Getenv("EVENER_SSH_E2E_CONTAINER") == "1" {
		churnPID := start(root+"/churn", "churn")
		t.Logf("opted-in observational churn PID=%s", churnPID)
		probe("512 unrelated locks plus process churn", 200, true)
	} else {
		t.Log("stress observation off by default; requires EVENER_SSH_E2E=1 and EVENER_SSH_E2E_CONTAINER=1")
	}
	heldAfter, err := run("cat /proc/" + pid + "/fdinfo/" + fd)
	if err != nil || !strings.Contains(string(heldAfter), "FLOCK") {
		t.Fatalf("selected holder lost its lock: %q (%v)", heldAfter, err)
	}
	if got, err := m.selectedLockPID(t.Context(), host, root+"/missing"); err == nil || got != "" {
		t.Fatalf("missing selected lock adopted unrelated owner: PID=%q error=%v", got, err)
	}
	if out, err := run("test ! -e " + root + "/missing/hub.lock && mkdir " + root + "/unheld && printf 'unheld sentinel' > " + root + "/unheld/hub.lock"); err != nil {
		t.Fatalf("prepare real unheld negative: %v: %s", err, out)
	}
	if got, err := m.selectedLockPID(t.Context(), host, root+"/unheld"); !errors.Is(err, errOwnerEvidence) || got != "" {
		t.Fatalf("unheld selected file adopted unrelated held lock: PID=%q error=%v", got, err)
	}
	if out, err := run("cat " + root + "/unheld/hub.lock"); err != nil || string(out) != "unheld sentinel" {
		t.Fatalf("unheld negative mutated: %q (%v)", out, err)
	}
	after, err := run("stat -Lc '%d %i %s %f' " + selected + "/hub.lock")
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("observer mutated existing lock: %q -> %q (%v)", before, after, err)
	}
	t.Logf("selected PID %s, unrelated PID %s not adopted for missing/unheld roots, existing inode unchanged; independent fdinfo before=%q after=%q", pid, other, heldBefore, heldAfter)
}
