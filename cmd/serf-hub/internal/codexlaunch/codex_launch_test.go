package codexlaunch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"primeradiant.com/serf/appwire"
)

// syncBuffer collects the launcher's log the way the hub's stderr does: both
// of an app-server's pipes are scanned concurrently and write to the one sink.
type syncBuffer struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	want string
	seen chan struct{}
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n, err := b.buf.Write(p)
	if b.seen != nil && strings.Contains(b.buf.String(), b.want) {
		close(b.seen)
		b.seen = nil
	}
	return n, err
}

// await returns a channel closed once want has been logged, so a test waits on
// the forwarding it is asserting rather than on the clock.
func (b *syncBuffer) await(want string) <-chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.want = want
	b.seen = make(chan struct{})
	return b.seen
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// gatedLog holds the one forwarded line that carries hold until the test
// releases it, which is how a scanner is put where output goes missing: busy
// writing to the hub log at the moment the app-server exits. Only that line is
// held, so anything the other pipe's scanner has to say still lands and can be
// asserted on.
type gatedLog struct {
	syncBuffer
	hold    string
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (g *gatedLog) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte(g.hold)) {
		g.once.Do(func() {
			close(g.entered)
			<-g.release
		})
	}
	return g.syncBuffer.Write(p)
}

// os/exec closes a StdoutPipe as soon as Cmd.Wait sees the child exit — "It is
// thus incorrect to call Wait before all reads from the pipe have completed" —
// and this launch starts Wait in a goroutine while both scanners are still
// draining, because the readiness loop needs the exit signal. Whatever is still
// in the pipe when Wait closes it is gone, and since d35w the hub log is the
// only place a launched app-server's output can land: what gets lost is its
// dying words, the most valuable thing it ever writes (kata j27f).
//
// The loss needs a scanner to be busy elsewhere at the moment of exit, which is
// what the gate arranges: the app-server does not say its last words until the
// scanner is held inside a write to the hub log, and it is not released until
// the process has been reaped.
func TestCodexLaunchKeepsTheAppServersDyingWordsAcrossWait(t *testing.T) {
	const held = "codex: warning: reticulating splines"
	const dying = "codex: fatal: session store is corrupt"
	// The app-server speaks, waits to be told the scanner is held, then says
	// its last words and exits. Reading stdin is how it waits — the test closes
	// the write end to release it — so nothing here turns on a clock.
	cmd := exec.Command("/bin/sh", "-c", `printf '%s\n' "$1"; read -r gate; printf '%s\n' "$2"`, "sh", held, dying)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}

	l := NewCodexLauncher(nil)
	l.client = seedClient(http.StatusOK, nil)
	log := &gatedLog{hold: held, entered: make(chan struct{}), release: make(chan struct{})}
	l.logOutput = log
	l.process = func(string, ...string) launchProcess { return &execLaunchProcess{cmd: cmd} }

	// A configured listen address answers on the first readiness check, so the
	// launch returns while the app-server is still talking.
	launched, err := l.launchLocked(context.Background(), CodexLaunchConfig{ID: "live", Listen: "ws://127.0.0.1:4321"})
	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-log.entered:
	case <-time.After(10 * time.Second):
		t.Fatalf("the app-server's first line never reached the hub log: %q", log.String())
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	// Wait has returned, so it has already done whatever it does to the pipes.
	<-launched.Exited

	logged := log.await(dying)
	close(log.release)
	select {
	case <-logged:
	case <-time.After(10 * time.Second):
		t.Fatalf("the app-server's last words never reached the hub log: %q", log.String())
	}
	// The other half of the same lifecycle bug: a scanner whose pipe is closed
	// under it reports os.ErrClosed rather than reaching EOF, so an ordinary
	// exit files a read failure nobody can act on.
	if strings.Contains(log.String(), "ended early") {
		t.Fatalf("an ordinary app-server exit was reported as a read failure: %q", log.String())
	}
	// Owning the pipe puts the other half of that bargain on the launch: the
	// parent's copy of the child's write end has to go at Start, or the exit
	// that ends the app-server never becomes the EOF that ends the scan. A zero
	// byte write reports the descriptor's state without putting anything on the
	// pipe.
	childEnd, ok := launched.Cmd.Stdout.(*os.File)
	if !ok {
		t.Fatalf("app-server stdout = %T, want a pipe the launch owns", launched.Cmd.Stdout)
	}
	if _, err := childEnd.Write(nil); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("the launch still holds the child's end of its own stdout: %v", err)
	}
}

