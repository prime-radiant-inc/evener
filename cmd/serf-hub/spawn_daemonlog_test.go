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

// A failed resume must explain this launch, not quote the tail of the session
// history that happened to be in the file before it opened. The log remains an
// append-only account of every run; only the failure diagnostic is scoped to
// the bytes this resume wrote.
func TestFailedResumeReportsOnlyCurrentLaunchOutput(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	runDir := filepath.Join(dir, "run")
	const sessionID = "01JRESUME"
	const staleMarker = "STALE_OUTPUT_FROM_EARLIER_RUN"
	const currentStdout = "CURRENT_RESUME_DIAGNOSTIC_STDOUT"
	const currentStderr = "CURRENT_RESUME_DIAGNOSTIC_STDERR"
	logPath := filepath.Join(runDir, daemonLogDirName, daemonLogName(sessionID))
	stale := strings.Repeat(staleMarker+"\n", daemonLaunchOutputLimit/len(staleMarker)+100)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}
	if err := os.WriteFile(logPath, []byte(stale), 0o600); err != nil {
		t.Fatalf("seed earlier log: %v", err)
	}

	bin := filepath.Join(dir, "fake-serf")
	writeFakeSerf(t, bin, "#!/bin/sh\nprintf '%s\\n' '"+currentStdout+"'\nprintf '%s\\n' '"+currentStderr+"' >&2\nexit 42\n")

	var hubLog bytes.Buffer
	_, err := resumeDaemon(context.Background(), bin, runDir, hubcore.ResumeRequest{SessionID: sessionID}, 10*time.Second, &hubLog)
	if err == nil {
		t.Fatal("resume succeeded, want the fake daemon to fail before rendezvous")
	}
	for _, want := range []string{currentStdout, currentStderr} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("resume failure dropped current-launch output %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), staleMarker) {
		t.Fatalf("resume failure quoted stale output from an earlier run: %v", err)
	}
}

// A replacement resume must get a new inode before it records its launch
// offset. An older daemon can still hold the previous descriptor and append to
// it after the replacement has opened the session's path; those bytes must not
// displace the replacement's diagnostic tail.
func TestResumedDaemonLogExcludesOutputFromAnExistingWriter(t *testing.T) {
	t.Parallel()
	runDir := filepath.Join(t.TempDir(), "run")
	const sessionID = "01JRESUME"
	logPath := filepath.Join(runDir, daemonLogDirName, daemonLogName(sessionID))
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}
	if err := os.WriteFile(logPath, []byte("earlier run\n"), 0o600); err != nil {
		t.Fatalf("seed earlier log: %v", err)
	}

	existingWriter, err := os.OpenFile(logPath, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open existing writer: %v", err)
	}
	defer func() { _ = existingWriter.Close() }()

	dlog, err := openDaemonLog(runDir, sessionID)
	if err != nil {
		t.Fatalf("openDaemonLog: %v", err)
	}
	defer dlog.close()

	const currentDiagnostic = "CURRENT_REPLACEMENT_DIAGNOSTIC\n"
	if _, err := dlog.file.WriteString(currentDiagnostic); err != nil {
		t.Fatalf("write replacement diagnostic: %v", err)
	}
	oldOutput := strings.Repeat("OLD_DAEMON_OUTPUT\n", daemonLaunchOutputLimit/len("OLD_DAEMON_OUTPUT\n")+1)
	if _, err := existingWriter.WriteString(oldOutput); err != nil {
		t.Fatalf("write existing daemon output: %v", err)
	}

	got := dlog.tail(daemonLaunchOutputLimit)
	if !strings.Contains(got, currentDiagnostic) {
		t.Fatalf("replacement diagnostic was displaced by an existing writer: %q", got)
	}
	if strings.Contains(got, "OLD_DAEMON_OUTPUT") {
		t.Fatalf("existing daemon output leaked into replacement diagnostic: %q", got)
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

// A failed launch's log is adopted by nobody. The session id that would name
// it arrives only with the rendezvous entry the launch never got, so the file
// keeps the opaque daemon-pending-<random>.log name it was opened under — and
// the launch has already quoted its tail into the error, which is the only
// account of the failure anyone reads. Nothing in the hub sweeps
// <run-dir>/logs (rendezvous.List skips it; hubcore.Roster prunes rendezvous
// entries only), so every failed launch used to leave one more of these behind
// forever (kata dd8d). Adopt it or delete it; there is no third thing to do
// with it.
func TestFailedLaunchLeavesNoPendingDaemonLogBehind(t *testing.T) {
	t.Parallel()
	// Both ways a launch can fail before the daemon reports a session id: the
	// binary never starts, and the daemon starts, says why it is unhappy, and
	// exits before rendezvous.
	for _, tc := range []struct {
		name   string
		script string
		start  bool // the fake serf is written at all
		quotes string
	}{
		{
			name:   "daemon exits before rendezvous",
			script: "#!/bin/sh\necho 'serf serve: session creation: no such model' >&2\nexit 42\n",
			start:  true,
			quotes: "no such model",
		},
		{
			name:  "the daemon binary cannot be started",
			start: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			runDir := filepath.Join(dir, "run")
			bin := filepath.Join(dir, "fake-serf")
			if tc.start {
				writeFakeSerf(t, bin, tc.script)
			}

			var hubLog bytes.Buffer
			_, err := spawnDaemon(context.Background(), bin, runDir, hubcore.SpawnRequest{}, 10*time.Second, &hubLog)
			if err == nil {
				t.Fatal("spawn should have failed")
			}
			// The tail is taken before the file goes, and it has to stay that
			// way: deleting the log first would trade a leaked file for a
			// failure that says nothing about why.
			if tc.quotes != "" && !strings.Contains(err.Error(), tc.quotes) {
				t.Fatalf("failure dropped the daemon's own words: %v", err)
			}
			pending, globErr := filepath.Glob(filepath.Join(runDir, "logs", "daemon-pending-*.log"))
			if globErr != nil {
				t.Fatalf("glob pending logs: %v", globErr)
			}
			if len(pending) > 0 {
				t.Fatalf("failed launch left %d pending daemon log(s) nobody can name or read: %v", len(pending), pending)
			}
		})
	}
}

