package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/llm"
)

func TestAllFailed(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		failed []bool
		window int
		want   bool
	}{
		{"all failures in window", []bool{true, true, true, true}, 4, true},
		{"one success in window", []bool{true, true, false, true}, 4, false},
		{"success outside window is ignored", []bool{false, true, true, true}, 3, true},
		{"slice shorter than window", []bool{true, true}, 3, false},
		{"empty slice", nil, 3, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := allFailed(tc.failed, tc.window); got != tc.want {
				t.Fatalf("allFailed(%v, %d) = %v, want %v", tc.failed, tc.window, got, tc.want)
			}
		})
	}
}

// loopDetectionSession runs a session whose fake model reads one of the named
// files per round and then finishes. Files listed in present are created in the
// session directory (so reading them succeeds); any other name is absent, so
// reading it fails. It returns every loop-detection message the session emitted
// and the session's reasoning effort afterwards.
func loopDetectionSession(t *testing.T, window int, present []string, reads []string) ([]string, string, int) {
	t.Helper()

	dir := t.TempDir()
	for _, name := range present {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("hello\n"), 0o644); err != nil {
			t.Fatalf("WriteFile %s: %v", name, err)
		}
	}

	c := llm.NewClient()
	steps := make([]func(req llm.Request) llm.Response, 0, len(reads)+1)
	for i, name := range reads {
		args, err := json.Marshal(map[string]string{"file_path": filepath.Join(dir, name)})
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		call := llm.ToolCallData{
			ID:        "call" + string(rune('a'+i)),
			Name:      "read_file",
			Arguments: args,
			Type:      "function",
		}
		steps = append(steps, func(req llm.Request) llm.Response {
			return llm.Response{
				Message: llm.Message{
					Role:    llm.RoleAssistant,
					Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}},
				},
			}
		})
	}
	steps = append(steps, func(req llm.Request) llm.Response { return finalResponse("done") })
	c.Register(&fakeAdapter{name: "openai", steps: steps})

	enableLoop := true
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir),
		SessionConfig{
			EnableLoopDetection:   &enableLoop,
			LoopDetectionWindow:   window,
			MaxToolRoundsPerInput: 30,
			ReasoningEffort:       "low",
		})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	messages := make(chan []string, 1)
	go func() {
		var got []string
		for ev := range sess.Events() {
			if ev.Kind == events.EventLoopDetection {
				if data, ok := ev.Data.(events.LoopDetectionData); ok {
					got = append(got, data.Message)
				}
			}
		}
		messages <- got
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "test", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.mu.Lock()
	effort := sess.cfg.ReasoningEffort
	detections := sess.loopDetectionCount
	sess.mu.Unlock()
	sess.Close()

	return <-messages, effort, detections
}

func TestLoopDetection_MixedWindowKeepsStuckEscalation(t *testing.T) {
	t.Parallel()

	// Alternating failure/success fills a 6-wide window with an A-B pattern that
	// still contains real progress, so today's tiered steering stands.
	reads := []string{"absent.txt", "present.txt", "absent.txt", "present.txt", "absent.txt", "present.txt"}
	messages, _, _ := loopDetectionSession(t, 6, []string{"present.txt"}, reads)

	if len(messages) == 0 {
		t.Fatal("expected a loop detection for the alternating pattern")
	}
	joined := strings.Join(messages, "\n")
	if !strings.Contains(joined, "Your reasoning effort has been increased") {
		t.Fatalf("expected tier-1 stuck escalation, got %q", joined)
	}
	if strings.Contains(joined, "Repeating a failing call cannot make it succeed") {
		t.Fatalf("mixed window must not get the structural intervention, got %q", joined)
	}
}

func TestLoopDetection_AllFailingWindowGetsStructuralIntervention(t *testing.T) {
	t.Parallel()

	reads := []string{"absent.txt", "absent.txt", "absent.txt", "absent.txt", "absent.txt", "absent.txt"}
	messages, effort, detections := loopDetectionSession(t, 6, nil, reads)

	if len(messages) == 0 {
		t.Fatal("expected a loop detection for the all-failing pattern")
	}
	joined := strings.Join(messages, "\n")
	if !strings.Contains(joined, "Every one of the last 6 tool calls failed, and they repeat the same pattern: read_file.") {
		t.Fatalf("expected the structural intervention, got %q", joined)
	}
	if !strings.Contains(joined, "Repeating a failing call cannot make it succeed") ||
		!strings.Contains(joined, "Stop. Either change the arguments, or take a different approach to the goal.") {
		t.Fatalf("structural intervention text incomplete, got %q", joined)
	}
	if strings.Contains(joined, "Your reasoning effort has been increased") {
		t.Fatalf("failure loop must skip the tier-1 escalation, got %q", joined)
	}
	// The tier counter still advances through a failure loop, so a later mixed
	// loop escalates from the tier it actually reached rather than restarting.
	if detections == 0 {
		t.Fatal("loop detection count must still advance on the failure path")
	}
	if effort != "low" {
		t.Fatalf("reasoning effort = %q, want it unchanged at %q", effort, "low")
	}
}
