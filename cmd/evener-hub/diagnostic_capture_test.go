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

// lifecycleFields is the field schema of a lifecycle record. It pins the
// diagnostic contract: a record carrying a field the fixture does not know is
// a diagnostic change the test must not absorb silently.
var lifecycleFields = map[string]bool{
	"request_id": true, "operation": true, "session_id": true,
	"resolved_session_id": true, "stage": true, "state": true, "result": true,
	"error_class": true, "elapsed_ms": true, "stage_elapsed_ms": true,
	"pid": true, "exit_code": true, "tail_bytes": true, "tail_at_limit": true,
}

// resumeStages is every stage the resume path may record. A stage outside it is
// an unexpected record, not a legitimate addition to this fixture.
var resumeStages = map[string]bool{
	"request": true, "ownership": true, "lock_wait": true, "lock_held": true,
	"ownership_recheck": true, "discovery": true, "protocol_check": true,
	"owner_lookup": true, "request_preparation": true, "spawner_resume": true,
	"post_launch_discovery": true, "daemon_read": true,
}

// lifecycleDiagnostics parses the capture with the package's structured
// lifecycle parser and returns the records alongside the lines that are not
// lifecycle records. The shared assertions validate record correlation,
// numeric fields and stage pairing, so a malformed or truncated record fails
// here. Every record must be a resume record with a known field set and stage,
// so an unexpected record or field fails. The non-lifecycle lines are returned
// so the caller can assert that nothing unexpected was written.
func (c *hubDiagnosticCapture) lifecycleDiagnostics(t *testing.T) ([]map[string]string, []string) {
	t.Helper()
	lines := c.recorded()
	records := assertThreadLifecycleRecords(t, strings.Join(lines, "\n"))
	for _, record := range records {
		for field := range record {
			if !lifecycleFields[field] {
				t.Fatalf("unexpected lifecycle field %q in %#v", field, record)
			}
		}
		if record["operation"] != "resume" {
			t.Fatalf("lifecycle operation = %q, want resume: %#v", record["operation"], record)
		}
		if !resumeStages[record["stage"]] {
			t.Fatalf("unexpected lifecycle stage %q: %#v", record["stage"], record)
		}
	}
	var other []string
	for _, line := range lines {
		if !strings.HasPrefix(line, "[hub] lifecycle ") {
			other = append(other, line)
		}
	}
	return records, other
}

// requireErrorStages fails unless the completed error records name exactly the
// wanted stages, in order, each with error_class "failed" and operation
// "resume". Those fields are the diagnostic outcome; the rendered message is
// not consulted.
func requireErrorStages(t *testing.T, records []map[string]string, want []string) {
	t.Helper()
	var got []string
	for _, record := range records {
		if record["state"] != "complete" || record["result"] != "error" {
			continue
		}
		if record["error_class"] != "failed" {
			t.Fatalf("error lifecycle stage %q error_class = %q, want failed", record["stage"], record["error_class"])
		}
		got = append(got, record["stage"])
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("error lifecycle stages = %v, want %v", got, want)
	}
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
