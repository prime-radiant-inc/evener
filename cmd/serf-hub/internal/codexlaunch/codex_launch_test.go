package codexlaunch

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/appwire"
)

// syncBuffer collects the launcher's log the way the hub's stderr does: both
// of an app-server's pipes are scanned concurrently and write to the one sink.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
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
		endpoints, &log, "[codex:live]")
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