// Shutdown must wait for the scanners after Wait has reaped the process. The
// held write is the scanner's last step; process exit happens while it is
// held, so returning from Shutdown before the release would cut off the
// app-server output that is already in the pipe.
func TestCodexLauncherShutdownWaitsForPipeForwarders(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const held = "codex: warning: still forwarding"
		process := newSeedProcess(held+"\n", "")
		var log gatedLog
		log.hold = held
		log.entered = make(chan struct{})
		release := make(chan struct{})
		var releaseOnce sync.Once
		releaseLog := func() { releaseOnce.Do(func() { close(release) }) }
		log.release = release
		defer releaseLog()

		l := NewCodexLauncher(nil)
		l.client = seedClient(http.StatusOK, nil)
		l.logOutput = &log
		useSeedRuntime(l, process, 0, false)

		launched, err := l.launchLocked(context.Background(), CodexLaunchConfig{
			ID:     "live",
			Listen: "ws://127.0.0.1:4321",
		})
		if err != nil {
			t.Fatal(err)
		}
		l.Running["live"] = launched

		<-log.entered
		process.Exit()
		<-launched.Exited

		shutdownDone := make(chan error, 1)
		go func() { shutdownDone <- l.Shutdown(context.Background()) }()
		synctest.Wait()
		select {
		case err := <-shutdownDone:
			t.Fatalf("Shutdown returned before pipe forwarding finished: %v", err)
		default:
		}

		releaseLog()
		synctest.Wait()
		if err := <-shutdownDone; err != nil {
			t.Fatalf("Shutdown returned an error after pipe forwarding finished: %v", err)
		}
	})
}

// A codex app-server that fails after launch says so on the pipes the hub is
// scanning, and the hub owns both of them — nothing else can read that output,
// so a line the scanner drops is a line nobody will ever see. Every line that
// is not the endpoint announcement therefore reaches the hub log, attributed
// to the launch that produced it so one log carrying several app-servers stays
// readable (kata d35w).
func TestScanCodexEndpointLogsWhatIsNotAnEndpoint(t *testing.T) {
	endpoints := make(chan string, 4)
	var log syncBuffer
	// The first line is CRLF-framed and its expectation below is not: the
	// carriage return is framing, and a log line carrying one returns the
	// cursor over the prefix that says which app-server spoke.
	scanCodexEndpoint(
		strings.NewReader("codex: error: address already in use\r\n{\"endpoint\":\"ws://one:1\"}\nlisten ws://two:2.\n"),
		endpoints, launching(), &log, "[codex:live]")
	close(endpoints)

	var got []string
	for endpoint := range endpoints {
		got = append(got, endpoint)
	}
	if strings.Join(got, ",") != "ws://one:1,ws://two:2" {
		t.Fatalf("scanned endpoints = %v", got)
	}
	// Exact, not Contains: an announcement consumed as an endpoint must not
	// also be logged as prose.
	if want := "[codex:live] codex: error: address already in use\n"; log.String() != want {
		t.Fatalf("log = %q, want %q", log.String(), want)
	}
	// A launch whose config never went through the launcher's id
	// normalization still labels its lines as the codex app-server's.
	if got := codexLogPrefix("  "); got != "[codex]" {
		t.Fatalf("unnamed launch prefix = %q", got)
	}
}

