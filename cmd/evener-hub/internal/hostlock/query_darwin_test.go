//go:build darwin

package hostlock

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// This is a candidate-observation proof, not production IsHeld. In particular,
// do not add Linux's proposed OFD marker here: Darwin's existing flock is the
// only ownership operation whose visibility this experiment must establish.
func darwinFlockQuery(file *os.File) (syscall.Flock_t, error) {
	lock := syscall.Flock_t{Type: syscall.F_WRLCK, Whence: io.SeekStart, Start: 0, Len: 0}
	err := syscall.FcntlFlock(file.Fd(), syscall.F_GETLK, &lock)
	return lock, err
}

const darwinFlockTripwire = 5 * time.Second

func TestDarwinFlockQuery(t *testing.T) {
	if role := os.Getenv("EVENER_TEST_DARWIN_FLOCK_ROLE"); role != "" {
		darwinFlockChildMain(t, role, os.Getenv("EVENER_TEST_DARWIN_FLOCK_PATH"))
		return
	}

	path := filepath.Join(t.TempDir(), "hub.lock")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	inode, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := syscall.Sysctl("kern.version")
	if err != nil {
		t.Fatal(err)
	}
	var fs syscall.Statfs_t
	if err := syscall.Statfs(path, &fs); err != nil {
		t.Fatal(err)
	}
	var fsType []byte
	for _, c := range fs.Fstypename {
		if c == 0 {
			break
		}
		fsType = append(fsType, byte(c))
	}
	t.Logf("OS=%s architecture=%s kernel=%s temp_filesystem=%s path=%s", runtime.GOOS, runtime.GOARCH, kernel, fsType, path)
	t.Log("Evidence applies only to this kernel and temp filesystem, not macOS network filesystems")

	// Subtests share one inode, never unlinking or replacing it between owners.
	t.Run("same_process_inspection_preserves_owner", func(t *testing.T) {
		darwinRequireQuery(t, path, syscall.F_UNLCK)
		release, err := AcquireLock(path)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		darwinRequireQuery(t, path, syscall.F_WRLCK)
		observer := darwinStartChild(t, path, "query")
		darwinRequireLock(t, observer.receive(t), "queried", syscall.F_WRLCK)
		observer.wait(t, 0)

		unrelated, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := unrelated.Close(); err != nil {
			t.Fatal(err)
		}
		darwinRequireQuery(t, path, syscall.F_WRLCK)
		loser, err := AcquireLock(path)
		if loser != nil {
			defer loser()
			t.Fatal("same-process contender acquired while owner lives")
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			t.Fatalf("same-process contender: got %v, want EWOULDBLOCK", err)
		}
		darwinRequireRefused(t, path)
		darwinRequireQuery(t, path, syscall.F_WRLCK)
	})

	// A barrier receipt, not scheduling or a sleep, proves the observer is
	// parked before an independent starter attempts the same inode.
	for _, pause := range []string{"open", "free_query", "exclusive_control"} {
		t.Run("paused_observer_"+pause, func(t *testing.T) {
			darwinRequireQuery(t, path, syscall.F_UNLCK)
			observer := darwinStartChild(t, path, pause)
			barrier := observer.receive(t)
			if barrier.Phase != pause {
				t.Fatalf("observer barrier: %+v, want %s", barrier, pause)
			}
			if pause == "free_query" {
				darwinRequireLock(t, barrier, pause, syscall.F_UNLCK)
			}
			if pause == "exclusive_control" {
				// Real oracle control: an observer which actually owns flock
				// must prevent the very starter the query-only cases admit.
				darwinRequireRefused(t, path)
				observer.send(t, "resume")
				observer.wait(t, 0)
				darwinRequireQuery(t, path, syscall.F_UNLCK)
				starter := darwinRequireOwner(t, path)
				starter.send(t, "release")
				starter.wait(t, 0)
				return
			}

			starter := darwinRequireOwner(t, path)
			darwinRequireRefused(t, path)
			observer.send(t, "resume")
			// A free query is a snapshot before startup, not a promise about
			// later ownership. Re-query only in the open-only case.
			if pause == "open" {
				darwinRequireLock(t, observer.receive(t), "queried", syscall.F_WRLCK)
			}
			observer.wait(t, 0)
			darwinRequireQuery(t, path, syscall.F_WRLCK)
			darwinRequireRefused(t, path)
			starter.send(t, "release")
			starter.wait(t, 0)
			darwinRequireQuery(t, path, syscall.F_UNLCK)
		})
	}

	t.Run("normal_release_and_abrupt_exit_allow_next_starter", func(t *testing.T) {
		for _, command := range []string{"release", "crash"} {
			t.Run(command, func(t *testing.T) {
				// The parent holds no ownership descriptor. The owner opens
				// its flock only after exec, and passes no fd to another child.
				owner := darwinRequireOwner(t, path)
				darwinRequireQuery(t, path, syscall.F_WRLCK)
				darwinRequireRefused(t, path)
				exitCode := 0
				if command == "crash" {
					// SIGKILL cannot run the owner's deferred release.
					if err := owner.cmd.Process.Kill(); err != nil {
						t.Fatal(err)
					}
					exitCode = -1
				} else {
					owner.send(t, "release")
				}
				owner.wait(t, exitCode)
				darwinRequireQuery(t, path, syscall.F_UNLCK)
				next := darwinRequireOwner(t, path)
				darwinRequireQuery(t, path, syscall.F_WRLCK)
				next.send(t, "release")
				next.wait(t, 0)
				darwinRequireQuery(t, path, syscall.F_UNLCK)
				after, err := os.Stat(path)
				if err != nil || !os.SameFile(inode, after) {
					t.Fatalf("lock inode changed: stat=%v err=%v", after, err)
				}
			})
		}
	})
}

