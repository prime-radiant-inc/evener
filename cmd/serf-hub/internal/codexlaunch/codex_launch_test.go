package codexlaunch

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/appwire"
)

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