// A line longer than the launch will hold — a stack dump, a serialized payload
// — used to end the scan of that pipe, and with the scan went every later line
// the app-server wrote. An abandoned pipe also fills, and then the app-server
// blocks writing to it, so one pathological line silenced the process as well
// as the log. The line is now cut at the bound and the rest of it consumed, so
// what an overlong line costs is its own tail and nothing else (kata jqbb).
func TestScanCodexEndpointKeepsReadingPastAnOverlongLine(t *testing.T) {
	const overrun = 4096
	endpoints := make(chan string, 4)
	var log syncBuffer
	scanCodexEndpoint(
		strings.NewReader(strings.Repeat("x", maxCodexLogLine+overrun)+"\ncodex: still here\n"),
		endpoints, launching(), &log, "[codex:live]")

	got := strings.Split(strings.TrimSuffix(log.String(), "\n"), "\n")
	if len(got) != 2 {
		t.Fatalf("log has %d lines, want the cut line and the line after it", len(got))
	}
	// A cut line says so and says how much went missing, so it cannot be read
	// as a whole line that happened to end there.
	marker := fmt.Sprintf(" [truncated: %d bytes dropped from this line]", overrun)
	kept, said := strings.CutSuffix(got[0], marker)
	if !said {
		t.Fatalf("cut line does not say what it dropped, it ends %q", tailOf(got[0]))
	}
	if want := "[codex:live] " + strings.Repeat("x", maxCodexLogLine); kept != want {
		t.Fatalf("kept %d bytes of the overlong line, want %d", len(kept), len(want))
	}
	if got[1] != "[codex:live] codex: still here" {
		t.Fatalf("line after the overlong one = %q", got[1])
	}
}

// tailOf keeps a failure message readable when the value under it is a line
// the size of the bound.
func tailOf(line string) string { return line[max(0, len(line)-120):] }

// Where the bound falls is the whole of what an overlong line costs, so the
// bytes either side of it are worth pinning: a line ending exactly on the bound
// is a whole line and says nothing about truncation, and one byte more is a cut
// line that names the byte it lost. An app-server that dies mid-line reaches
// the same place by a different route — the remainder is never terminated — and
// running out of line is not a read failure to report.
func TestScanCodexEndpointCutsOnlyWhatIsPastTheBound(t *testing.T) {
	const cutOne = " [truncated: 1 bytes dropped from this line]"
	tests := []struct {
		name       string
		content    int
		unfinished bool
		want       string
	}{
		{name: "one byte short of the bound", content: maxCodexLogLine - 1},
		{name: "exactly the bound", content: maxCodexLogLine},
		{name: "one byte past the bound", content: maxCodexLogLine + 1, want: cutOne},
		{name: "past the bound and never finished", content: maxCodexLogLine + 1, unfinished: true, want: cutOne},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			line := strings.Repeat("x", tt.content)
			if !tt.unfinished {
				line += "\n"
			}
			var log syncBuffer
			scanCodexEndpoint(strings.NewReader(line), make(chan string, 1), launching(), &log, "[codex:live]")

			want := "[codex:live] " + strings.Repeat("x", min(tt.content, maxCodexLogLine)) + tt.want + "\n"
			if log.String() != want {
				t.Fatalf("logged %d bytes ending %q, want %d bytes ending %q",
					len(log.String()), tailOf(log.String()), len(want), tailOf(want))
			}
		})
	}
}

// A read that failed and a pipe that reached its end look identical in a log
// that simply stops, and the second is the app-server going quiet while the
// first is the launch losing the ability to hear it. The launch says which
// happened (kata e1nh). Past jqbb the surviving case is a genuine read failure:
// an overlong line is no longer one of them.
func TestScanCodexEndpointSaysWhyItStoppedReading(t *testing.T) {
	endpoints := make(chan string, 4)
	var log syncBuffer
	failure := errors.New("input/output error")
	scanCodexEndpoint(
		io.MultiReader(strings.NewReader("codex: still here\n"), failingReader{failure}),
		endpoints, launching(), &log, "[codex:live]")

	// Exact: everything read before the failure still reaches the log, and the
	// failure is named once.
	want := "[codex:live] codex: still here\n[codex:live] app-server output ended early: " + failure.Error() + "\n"
	if log.String() != want {
		t.Fatalf("log = %q, want %q", log.String(), want)
	}
}

// failingReader is a pipe that has stopped being readable, the way a launch's
// end of one behaves once something has gone wrong with it.
type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

