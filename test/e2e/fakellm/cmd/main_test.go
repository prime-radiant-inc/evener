package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/test/e2e/fakellm"
)

// The driver's two documented modes are promises about ONE session's rounds:
// --rounds N ends a turn after N of them, and --background-job-until answers
// this session's first round with a background job and its second by going
// idle. scripts/e2e-webui-turn-controls.sh hands the operator a repeatable
// spawn call, so two sessions against one fake is the ordinary case, not an
// exotic one. These tests drive two of them.

// startDriver runs the standalone driver against a fake of the test's own and
// returns its base URL. The driver stops when the test does.
func startDriver(t *testing.T, hold time.Duration, rounds int, jobRelease string) string {
	t.Helper()
	srv, err := fakellm.NewOn("127.0.0.1:0")
	if err != nil {
		t.Fatalf("start fake provider: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := serve(ctx, srv, hold, rounds, jobRelease); err != nil {
			t.Errorf("driver: %v", err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		srv.Close()
		<-done
	})
	return srv.BaseURL()
}

// session drives the fake the way a real serf session does: the whole
// conversation is replayed on every request, with each answered round in it
// as an assistant message carrying the tool call and a tool message carrying
// its result. The shape is copied from a live run of the real stack
// (cmd/serf-hub/e2e_control_invariant_test.go): system, environment context,
// prompt, then assistant/tool pairs.
type session struct {
	t        *testing.T
	baseURL  string
	name     string
	messages []map[string]any
}

func newSession(t *testing.T, baseURL, name string) *session {
	t.Helper()
	return &session{t: t, baseURL: baseURL, name: name, messages: []map[string]any{
		{"role": "system", "content": "## Identity\n\nYou are serf."},
		{"role": "user", "content": "<environment_context>\ncwd: \"/tmp/workspace\"\n</environment_context>"},
		// Both sessions send the same prompt: the script's spawn call is
		// copy-pasted, so identical openings are the expected case and the
		// fake must not need them to differ to tell sessions apart.
		{"role": "user", "content": "read NOTES.md and keep working"},
	}}
}

// toolCall is what the fake answered a round with.
type toolCall struct {
	name string
	args map[string]any
	id   string
}

// round makes this session's next model request and records the answer in the
// conversation, exactly as the session loop would before its next round. A
// request that fails is fatal; use try where a cancelled request is expected.
func (s *session) round(ctx context.Context) toolCall {
	s.t.Helper()
	call, err := s.try(ctx)
	if err != nil {
		s.t.Fatalf("%s: %v", s.name, err)
	}
	return call
}

// try is round without the test plumbing, so it is safe to call from a
// goroutine and safe to abandon.
func (s *session) try(ctx context.Context) (toolCall, error) {
	body, err := json.Marshal(map[string]any{
		"model":    fakellm.ModelID,
		"messages": s.messages,
		"tools": []any{map[string]any{
			"type":     "function",
			"function": map[string]any{"name": "read_file", "parameters": map[string]any{"type": "object"}},
		}},
		"stream": false,
	})
	if err != nil {
		return toolCall{}, fmt.Errorf("marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return toolCall{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return toolCall{}, fmt.Errorf("model request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var decoded struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return toolCall{}, fmt.Errorf("decode response: %w", err)
	}
	if len(decoded.Choices) != 1 || len(decoded.Choices[0].Message.ToolCalls) != 1 {
		return toolCall{}, fmt.Errorf("want exactly one tool call, got %+v", decoded)
	}
	raw := decoded.Choices[0].Message.ToolCalls[0]
	args := map[string]any{}
	if err := json.Unmarshal([]byte(raw.Function.Arguments), &args); err != nil {
		return toolCall{}, fmt.Errorf("decode tool arguments %q: %w", raw.Function.Arguments, err)
	}

	s.messages = append(s.messages,
		map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
			"id":       raw.ID,
			"type":     "function",
			"function": map[string]any{"name": raw.Function.Name, "arguments": raw.Function.Arguments},
		}}},
		map[string]any{"role": "tool", "tool_call_id": raw.ID, "content": `{"ok":true}`},
	)
	return toolCall{name: raw.Function.Name, args: args, id: raw.ID}, nil
}

