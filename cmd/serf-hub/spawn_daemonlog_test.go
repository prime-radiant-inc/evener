package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
)

// registeringFakeSerf is a stub daemon that says one thing on each stream,
// registers a rendezvous entry under sessionID, and lingers just long enough
// to look alive. runDir is baked in rather than read from the environment so
// the stub cannot pick up an ambient SERF_RUN_DIR from the machine running
// the test.
func registeringFakeSerf(runDir, sessionID, onStdout, onStderr string) string {
	return fmt.Sprintf(`#!/bin/sh
mkdir -p "%[1]s"
printf '%%s\n' '%[3]s'
printf '%%s\n' '%[4]s' >&2
cat > "%[1]s/$$.json" <<EOF
{"pid":$$,"address":"127.0.0.1:1","session_id":"%[2]s","started_at":"2999-01-01T00:00:00Z"}
EOF
sleep 1
`, runDir, sessionID, onStdout, onStderr)
}

// The hub log is shared by every daemon on the hub, and a daemon's lines
// arriving in it are what made attribution guesswork: "[serve] error: model
// returned empty response after 3 retries" sat as the last line of a hub log
// directly under a "[serve] listening…" line for a DIFFERENT session and read
// as the smoking gun for the session under investigation (kata vca1). A
// daemon's output now lives in a file of its own, named for the session that
// wrote it, and the hub log keeps only hub lines.
func TestSpawnedDaemonOutputStaysOutOfTheHubLog(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	runDir := filepath.Join(dir, "run")
	bin := filepath.Join(dir, "fake-serf")
	writeFakeSerf(t, bin, registeringFakeSerf(runDir, "033z7k96Nj0LLiLImAqa9s", "daemon says this on stdout", "daemon says this on stderr"))

	var hubLog bytes.Buffer
	entry, err := spawnDaemon(context.Background(), bin, runDir, hubcore.SpawnRequest{}, 10*time.Second, &hubLog)
	if err != nil {
		t.Fatalf("spawnDaemon: %v", err)
	}
	if entry.SessionID != "033z7k96Nj0LLiLImAqa9s" {
		t.Fatalf("rendezvous session id = %q", entry.SessionID)
	}
	for _, said := range []string{"daemon says this on stdout", "daemon says this on stderr"} {
		if strings.Contains(hubLog.String(), said) {
			t.Fatalf("daemon output reached the hub log:\n%s", hubLog.String())
		}
	}

	logPath := filepath.Join(runDir, "logs", "daemon-033z7k96Nj0LLiLImAqa9s.log")
	daemonLog := readEventually(t, logPath, "daemon says this on stderr")
	if !strings.Contains(daemonLog, "daemon says this on stdout") {
		t.Fatalf("daemon stdout missing from %s:\n%s", logPath, daemonLog)
	}
}

// The operator habit of reading a daemon's errors out of the hub log has to be
// replaced by something, so the one hub line about a spawn says where that
// daemon's words went.
func TestSpawnBannerNamesTheDaemonLogFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	runDir := filepath.Join(dir, "run")
	bin := filepath.Join(dir, "fake-serf")
	writeFakeSerf(t, bin, registeringFakeSerf(runDir, "033z7k96Nj0LLiLImAqa9s", "out", "err"))

	var hubLog bytes.Buffer
	entry, err := spawnDaemon(context.Background(), bin, runDir, hubcore.SpawnRequest{}, 10*time.Second, &hubLog)
	if err != nil {
		t.Fatalf("spawnDaemon: %v", err)
	}
	want := fmt.Sprintf("[hub] daemon session=033z7k96Nj0LLiLImAqa9s pid=%d log=%s\n",
		entry.PID, filepath.Join(runDir, "logs", "daemon-033z7k96Nj0LLiLImAqa9s.log"))
	if hubLog.String() != want {
		t.Fatalf("hub log:\n got %q\nwant %q", hubLog.String(), want)
	}
}

// A resumed session keeps its id, so it keeps its log: the second run appends
// to the first one's file instead of starting a new one an operator has to go
// find.
func TestResumedDaemonAppendsToTheSessionsOwnLog(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	runDir := filepath.Join(dir, "run")
	logPath := filepath.Join(runDir, "logs", "daemon-01JRESUME.log")

	for _, said := range []string{"first run", "second run"} {
		bin := filepath.Join(dir, "fake-serf-"+strings.ReplaceAll(said, " ", "-"))
		writeFakeSerf(t, bin, registeringFakeSerf(runDir, "01JRESUME", said, said+" on stderr"))
		var hubLog bytes.Buffer
		entry, err := resumeDaemon(context.Background(), bin, runDir, hubcore.ResumeRequest{SessionID: "01JRESUME"}, 10*time.Second, &hubLog)
		if err != nil {
			t.Fatalf("resumeDaemon(%s): %v", said, err)
		}
		want := fmt.Sprintf("[hub] daemon session=01JRESUME pid=%d log=%s\n", entry.PID, logPath)
		if hubLog.String() != want {
			t.Fatalf("hub log after %s:\n got %q\nwant %q", said, hubLog.String(), want)
		}
	}
	got := readEventually(t, logPath, "second run on stderr")
	if !strings.Contains(got, "first run") {
		t.Fatalf("resume truncated the session's earlier log:\n%s", got)
	}
}