// The launch wires both of the app-server's pipes to the hub log, since the
// endpoint announcement itself arrives on either one and so does the failure
// that replaces it. Each case drives one pipe and leaves the other empty: the
// announcement that ends the launch is written after the diagnostic line by
// the same scanner, so a launch that has returned has already logged it.
func TestCodexLaunchForwardsAppServerOutputFromBothPipes(t *testing.T) {
	const diagnostic = "codex: warning: falling back to no sandbox"
	const output = diagnostic + "\n{\"endpoint\":\"ws://127.0.0.1:4567\"}\n"
	tests := []struct {
		name           string
		stdout, stderr string
	}{
		{name: "stdout", stdout: output},
		{name: "stderr", stderr: output},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := NewCodexLauncher(nil)
			l.client = seedClient(http.StatusOK, nil)
			var log syncBuffer
			l.logOutput = &log
			process := newSeedProcess(tt.stdout, tt.stderr)
			useSeedRuntime(l, process, 0, false)

			launched, err := l.launchLocked(context.Background(), CodexLaunchConfig{ID: "live"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = launched.process.Kill()
				<-launched.Exited
			})
			if launched.endpoint != "ws://127.0.0.1:4567" {
				t.Fatalf("discovered endpoint = %q", launched.endpoint)
			}
			if want := "[codex:live] " + diagnostic + "\n"; log.String() != want {
				t.Fatalf("log = %q, want %q", log.String(), want)
			}
		})
	}
}

// Once launchLocked has returned, nothing will ever receive from the endpoints
// channel again — and ParseCodexEndpoint accepts any line carrying a ws:// URL,
// so an app-server that merely mentions its own address ("client connected to
// ws://…") keeps offering endpoints to a wait that is gone. One line past the
// channel's four slots, a blocking send parks the scanner goroutine for the
// life of the hub: it stops reading that pipe, and the app-server's only log
// record dies with it, exactly when the app-server has something to say
// (kata e1nh). Post-launch, an endpoint announcement is just another line for
// the log.
func TestCodexLaunchKeepsForwardingAfterTheEndpointWaitIsGone(t *testing.T) {
	l := NewCodexLauncher(nil)
	l.client = seedClient(http.StatusOK, nil)
	var log syncBuffer
	l.logOutput = &log
	process := newSeedProcess("", "")
	appServer := seedProcessLiveStdout(t, process)
	useSeedRuntime(l, process, 0, false)

	// A configured listen address is ready on the first check, so this launch
	// returns without ever receiving from endpoints: what the scanner fills
	// afterwards is an empty buffer with no reader behind it.
	launched, err := l.launchLocked(context.Background(), CodexLaunchConfig{ID: "live", Listen: "ws://127.0.0.1:4321"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = launched.process.Kill()
		<-launched.Exited
	})

	// One endpoint-shaped line more than the channel can hold, then the line
	// that matters. Written as a single small write so a wedged scanner shows
	// up as a missing log line instead of as a blocked test.
	const dying = "codex: fatal: session store is corrupt"
	const chatter = 5
	logged := log.await(dying)
	var talk strings.Builder
	for i := range chatter {
		fmt.Fprintf(&talk, "client %d connected to ws://127.0.0.1:4321/session\n", i)
	}
	fmt.Fprintln(&talk, dying)
	if _, err := appServer.WriteString(talk.String()); err != nil {
		t.Fatal(err)
	}

	select {
	case <-logged:
	case <-time.After(10 * time.Second):
		t.Fatalf("app-server output stopped reaching the log after %d endpoint-shaped lines: %q", chatter, log.String())
	}
	if got := strings.Count(log.String(), "connected to ws://"); got != chatter {
		t.Fatalf("logged %d of %d post-launch endpoint lines: %q", got, chatter, log.String())
	}
}

