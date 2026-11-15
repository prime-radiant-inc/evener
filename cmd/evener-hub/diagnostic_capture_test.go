package hub

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// hubDiagnosticCapture is installed as hubDiagnosticSink for one fixture. It
// records every line the hub writes there so the test can assert on the
// diagnostics a failure-injection fault must produce, and fail when an
// unexpected one appears instead of letting it leak into the test's output.
type hubDiagnosticCapture struct {
	mu    sync.Mutex
	lines []string
}

func (c *hubDiagnosticCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for line := range strings.SplitSeq(strings.TrimSuffix(string(p), "\n"), "\n") {
		if line != "" {
			c.lines = append(c.lines, line)
		}
	}
	return len(p), nil
}

// captureHubDiagnostics redirects the hub's diagnostic sink into a capture for
// the duration of the test.
func captureHubDiagnostics(t *testing.T) *hubDiagnosticCapture {
	t.Helper()
	capture := &hubDiagnosticCapture{}
	previous := hubDiagnosticSink
	hubDiagnosticSink = capture
	t.Cleanup(func() { hubDiagnosticSink = previous })
	return capture
}

func (c *hubDiagnosticCapture) recorded() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.lines...)
}

// rosterRefreshCapture records every roster refresh error while a fixture runs,
// so a test can prove a deliberately malformed rendezvous file reached the
// decoder. The wrapped refresh still does the real work.
type rosterRefreshCapture struct {
	mu   sync.Mutex
	errs []error
}

// captureRosterRefreshErrors wraps hubRosterRefresh for the duration of the
// test, recording what each refresh returned while still running the real one.
func captureRosterRefreshErrors(t *testing.T) *rosterRefreshCapture {
	t.Helper()
	capture := &rosterRefreshCapture{}
	previous := hubRosterRefresh
	hubRosterRefresh = func(ctx context.Context, r *hubcore.Roster) error {
		err := previous(ctx, r)
		capture.mu.Lock()
		capture.errs = append(capture.errs, err)
		capture.mu.Unlock()
		return err
	}
	t.Cleanup(func() { hubRosterRefresh = previous })
	return capture
}

// hasJSONSyntaxError reports whether one of the recorded refreshes failed on
// malformed JSON. The typed decoder error, not its wording, proves the
// fixture's deliberately malformed rendezvous file reached the decoder.
func (c *rosterRefreshCapture) hasJSONSyntaxError() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, err := range c.errs {
		if _, ok := errors.AsType[*json.SyntaxError](err); ok {
			return true
		}
	}
	return false
}