// The other half: a launch that succeeds owns its log, and a resume appends to
// it. Nothing about reaping the unadopted ones may touch a file a session has
// its name on — that file is an operator's account of a daemon that ran.
func TestAdoptedDaemonLogSurvivesTheLaunchThatMadeIt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	runDir := filepath.Join(dir, "run")
	bin := filepath.Join(dir, "fake-serf")
	writeFakeSerf(t, bin, registeringFakeSerf(runDir, "033z7k96Nj0LLiLImAqa9s", "out", "daemon says this on stderr"))

	var hubLog bytes.Buffer
	if _, err := spawnDaemon(context.Background(), bin, runDir, hubcore.SpawnRequest{}, 10*time.Second, &hubLog); err != nil {
		t.Fatalf("spawnDaemon: %v", err)
	}
	logPath := filepath.Join(runDir, "logs", "daemon-033z7k96Nj0LLiLImAqa9s.log")
	readEventually(t, logPath, "daemon says this on stderr")
}

// removeIfPending's guard is the only thing between a future caller — the
// resume failure path is the obvious candidate, and its log is the SESSION's,
// holding every earlier run of it — and deleting an operator's account of a
// daemon that ran. The spawn paths that call it today are all pre-adopt, so
// nothing else in this package can hold that guard to its name. Assert the
// contract directly, in all three states a log can be in.
func TestRemoveIfPendingSparesALogASessionOwns(t *testing.T) {
	t.Parallel()
	runDir := filepath.Join(t.TempDir(), "run")
	open := func(sessionID string) *daemonLog {
		t.Helper()
		l, err := openDaemonLog(runDir, sessionID)
		if err != nil {
			t.Fatalf("openDaemonLog(%q): %v", sessionID, err)
		}
		l.close()
		return l
	}

	named := open("033z7k96Nj0LLiLImAqa9s")
	named.removeIfPending()
	if _, err := os.Stat(named.path); err != nil {
		t.Fatalf("a log opened under a session's own name was reaped: %v", err)
	}

	adopted := open("")
	adopted.adopt("01JADOPTEDSESSIONID000")
	adopted.removeIfPending()
	if _, err := os.Stat(adopted.path); err != nil {
		t.Fatalf("a log a session has adopted was reaped: %v", err)
	}

	orphan := open("")
	orphan.removeIfPending()
	if _, err := os.Stat(orphan.path); !os.IsNotExist(err) {
		t.Fatalf("a log no session ever claimed survived removeIfPending (stat err=%v)", err)
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

// seedDaemonLogHistory writes want bytes of fixed-width, self-numbering lines
// to a session's log, as an earlier run of that session would have left them,
// and returns the line function and the highest number it got to. Every line is
// the same length, so a line that comes back a different length came back torn.
func seedDaemonLogHistory(t *testing.T, logPath string, want int) (line func(int) string, last int) {
	t.Helper()
	line = func(n int) string {
		return fmt.Sprintf("[serve] earlier run line %06d %s\n", n, strings.Repeat("x", 200))
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}
	var seeded bytes.Buffer
	for seeded.Len() < want {
		seeded.WriteString(line(last))
		last++
	}
	last--
	if err := os.WriteFile(logPath, seeded.Bytes(), 0o600); err != nil {
		t.Fatalf("seed log history: %v", err)
	}
	return line, last
}

// A resume keeps its session's id, so it keeps and appends to that session's
// own log (kata vca1) — and nothing ever trimmed that file, so a session
// resumed daily for months held every line its daemon had ever written, with
// only deleting the session to end it (kata rcxy). A launch now carries at most
// daemonLogRetainedBytes of history in: the newest lines stay, the oldest go,
// and what stays is whole lines rather than a torn prefix.
func TestResumingASessionBoundsTheHistoryItsLogCarriesIn(t *testing.T) {
	t.Parallel()
	runDir := filepath.Join(t.TempDir(), "run")
	const sessionID = "033z7k96Nj0LLiLImAqa9s"
	logPath := filepath.Join(runDir, daemonLogDirName, daemonLogName(sessionID))
	line, last := seedDaemonLogHistory(t, logPath, 3*daemonLogRetainedBytes)

	dlog, err := openDaemonLog(runDir, sessionID)
	if err != nil {
		t.Fatalf("openDaemonLog: %v", err)
	}
	defer dlog.close()

	// Measured before this run writes anything: the bound is on what a launch
	// inherits, which is the only part of the file the hub gets to decide.
	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("stat log: %v", err)
	}
	if info.Size() > daemonLogRetainedBytes {
		t.Fatalf("resume carried %d bytes of earlier runs into this one, past the %d byte cap", info.Size(), daemonLogRetainedBytes)
	}

	const thisRun = "[serve] this run's very first line\n"
	if _, err := dlog.file.WriteString(thisRun); err != nil {
		t.Fatalf("write this run's line: %v", err)
	}
	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.HasSuffix(string(got), thisRun) {
		t.Fatalf("this run appends to the log it inherited; its first line is not at the end")
	}
	if !strings.Contains(string(got), line(last)) {
		t.Fatalf("the previous run's newest line did not survive the trim")
	}
	if strings.Contains(string(got), line(0)) {
		t.Fatalf("the oldest line survived a trim whose whole job was to drop it")
	}

	// A trimmed log has to say so, or it reads as the session's complete
	// history and an operator counts the absence of a line as evidence.
	kept := strings.SplitAfter(string(got), "\n")
	if !strings.Contains(kept[0], "earlier output dropped") {
		t.Fatalf("a trimmed log does not admit to being trimmed; it opens with %q", kept[0])
	}
	if len(kept[1]) != len(line(0)) || !strings.HasPrefix(kept[1], "[serve] earlier run line ") {
		t.Fatalf("the oldest retained line came back torn: %q", kept[1])
	}
}

// The trim is a launch-time act and has to stay one. A daemon's log is the
// account of the incident it is in the middle of, so trimming it while that
// daemon writes cuts the evidence out from under the crash being investigated
// — and a trim that replaces the file also strands the descriptor the daemon
// is holding, so everything it says afterwards goes nowhere at all.
//
// This runs the production handle lifecycle: the hub opens the log, the child
// inherits a descriptor of its own, and the hub drops its own the moment the
// child is started (SpawnDaemon, ResumeDaemon) while the daemon writes on past
// the cap. Every byte this run wrote has to be in the file.
func TestATrimNeverCutsTheRunItOpenedFor(t *testing.T) {
	t.Parallel()
	runDir := filepath.Join(t.TempDir(), "run")
	const sessionID = "01JLOUDSESSION0000000"
	logPath := filepath.Join(runDir, daemonLogDirName, daemonLogName(sessionID))
	seedDaemonLogHistory(t, logPath, 2*daemonLogRetainedBytes)

	dlog, err := openDaemonLog(runDir, sessionID)
	if err != nil {
		t.Fatalf("openDaemonLog: %v", err)
	}
	child, err := os.OpenFile(logPath, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("inherit child descriptor: %v", err)
	}
	defer func() { _ = child.Close() }()

	first := "[serve] this run's first word\n"
	if _, err := child.WriteString(first); err != nil {
		t.Fatalf("write first: %v", err)
	}
	// Well past the cap, as a daemon in trouble would.
	shout := strings.Repeat("[serve] retrying\n", 2*daemonLogRetainedBytes/len("[serve] retrying\n"))
	if _, err := child.WriteString(shout); err != nil {
		t.Fatalf("write shout: %v", err)
	}
	dlog.close()
	last := "[serve] this run's dying words\n"
	if _, err := child.WriteString(last); err != nil {
		t.Fatalf("write last: %v", err)
	}

	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(got), first) {
		t.Fatalf("this run's first line was trimmed away while the run was still going")
	}
	if !strings.HasSuffix(string(got), last) {
		t.Fatalf("this run's last line is not at the end of its own log")
	}
	if len(got) < len(first)+len(shout)+len(last) {
		t.Fatalf("log is %d bytes; this run alone wrote %d, so something cut it mid-incident", len(got), len(first)+len(shout)+len(last))
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