// The other half of that contract: while the launch is still waiting, a full
// buffer is backpressure and nothing else. An app-server can name several
// addresses before the one that answers — the readiness wait tries them in
// turn — so an announcement dropped because four were already queued is an
// announcement the launch needed. The send waits for the wait to catch up.
func TestDeliverCodexEndpointWaitsForTheLaunchToCatchUp(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		endpoints := make(chan string, 1)
		endpoints <- "ws://first:1"
		delivered := make(chan bool, 1)
		go func() { delivered <- deliverCodexEndpoint(endpoints, launching(), "ws://second:2") }()

		// The bubble goes idle only once the sender is durably blocked, which
		// is the state under test: buffer full, launch not yet caught up. A
		// send that gave up on a full buffer would have answered by now.
		synctest.Wait()
		select {
		case taken := <-delivered:
			t.Fatalf("a full buffer ended the send early, taken=%v", taken)
		default:
		}

		if got := <-endpoints; got != "ws://first:1" {
			t.Fatalf("queued endpoint = %q", got)
		}
		if got := <-endpoints; got != "ws://second:2" {
			t.Fatalf("endpoint after the buffer drained = %q", got)
		}
		// An announcement the wait took is consumed, not also logged as prose.
		if !<-delivered {
			t.Fatal("delivered announcement reported as not taken")
		}
	})
}

// launching is the launch-done signal a scanner sees while the readiness wait
// is still there to receive endpoints: never closed.
func launching() <-chan struct{} { return make(chan struct{}) }

// seedProcessLiveStdout gives a seeded app-server a stdout it can still speak
// on after launchLocked has returned: a real pipe, the way the hub's own
// StdoutPipe behaves, so a test write never blocks on the scanner.
func seedProcessLiveStdout(t *testing.T, process *seedProcess) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	// Closing the write end is what ends the scanner, at EOF; the read end is
	// the scanner's for as long as it lives.
	t.Cleanup(func() { _ = w.Close() })
	process.stdout = r
	return w
}

// A ready-wait that never saw the app-server come up ended one of two
// unrelated ways, and the message must name which: the readiness budget
// elapsed, or the caller walked away. waitCtx derives from the caller's
// context, and every hub path into EnsureSource carries a live request context
// — r.Context() on the REST spawn, the websocket connection's ctx (which the
// keepalive cancels) on the RPC one — so a browser that navigates away
// mid-launch lands here with nothing slow having happened. Calling that a
// timeout sends an operator after a slow machine or a too-short launch
// timeout, neither of which is involved (kata f9hr).
//
// Both outcomes stay an appwire.HubLaunchError, the discriminator clients read
// to headline the failure as a session that would not start. The label is what
// changes; which family of failure this is does not. Either way the launch owns
// a process no caller can reach, so it must still be killed.
func TestCodexLaunchNamesWhatStoppedTheReadyWait(t *testing.T) {
	tests := []struct {
		name string
		// The context the hub hands the launch, already done the way this
		// case describes.
		callerCtx  func(*testing.T) context.Context
		wantLabel  string
		otherLabel string
	}{
		{
			name: "caller walked away",
			callerCtx: func(*testing.T) context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			wantLabel:  "canceled",
			otherLabel: "timed out",
		},
		{
			name: "deadline elapsed",
			callerCtx: func(t *testing.T) context.Context {
				ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				t.Cleanup(cancel)
				return ctx
			},
			wantLabel:  "timed out",
			otherLabel: "canceled",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := NewCodexLauncher(nil)
			l.client = seedClient(0, errors.New("not ready"))
			process := newSeedProcess("", "")
			l.process = func(string, ...string) launchProcess { return process }
			l.newTicker = func(time.Duration) launchTicker { return &seedTicker{ch: make(chan time.Time)} }

			_, err := l.launchLocked(tt.callerCtx(t), CodexLaunchConfig{Listen: "ws://127.0.0.1:1", Timeout: time.Hour})
			if err == nil {
				t.Fatal("expected a launch error")
			}
			if strings.Contains(err.Error(), tt.otherLabel) {
				t.Fatalf("launch reported as %q: %v", tt.otherLabel, err)
			}
			if !strings.Contains(err.Error(), tt.wantLabel) {
				t.Fatalf("error = %v, want it to say %q", err, tt.wantLabel)
			}
			if !isHubLaunchError(err) {
				t.Fatalf("error is not a hub-launch failure: %v", err)
			}
			select {
			case <-process.killed:
			default:
				t.Fatal("abandoned launch left the app-server running")
			}
		})
	}
}

func isHubLaunchError(err error) bool {
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		return false
	}
	data, ok := wire.Data.(appwire.ErrorData)
	return ok && data.SerfErrorInfo == appwire.ErrorHubLaunch
}
