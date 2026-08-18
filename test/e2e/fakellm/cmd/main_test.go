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
	t                *testing.T
	baseURL          string
	name             string
	messages         []map[string]any
	affinityHeaderID string // if non-empty, send this value as session-affinity headers
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
	// Send affinity headers if configured, mimicking what serf does when send_session_affinity_headers is true.
	if s.affinityHeaderID != "" {
		req.Header.Set("session_id", s.affinityHeaderID)
		req.Header.Set("x-client-request-id", s.affinityHeaderID)
		req.Header.Set("x-session-affinity", s.affinityHeaderID)
	}
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

// newTurn is what the operator does from the browser: send a message to a
// session whose previous turn has ended. The conversation carries on; only
// the turn is new.
func (s *session) newTurn(text string) {
	s.messages = append(s.messages, map[string]any{"role": "user", "content": text})
}

// stopInFlightRound is what Stop does to the fake: the operator cancels the
// model request the driver is holding, so the driver answers a round whose
// answer never reaches the transcript. The session's next request replays the
// last round that WAS recorded.
//
// Measured against a live hub and daemon: a turn stopped during round 2 comes
// back replaying round 1's tool-call id, and the id the fake minted for round
// 2 is never seen again. Discarding the answer here reproduces the transcript
// half of that event; the other half — the request aborted while the driver is
// still holding the round — is exercised for real by
// TestACancelledRoundLeavesTheNextTurnItsOwnCount.
func (s *session) stopInFlightRound(ctx context.Context) {
	s.t.Helper()
	recorded := append([]map[string]any(nil), s.messages...)
	s.round(ctx)
	s.messages = recorded
}

// compact rewrites this session's conversation the way serf's context
// compaction does: the older turns are folded into a user-role checkpoint and
// the most recent messages are kept verbatim
// (agent/internal/contextmgr/context_manager.go, PreserveRecentTurns).
//
// It keeps the exact shape measured against a live hub + daemon after a
// thread/compact/start: system prompt, a "[CONTEXT SUMMARY]" user message,
// and a preserved tail whose FIRST message is an orphaned tool result -- the
// cut landed between the newest assistant tool call and its result, so the
// assistant message that made the call is gone and the result that carries
// its id is not.
func (s *session) compact() {
	tail := s.messages[len(s.messages)-1:]
	s.messages = append([]map[string]any{
		s.messages[0],
		{"role": "user", "content": "[CONTEXT SUMMARY]\nthe session did some things\n[END SUMMARY]"},
	}, tail...)
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

// TestConcurrentRoundLinesNameTheirSession: rounds are counted per session, so
// sessions genuinely overlap and the log interleaves them. fakellm.log is the
// only view scripts/e2e-webui-turn-controls.sh gives an operator of what
// reached the model, and two `round 1` lines back to back with nothing between
// them and their sessions is not a view of anything.
//
// The name has to be a token the session's own transcript carries, not a label
// the log invents: the tool-call id of the first round this driver saw from it,
// which the session replays on every request after that (fakellm.Call's
// PreviousToolCallID). That is what makes `grep call_fakellm_3 fakellm.log`
// return one session's run and nothing else.
func TestConcurrentRoundLinesNameTheirSession(t *testing.T) {
	var logged syncBuffer
	previous := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(previous) })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// --rounds 3 so each session's third round is an "ending the turn" line as
	// well: that line has to name its session too.
	baseURL := startDriver(t, 0, 3, "")

	// Both sessions run at once, on their own goroutines, so the driver is
	// answering them from separate goroutines of its own and the log lines
	// really do interleave -- the condition the operator is stuck with.
	names := make([]string, 2)
	var wg sync.WaitGroup
	for i, name := range []string{"first", "second"} {
		wg.Go(func() {
			s := newSession(t, baseURL, name)
			for round := 1; round <= 3; round++ {
				call, err := s.try(ctx)
				if err != nil {
					t.Errorf("%s session round %d: %v", name, round, err)
					return
				}
				if round == 1 {
					names[i] = call.id
				}
			}
		})
	}
	wg.Wait()

	if names[0] == "" || names[1] == "" {
		t.Fatalf("each session must report its first round's tool-call id, got %q and %q", names[0], names[1])
	}
	if names[0] == names[1] {
		t.Fatalf("both sessions were handed the tool-call id %q; there is no handle to name them by", names[0])
	}

	text := logged.String()
	for i, id := range names {
		for round := 1; round <= 3; round++ {
			want := fmt.Sprintf("session %s round %d: holding", id, round)
			if !strings.Contains(text, want) {
				t.Errorf("no log line %q: round %d of session %d cannot be told from the other session's", want, round, i+1)
			}
		}
		want := fmt.Sprintf("session %s round 3: ending the turn", id)
		if !strings.Contains(text, want) {
			t.Errorf("no log line %q: the turn-ending line does not name its session", want)
		}
	}

	// And nothing the driver says about a round may be anonymous. With two
	// sessions in flight an unnamed line is unattributable by construction, so
	// one line missed is the whole defect back again.
	for line := range strings.SplitSeq(text, "\n") {
		if strings.Contains(line, " round ") && !strings.Contains(line, "session ") {
			t.Errorf("round line names no session: %q", line)
		}
	}
}