// A hub-owned pipe on a daemon's stdout or stderr kills that daemon the moment
// the hub exits: the child's next write gets EPIPE, and Go raises SIGPIPE to
// the default handler for writes to fd 1 and 2. Spawned daemons must outlive a
// hub restart, so both streams have to be a real file the child inherits —
// which is also the only wiring os/exec passes through without interposing a
// pipe of its own (it does that for every io.Writer that is not an *os.File).
func TestDaemonStreamsAreInheritedFilesNotHubPipes(t *testing.T) {
	t.Parallel()
	runDir := filepath.Join(t.TempDir(), "run")
	dlog, err := openDaemonLog(runDir, "033z7k96Nj0LLiLImAqa9s")
	if err != nil {
		t.Fatalf("openDaemonLog: %v", err)
	}
	defer dlog.close()

	cmd := exec.Command("/bin/echo") //nolint:noctx // never started; this inspects the wiring
	dlog.attach(cmd)
	for name, w := range map[string]any{"stdout": cmd.Stdout, "stderr": cmd.Stderr} {
		f, ok := w.(*os.File)
		if !ok {
			t.Fatalf("daemon %s is a %T: os/exec will interpose a hub-owned pipe and SIGPIPE the daemon when the hub exits", name, w)
		}
		if f == os.Stderr || f == os.Stdout {
			t.Fatalf("daemon %s is the hub's own %s: its output belongs in the daemon's log file", name, name)
		}
	}
}

// A daemon whose output cannot be attributed is the whole problem this fixes,
// so a hub that cannot open the log refuses the launch rather than quietly
// pouring the daemon back into the shared log. The run dir is where the
// rendezvous file has to land too, so a run dir this broken fails the launch
// either way; failing here says which.
func TestSpawnFailsWhenTheDaemonLogCannotBeOpened(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	runDir := filepath.Join(dir, "run")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run: %v", err)
	}
	// A regular file where the log directory belongs: nothing can be created
	// underneath it.
	if err := os.WriteFile(filepath.Join(runDir, "logs"), []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write logs blocker: %v", err)
	}
	ran := filepath.Join(dir, "ran")
	bin := filepath.Join(dir, "fake-serf")
	writeFakeSerf(t, bin, "#!/bin/sh\ntouch "+ran+"\n")

	var hubLog bytes.Buffer
	_, err := spawnDaemon(context.Background(), bin, runDir, hubcore.SpawnRequest{}, 10*time.Second, &hubLog)
	if err == nil {
		t.Fatal("spawn succeeded with an unopenable daemon log")
	}
	if !strings.Contains(err.Error(), "daemon log") {
		t.Fatalf("error does not name the daemon log: %v", err)
	}
	if _, statErr := os.Stat(ran); statErr == nil {
		t.Fatal("the daemon was launched anyway, with nowhere to log")
	}
}

// A session id reaches the hub from a client on the resume path, so the log
// file it names has to stay inside the log directory no matter what the id
// says.
func TestDaemonLogNameCannotEscapeItsDirectory(t *testing.T) {
	t.Parallel()
	ids := []string{
		"../../../../etc/passwd",
		"..",
		"a/b",
		`a\b`,
		"033z7k96Nj0LLiLImAqa9s",
	}
	dir := t.TempDir()
	for _, id := range ids {
		name := daemonLogName(id)
		if got := filepath.Dir(filepath.Join(dir, name)); got != dir {
			t.Fatalf("session id %q names %q, which lands in %q instead of %q", id, name, got, dir)
		}
	}
	if want := "daemon-033z7k96Nj0LLiLImAqa9s.log"; daemonLogName("033z7k96Nj0LLiLImAqa9s") != want {
		t.Fatalf("an ordinary session id must keep its own name: got %q, want %q", daemonLogName("033z7k96Nj0LLiLImAqa9s"), want)
	}
}

// readEventually reads path until it contains want. The daemon writes on its
// own schedule; the rendezvous entry the hub waited for is written after these
// lines, so this is a short backstop against buffering, not a race.
func readEventually(t *testing.T, path, want string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last string
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			last = string(data)
			if strings.Contains(last, want) {
				return last
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never contained %q; last read (err=%v):\n%s", path, want, err, last)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