// TestRoundsAreCountedPerSession: --rounds N ends EACH session's Nth round,
// not the Nth round the fake has served across all of them.
func TestRoundsAreCountedPerSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	baseURL := startDriver(t, 0, 3, "")

	first := newSession(t, baseURL, "first")
	second := newSession(t, baseURL, "second")

	for round := 1; round <= 2; round++ {
		if got := first.round(ctx).name; got != "read_file" {
			t.Fatalf("first session round %d: tool %q, want read_file", round, got)
		}
	}

	// The fake has now served two rounds. The second session's first round is
	// its own round 1 -- with a global counter it is round 3 of 3 and the
	// second session's turn ends before it has done anything.
	for round := 1; round <= 2; round++ {
		if got := second.round(ctx).name; got != "read_file" {
			t.Errorf("second session round %d: tool %q, want read_file", round, got)
		}
	}
	if got := second.round(ctx).name; got != "communicate" {
		t.Errorf("second session round 3: tool %q, want communicate (--rounds 3 ends its own third round)", got)
	}
	if got := first.round(ctx).name; got != "communicate" {
		t.Errorf("first session round 3: tool %q, want communicate (--rounds 3 ends its own third round)", got)
	}
}

// TestBackgroundJobModeAppliesToEverySession: --background-job-until answers
// EACH session's first round with the background job, not just the first
// session's.
func TestBackgroundJobModeAppliesToEverySession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	baseURL := startDriver(t, 0, 20, "release-the-job")

	for _, name := range []string{"first", "second"} {
		s := newSession(t, baseURL, name)

		launch := s.round(ctx)
		if launch.name != "shell" {
			t.Errorf("%s session round 1: tool %q, want shell (the background job)", name, launch.name)
		} else if mode, _ := launch.args["mode"].(string); mode != "background" {
			t.Errorf("%s session round 1: shell mode %q, want background", name, mode)
		}

		idle := s.round(ctx)
		if idle.name != "communicate" {
			t.Errorf("%s session round 2: tool %q, want communicate (go idle holding the job)", name, idle.name)
		} else if endTurn, _ := idle.args["end_turn"].(bool); !endTurn {
			t.Errorf("%s session round 2: end_turn %v, want true", name, endTurn)
		}
	}
}

// TestToolCallIDsAreUnique: the id is how a transcript pairs a result with
// its call (internal/appprojector), so a fake that reuses one makes a
// fakellm-backed assertion measure the collision instead of the code.
func TestToolCallIDsAreUnique(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	baseURL := startDriver(t, 0, 20, "")

	seen := map[string]string{}
	for _, name := range []string{"first", "second"} {
		s := newSession(t, baseURL, name)
		for round := 1; round <= 3; round++ {
			id := s.round(ctx).id
			where := fmt.Sprintf("%s session round %d", name, round)
			if id == "" {
				t.Errorf("%s: tool call has no id", where)
				continue
			}
			if previous, ok := seen[id]; ok {
				t.Errorf("%s: tool call id %q was already used by %s", where, id, previous)
			}
			seen[id] = where
		}
	}
}

// syncBuffer collects the driver's log lines, which the driver writes from
// whichever goroutine is answering a round.
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

// TestASessionsHoldDoesNotBlockAnother: a round is HELD before it is
// answered, so a driver that takes them one at a time makes every other
// session wait out that hold before its request is even seen. The fixture's
// promise is that each session stays in flight for hold x rounds; that is
// only true if the holds overlap.
func TestASessionsHoldDoesNotBlockAnother(t *testing.T) {
	var logged syncBuffer
	previous := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(previous) })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// A hold far longer than the deadline below: the second session's round
	// can only be logged inside it if the two holds overlap.
	const hold = 30 * time.Second
	baseURL := startDriver(t, hold, 20, "")

	requestCtx, cancelRequests := context.WithCancel(ctx)
	defer cancelRequests()
	var wg sync.WaitGroup
	for _, name := range []string{"first", "second"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := newSession(t, baseURL, name)
			// These requests never come back: the driver is still holding
			// them when the test cancels them.
			_, _ = s.try(requestCtx)
		}()
	}

	deadline := time.Now().Add(5 * time.Second)
	held := 0
	for time.Now().Before(deadline) {
		held = strings.Count(logged.String(), "holding")
		if held >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if held < 2 {
		t.Errorf("only %d of 2 sessions' rounds were held within 5s of a %s hold; the second waits out the first", held, hold)
	}
	cancelRequests()
	wg.Wait()
}