// TestASessionsNameOutlivesItsTurns: the script tells the operator to watch one
// session for roughly hold x rounds seconds, and its turns start and end
// underneath that. The name identifies the SESSION, so a turn boundary must not
// re-mint it -- otherwise grepping the log for it returns one turn rather than
// the run, which is the thing the operator was told to watch.
func TestASessionsNameOutlivesItsTurns(t *testing.T) {
	var logged syncBuffer
	previous := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(previous) })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	baseURL := startDriver(t, 0, 2, "")

	s := newSession(t, baseURL, "long-lived")
	first := s.round(ctx)
	if first.name != "read_file" {
		t.Fatalf("turn 1 round 1: tool %q, want read_file", first.name)
	}
	if got := s.round(ctx).name; got != "communicate" {
		t.Fatalf("turn 1 round 2: tool %q, want communicate (--rounds 2 ends it)", got)
	}
	// The operator sends the next message. Same session, new turn.
	s.newTurn("now do the other thing")
	if got := s.round(ctx).name; got != "read_file" {
		t.Fatalf("turn 2 round 1: tool %q, want read_file", got)
	}
	if got := s.round(ctx).name; got != "communicate" {
		t.Fatalf("turn 2 round 2: tool %q, want communicate", got)
	}

	// Each line appears once per turn, under the SAME name. A name minted per
	// turn rather than per session gives the second turn a different one, and
	// every count here drops to 1.
	text := logged.String()
	for _, want := range []string{
		fmt.Sprintf("session %s round 1: holding", first.id),
		fmt.Sprintf("session %s round 2: holding", first.id),
		fmt.Sprintf("session %s round 2: ending the turn", first.id),
	} {
		if got := strings.Count(text, want); got != 2 {
			t.Errorf("log has %d lines %q, want 2 (one per turn): the name changed when the turn did", got, want)
		}
	}
}

// TestBackgroundJobModeAppliesToEverySession: --background-job-until answers
// EACH session's first round with the background job, not just the first
// session's.
func TestBackgroundJobModeAppliesToEverySession(t *testing.T) {
	var logged syncBuffer
	previous := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(previous) })

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

		// This mode's own log lines have to be attributable too: the operator
		// releases one job file and watches several sessions wake from it.
		text := logged.String()
		for _, want := range []string{
			fmt.Sprintf("session %s round 1: launching a background job", launch.id),
			fmt.Sprintf("session %s round 2: ending the turn so the session goes idle", launch.id),
		} {
			if !strings.Contains(text, want) {
				t.Errorf("%s session: no log line %q", name, want)
			}
		}
	}
}

// TestTurnsAreCountedFromTheirOwnStart: --rounds N is a promise about a TURN,
// which is what the flag help, the package doc and the script's banner all
// say. The turn under test here is the one that follows a Stop -- the very
// control this harness exists to exercise -- so the second turn does not
// begin on a multiple of N and a count that only ever grows cannot pass by
// coincidence.
func TestTurnsAreCountedFromTheirOwnStart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	baseURL := startDriver(t, 0, 3, "")

	s := newSession(t, baseURL, "stopped")
	if got := s.round(ctx).name; got != "read_file" {
		t.Fatalf("turn 1 round 1: tool %q, want read_file", got)
	}
	// The operator hits Stop during round 2 and sends something else. The turn
	// that follows is a turn of its own.
	s.stopInFlightRound(ctx)
	s.newTurn("stop that and do this instead")

	for round := 1; round <= 2; round++ {
		if got := s.round(ctx).name; got != "read_file" {
			t.Errorf("turn 2 round %d: tool %q, want read_file", round, got)
		}
	}
	if got := s.round(ctx).name; got != "communicate" {
		t.Errorf("turn 2 round 3: tool %q, want communicate (--rounds 3 ends every turn on its own third round)", got)
	}
}

