package codexlaunch

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
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

// A codex app-server that fails after launch says so on the pipes the hub is
// scanning, and the hub owns both of them — nothing else can read that output,
// so a line the scanner drops is a line nobody will ever see. Every line that
// is not the endpoint announcement therefore reaches the hub log, attributed
// to the launch that produced it so one log carrying several app-servers stays
// readable (kata d35w).
func TestScanCodexEndpointLogsWhatIsNotAnEndpoint(t *testing.T) {
	endpoints := make(chan string, 4)
	var log syncBuffer
	scanCodexEndpoint(
		strings.NewReader("codex: error: address already in use\n{\"endpoint\":\"ws://one:1\"}\nlisten ws://two:2.\n"),
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

// Scan() reports a clean end of output and a fatal read failure the same way,
// by returning false, and bufio.Scanner fails on any line past its 64KB token
// limit — a stack dump, a serialized payload. That ends the scan of that pipe,
// and with the scan goes every later line the app-server writes: the trailing
// line here never reaches the log. A record that simply stops reads as an
// app-server that went quiet, which is the one thing it does not mean, so the
// launch says why it stopped listening (kata e1nh).
func TestScanCodexEndpointSaysWhyItStoppedReading(t *testing.T) {
	endpoints := make(chan string, 4)
	var log syncBuffer
	scanCodexEndpoint(
		strings.NewReader(strings.Repeat("x", bufio.MaxScanTokenSize+1)+"\ncodex: still here\n"),
		endpoints, launching(), &log, "[codex:live]")

	// Exact: the record names the failure, and the 64KB line that caused it
	// is not dumped into the log in the name of reporting it.
	want := "[codex:live] app-server output ended early: " + bufio.ErrTooLong.Error() + "\n"
	if log.String() != want {
		t.Fatalf("log = %q, want %q", log.String(), want)
	}
}

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