func darwinRequireQuery(t *testing.T, path string, want int16) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	lock, queryErr := darwinFlockQuery(file)
	closeErr := file.Close()
	if queryErr != nil || closeErr != nil {
		t.Fatalf("query=%v close=%v", queryErr, closeErr)
	}
	darwinRequireLock(t, darwinFlockMessage{Phase: "queried", Lock: lock}, "queried", want)
}

type darwinFlockMessage struct {
	Phase string          `json:"phase"`
	Lock  syscall.Flock_t `json:"lock"`
	Errno syscall.Errno   `json:"errno"`
}

func darwinRequireLock(t *testing.T, got darwinFlockMessage, phase string, want int16) {
	t.Helper()
	t.Logf("observation: phase=%s lock=%+v errno=%d", got.Phase, got.Lock, got.Errno)
	if got.Phase != phase || got.Errno != 0 || got.Lock.Type != want {
		t.Fatalf("observation=%+v, want phase=%s type=%d errno=0", got, phase, want)
	}
}

func darwinRequireOwner(t *testing.T, path string) *darwinFlockChild {
	t.Helper()
	child := darwinStartChild(t, path, "owner")
	if got := child.receive(t); got.Phase != "acquired" || got.Errno != 0 {
		t.Fatalf("starter: %+v, want acquired", got)
	}
	return child
}

func darwinRequireRefused(t *testing.T, path string) {
	t.Helper()
	child := darwinStartChild(t, path, "owner")
	got := child.receive(t)
	if got.Phase != "refused" || got.Errno != syscall.EWOULDBLOCK {
		t.Fatalf("contender: %+v, want refused with EWOULDBLOCK", got)
	}
	child.wait(t, 0)
	t.Logf("independent contender refused with errno=%d", got.Errno)
}

// Only the two control pipes are inherited. os.Open/AcquireLock descriptors
// are close-on-exec, and the crash owner acquires exclusively inside its child.
// Separate protocol pipes keep testing's stdout out of the structured results.
type darwinFlockChild struct {
	cmd     *exec.Cmd
	control *os.File
	events  *os.File
	decoder *json.Decoder
	done    chan error
	output  bytes.Buffer
	waited  bool
}