// TestACancelledRoundLeavesTheNextTurnItsOwnCount: what Stop actually does to
// the driver is cancel the model request MID-HOLD -- the daemon aborts the
// HTTP request while the answer goroutine is still waiting out --hold. That
// goroutine must not go on to mutate the session's shared state: by the time
// its hold expires the session's next turn has already begun, and an endTurn
// side effect landing then zeroes the new turn's count, handing it extra
// rounds (measured against HEAD: --rounds 3, cancel round 3 mid-hold, next
// turn runs 4). stopInFlightRound cannot see this -- it waits for the answer
// and only discards the transcript -- so this test cancels for real, with a
// real hold.
func TestACancelledRoundLeavesTheNextTurnItsOwnCount(t *testing.T) {
	var logged syncBuffer
	previous := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(previous) })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const hold = 300 * time.Millisecond
	baseURL := startDriver(t, hold, 3, "")

	s := newSession(t, baseURL, "stopped")
	// Round 1's id is this session's name in the driver's log. It is read here,
	// from a round that completed before anything below runs concurrently, so
	// the wait further down is keyed on a value the session has already
	// reported -- a wait on a name minted by the very round being waited for
	// would have nothing to match until the line it is waiting for exists.
	first := s.round(ctx)
	if first.name != "read_file" {
		t.Fatalf("turn 1 round 1: tool %q, want read_file", first.name)
	}
	if got := s.round(ctx).name; got != "read_file" {
		t.Fatalf("turn 1 round 2: tool %q, want read_file", got)
	}

	// Round 3 -- the round that would end the turn -- is cancelled while the
	// driver holds it, exactly as Stop cancels an in-flight model request.
	// Nothing of it reaches the transcript.
	recorded := append([]map[string]any(nil), s.messages...)
	requestCtx, cancelRequest := context.WithCancel(ctx)
	defer cancelRequest()
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		_, _ = s.try(requestCtx)
	}()
	waitForLog(t, &logged, fmt.Sprintf("session %s round 3: holding", first.id))
	cancelRequest()
	<-requestDone
	s.messages = recorded

	// The operator sends something else. The turn that follows must get its
	// full --rounds of its own, even while the cancelled round's hold is
	// still running out.
	s.newTurn("stop that and do this instead")
	for round := 1; round <= 2; round++ {
		if got := s.round(ctx).name; got != "read_file" {
			t.Errorf("turn 2 round %d: tool %q, want read_file", round, got)
		}
	}
	if got := s.round(ctx).name; got != "communicate" {
		t.Errorf("turn 2 round 3: tool %q, want communicate (the cancelled round must not touch the new turn's count)", got)
	}
}

// waitForLog blocks until the driver has logged substr, so a test can cancel
// a round only once the driver is genuinely holding it.
func waitForLog(t *testing.T, logged *syncBuffer, substr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(logged.String(), substr) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("driver never logged %q", substr)
}

// TestARoundCountSurvivesCompaction: an operator compacting the very browser
// session this fixture exists to drive must not silently get a turn that runs
// past --rounds. Counting the assistant turns a request replays cannot do
// this -- compaction folds them into a user-role checkpoint, and the count
// rewinds (measured against a live hub: 15 before, 4 after).
func TestARoundCountSurvivesCompaction(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	baseURL := startDriver(t, 0, 5, "")

	s := newSession(t, baseURL, "compacted")
	for round := 1; round <= 2; round++ {
		if got := s.round(ctx).name; got != "read_file" {
			t.Fatalf("round %d before compaction: tool %q, want read_file", round, got)
		}
	}
	s.compact()
	for round := 3; round <= 4; round++ {
		if got := s.round(ctx).name; got != "read_file" {
			t.Errorf("round %d after compaction: tool %q, want read_file", round, got)
		}
	}
	if got := s.round(ctx).name; got != "communicate" {
		t.Errorf("round 5 after compaction: tool %q, want communicate (the turn still ends on its own fifth round)", got)
	}
}

