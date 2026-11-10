package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/hooks"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/execsupport/shellquote"
	"primeradiant.com/evener/llm"
)

// TestStopFailure_FiresOnceAndNotStop proves a provider 429 ends the turn with
// error=rate_limit, fires StopFailure exactly once, fires no Stop, and keeps
// the salvaged draft.
//
// The failing round streams a draft and then dies mid-stream with a typed 429
// on every attempt, so the retry group's consume-phase streak rule ends it with
// a ProviderUnhealthyError wrapping that 429. Settlement therefore keeps the
// draft, and the mapping has to read the rate limit through the wrapper rather
// than call it unknown.
//
// The runner here has no SetEventCallback, while production wires one that
// records a TurnHookCompleted per hook. Production's history therefore ends
// TURN_FAILURE followed by one HOOK_COMPLETED per StopFailure hook; this test's
// ends at the TURN_FAILURE. Assertions that need the turn tail therefore walk
// the last non-hook turns rather than hardcode production's indices.
func TestStopFailure_FiresOnceAndNotStop(t *testing.T) {
	t.Parallel()
	const draft = "the plan so far: step one, step two"
	rateLimit := llm.ErrorFromHTTPStatus("openai", 429, "slow down", nil, nil)
	a := &scriptedStreamAdapter{
		provider: "openai",
		script: map[string]func(*llm.ChanStream){
			"primary": streamTextThenFail(draft, rateLimit),
		},
	}
	sess := settlementSession(t, a)

	dir := t.TempDir()
	capture := filepath.Join(dir, "stop-failure-input.jsonl")
	nameMatched := filepath.Join(dir, "stop-failure-runs.txt")
	wrongName := filepath.Join(dir, "stop-failure-wrong-name")
	stopMarker := filepath.Join(dir, "stop-ran")
	runner := hooks.NewRunner(nil, "")
	runner.Add(plugin.HookStopFailure, plugin.RegisteredHook{
		Matcher: "*", Type: "command", Timeout: 5,
		Command: "cat >> " + shellquote.Literal(capture),
	})
	runner.Add(plugin.HookStopFailure, plugin.RegisteredHook{
		Matcher: "rate_limit", Type: "command", Timeout: 5,
		Command: "printf 'run\\n' >> " + shellquote.Literal(nameMatched),
	})
	runner.Add(plugin.HookStopFailure, plugin.RegisteredHook{
		Matcher: "server_error", Type: "command", Timeout: 5,
		Command: "touch " + shellquote.Literal(wrongName),
	})
	runner.Add(plugin.HookStop, plugin.RegisteredHook{
		Matcher: "*", Type: "command", Timeout: 5,
		Command: "touch " + shellquote.Literal(stopMarker),
	})
	sess.hookRunner = runner
	drainSessionEvents(sess)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter and hooks; only fires on a genuine hang.
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "write the plan", nil); err == nil {
		t.Fatal("expected a provider error from ProcessInput")
	}
	hist := sessionHistory(sess)

	// The dispatched input names the failure rate_limit. This is the mutation
	// target: a mapping that answers "unknown" fails right here.
	lines := captureLines(t, capture)
	if len(lines) != 1 {
		t.Fatalf("StopFailure ran %d times, want exactly once\n%s", len(lines), strings.Join(lines, "\n"))
	}
	var got struct {
		HookEventName        string `json:"hook_event_name"`
		Error                string `json:"error"`
		ErrorDetails         string `json:"error_details"`
		LastAssistantMessage string `json:"last_assistant_message"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("StopFailure input is not JSON: %v\n%s", err, lines[0])
	}
	if got.HookEventName != "StopFailure" {
		t.Errorf("hook_event_name = %q, want %q", got.HookEventName, "StopFailure")
	}
	if got.Error != "rate_limit" {
		t.Errorf("error = %q, want %q", got.Error, "rate_limit")
	}
	if !strings.Contains(got.ErrorDetails, "429") {
		t.Errorf("error_details = %q, want the status %q", got.ErrorDetails, "429")
	}

	// The matcher target is the error name: the rate_limit hook fired once, the
	// server_error hook did not fire at all.
	if runs := captureLines(t, nameMatched); len(runs) != 1 {
		t.Fatalf("the rate_limit-matched hook ran %d times, want exactly once", len(runs))
	}
	if _, err := os.Stat(wrongName); !os.IsNotExist(err) {
		t.Fatalf("a server_error matcher fired for a rate limit; stat err = %v", err)
	}

	// The recorded failure text the conversation shows is last_assistant_message.
	tail := nonHookTurns(hist)
	failure := tail[len(tail)-1]
	if failure.Kind != schema.TurnFailure || failure.Error == nil {
		t.Fatalf("last turn = %s, want a TURN_FAILURE carrying its diagnostic", failure.Kind)
	}
	if got.LastAssistantMessage != failure.Error.Message {
		t.Errorf("last_assistant_message = %q, want the recorded failure text %q", got.LastAssistantMessage, failure.Error.Message)
	}

	// Stop does not fire: a failed turn never reaches deliverIfCommunicated.
	if _, err := os.Stat(stopMarker); !os.IsNotExist(err) {
		t.Fatalf("Stop fired on a failure turn; stat err = %v", err)
	}

	// The salvaged draft survives into the transcript.
	salvaged := tail[len(tail)-3]
	if salvaged.Message.Text() != draft {
		t.Errorf("salvaged turn text = %q, want the draft %q", salvaged.Message.Text(), draft)
	}
}

// nonHookTurns drops the TurnHookCompleted records a configured runner's
// SetEventCallback appends, so a test can assert on the turn tail a session
// without that callback builds.
func nonHookTurns(turns []schema.Turn) []schema.Turn {
	out := make([]schema.Turn, 0, len(turns))
	for _, turn := range turns {
		if turn.Kind == schema.TurnHookCompleted {
			continue
		}
		out = append(out, turn)
	}
	return out
}

func captureLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		// The hook never ran. An empty slice lets the caller report
		// "ran 0 times", which is the regression the assertion is about.
		return nil
	}
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var lines []string
	for line := range strings.SplitSeq(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// TestHookErrorDetails pins the StopFailure error_details format, so Task 9 can
// state it as a contract: status and provider code, comma-separated, each part
// present only when the error carries it.
func TestHookErrorDetails(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"status only", llm.ErrorFromHTTPStatus("p", 429, "slow", nil, nil), "status=429"},
		{"status and code", llm.ErrorFromHTTPStatus("p", 401, "bad key", map[string]any{
			"error": map[string]any{"code": "invalid_api_key"},
		}, nil), "status=401, code=invalid_api_key"},
		{"neither", errors.New("weird"), ""},
		{"non-HTTP provider error", llm.NewUnsupportedEndpointError("p", "no events", nil), ""},
	}
	for _, c := range cases {
		if got := hookErrorDetails(c.err); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