func darwinStartChild(t *testing.T, path, role string) *darwinFlockChild {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	controlRead, controlWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = controlWrite.Close() })
	defer controlRead.Close()
	eventsRead, eventsWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eventsRead.Close() })
	defer eventsWrite.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 2*darwinFlockTripwire)
	child := &darwinFlockChild{control: controlWrite, events: eventsRead, decoder: json.NewDecoder(eventsRead), done: make(chan error, 1)}
	child.cmd = exec.CommandContext(ctx, executable, "-test.run=^TestDarwinFlockQuery$", "-test.timeout=10s", "-test.count=1")
	child.cmd.Env = []string{
		"HOME=" + filepath.Dir(path), "TMPDIR=" + filepath.Dir(path),
		"EVENER_TEST_DARWIN_FLOCK_ROLE=" + role, "EVENER_TEST_DARWIN_FLOCK_PATH=" + path,
	}
	child.cmd.ExtraFiles = []*os.File{controlRead, eventsWrite}
	child.cmd.Stdout = &child.output
	child.cmd.Stderr = &child.output
	child.cmd.WaitDelay = time.Second
	if err := child.cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	go func() { child.done <- child.cmd.Wait() }()
	t.Cleanup(func() {
		defer cancel()
		if !child.waited {
			_ = child.cmd.Process.Kill()
			<-child.done // Always reap, including a failed assertion at a barrier.
			t.Logf("reaped child role=%s output=%s", role, child.output.String())
		}
	})
	return child
}

func (child *darwinFlockChild) receive(t *testing.T) darwinFlockMessage {
	t.Helper()
	if err := child.events.SetReadDeadline(time.Now().Add(darwinFlockTripwire)); err != nil {
		t.Fatal(err)
	}
	var message darwinFlockMessage
	if err := child.decoder.Decode(&message); err != nil {
		t.Fatalf("child protocol: %v", err)
	}
	return message
}

func (child *darwinFlockChild) send(t *testing.T, command string) {
	t.Helper()
	if err := child.control.SetWriteDeadline(time.Now().Add(darwinFlockTripwire)); err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(child.control).Encode(command); err != nil {
		t.Fatal(err)
	}
}

func (child *darwinFlockChild) wait(t *testing.T, wantExit int) {
	t.Helper()
	err := <-child.done // CommandContext and WaitDelay bound this join.
	child.waited = true
	if wantExit == 0 && err != nil {
		t.Fatalf("child exit: %v\n%s", err, child.output.String())
	}
	if got := child.cmd.ProcessState.ExitCode(); got != wantExit {
		t.Fatalf("child exit=%d, want %d: %v\n%s", got, wantExit, err, child.output.String())
	}
	if wantExit == -1 {
		status := child.cmd.ProcessState.Sys().(syscall.WaitStatus)
		if !status.Signaled() || status.Signal() != syscall.SIGKILL {
			t.Fatalf("child status=%v, want SIGKILL", status)
		}
	}
}

func darwinFlockChildMain(t *testing.T, role, path string) {
	control := os.NewFile(3, "control")
	defer control.Close()
	events := os.NewFile(4, "events")
	defer events.Close()
	send := func(message darwinFlockMessage) {
		if err := json.NewEncoder(events).Encode(message); err != nil {
			t.Fatal(err)
		}
	}
	receive := func() string {
		var command string
		if err := json.NewDecoder(control).Decode(&command); err != nil {
			t.Fatal(err)
		}
		return command
	}
	if role == "owner" {
		release, err := AcquireLock(path)
		if err != nil {
			var errno syscall.Errno
			if !errors.As(err, &errno) {
				t.Fatal(err)
			}
			send(darwinFlockMessage{Phase: "refused", Errno: errno})
			return
		}
		defer release()
		send(darwinFlockMessage{Phase: "acquired"})
		if command := receive(); command != "release" {
			t.Fatalf("owner command: %q", command)
		}
		return
	}

	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	query := func() darwinFlockMessage {
		lock, err := darwinFlockQuery(file)
		if err != nil {
			t.Fatal(err)
		}
		return darwinFlockMessage{Phase: "queried", Lock: lock}
	}
	switch role {
	case "query":
		send(query())
		return
	case "open":
		send(darwinFlockMessage{Phase: role})
	case "free_query":
		message := query()
		message.Phase = role
		send(message)
	case "exclusive_control":
		if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			t.Fatal(err)
		}
		send(darwinFlockMessage{Phase: role})
	default:
		t.Fatalf("observer role: %q", role)
	}
	if command := receive(); command != "resume" {
		t.Fatalf("observer command: %q", command)
	}
	if role == "open" {
		send(query())
	}
}