// TestTheNotificationTurnEndsWithoutANewJob: --background-job-until exists to
// produce a job-completion notification turn, and the script tells the
// operator exactly that. That turn must be brief and must not launch a second
// background job -- the release file is already there, so a second job would
// complete at once and wake the session again, forever.
func TestTheNotificationTurnEndsWithoutANewJob(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	baseURL := startDriver(t, 0, 20, "release-the-job")

	s := newSession(t, baseURL, "waker")
	if got := s.round(ctx).name; got != "shell" {
		t.Fatalf("turn 1 round 1: tool %q, want shell", got)
	}
	if got := s.round(ctx).name; got != "communicate" {
		t.Fatalf("turn 1 round 2: tool %q, want communicate", got)
	}

	// The job completes and wakes the session: a turn with no input of its own.
	s.newTurn("<job-completion notification>")
	notification := s.round(ctx)
	if notification.name != "communicate" {
		t.Errorf("notification turn round 1: tool %q, want communicate (a brief turn, then idle again)", notification.name)
	}
	if endTurn, _ := notification.args["end_turn"].(bool); !endTurn {
		t.Errorf("notification turn round 1: end_turn %v, want true", endTurn)
	}
	if notification.name == "shell" {
		t.Errorf("notification turn round 1 launched a second background job")
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
		wg.Go(func() {
			s := newSession(t, baseURL, name)
			// These requests never come back: the driver is still holding
			// them when the test cancels them.
			_, _ = s.try(requestCtx)
		})
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

// TestSessionAffinityHeaderNamesRound1: when affinity headers are present, they
// should be used to name the session starting from round 1. This is the key
// improvement over tool-call-id minting: the name comes from the client on
// every request, not just from round 2 on.
func TestSessionAffinityHeaderNamesRound1(t *testing.T) {
	var logged syncBuffer
	previous := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(previous) })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	baseURL := startDriver(t, 0, 2, "")

	// Session with affinity header
	s := newSession(t, baseURL, "with-affinity")
	s.affinityHeaderID = "sess-affinity-123"

	// First round should be logged with the affinity header name, not a tool-call id.
	s.round(ctx)

	text := logged.String()
	want := "session sess-affinity-123 round 1"
	if !strings.Contains(text, want) {
		t.Errorf("round 1 not named by affinity header: %q not in log:\n%s", want, text)
	}

	// Verify the affinity header name persists to round 2
	s.round(ctx)
	text = logged.String() // refresh the logged text after round 2 completes
	want = "session sess-affinity-123 round 2"
	if !strings.Contains(text, want) {
		t.Errorf("round 2 not named by affinity header: %q not in log:\n%s", want, text)
	}
}

// TestToolCallIDFallbackWhenNoAffinityHeader: when no affinity headers are
// present, the system should fall back to the existing tool-call-id naming
// scheme. This preserves backward compatibility for instances without
// send_session_affinity_headers configured.
func TestToolCallIDFallbackWhenNoAffinityHeader(t *testing.T) {
	var logged syncBuffer
	previous := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(previous) })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	baseURL := startDriver(t, 0, 2, "")

	// Session without affinity header (affinityHeaderID remains empty)
	s := newSession(t, baseURL, "no-affinity")

	// First round: no affinity header, so should use tool-call id
	call := s.round(ctx)
	toolCallID := call.id

	text := logged.String()
	// The session should be named by its tool-call id
	want := fmt.Sprintf("session %s round 1", toolCallID)
	if !strings.Contains(text, want) {
		t.Errorf("round 1 not named by tool-call id: %q not in log:\n%s", want, text)
	}

	// Verify it persists to round 2
	s.round(ctx)
	text = logged.String() // refresh the logged text after round 2 completes
	want = fmt.Sprintf("session %s round 2", toolCallID)
	if !strings.Contains(text, want) {
		t.Errorf("round 2 not named by tool-call id: %q not in log:\n%s", want, text)
	}
}
