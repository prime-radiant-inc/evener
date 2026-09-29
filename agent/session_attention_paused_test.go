package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// pausedWarnings serves s, collecting the attention-paused warnings it emits.
// The returned func closes s and waits for its event drain to finish, so the
// warnings it returns are every one the session emitted.
func pausedWarnings(t *testing.T, s *Session) func() []events.WarningData {
	t.Helper()
	var warnings []events.WarningData
	drained := make(chan struct{})
	s.ConsumeEventsLossless(func(ev events.SessionEvent) {
		if warning, ok := ev.Data.(events.WarningData); ok && warning.Code == events.WarningCodeAttentionPaused {
			warnings = append(warnings, warning)
		}
	}, func() { close(drained) })
	return func() []events.WarningData {
		s.Close()
		<-drained
		return warnings
	}
}

// When a permanent provider failure defers the root's pending delegate
// attention (#2329: the paced retry stands down so a dead credential isn't
// hit every 5s), nothing said so: the updates just stopped arriving. The
// session now warns once per deferral, visible at every level, naming the
// provider and how to resume. A second failure in the same episode says
// nothing new.
func TestPermanentFailureDeferringAttentionWarnsOnceWithHowToResume(t *testing.T) {
	s, _, _, _ := newAttentionLivelockSession(t, llm.ErrorFromHTTPStatus("openai", 401, "unauthorized", nil, nil))
	warnings := pausedWarnings(t, s)

	armOneRootAttention(t, s, "dlg_401", "delegate:dlg_401/delivery/1")
	if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err == nil {
		t.Fatal("the 401 notification turn unexpectedly succeeded")
	}
	armOneRootAttention(t, s, "dlg_401b", "delegate:dlg_401b/delivery/1")
	if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err == nil {
		t.Fatal("the second 401 notification turn unexpectedly succeeded")
	}

	got := warnings()
	if len(got) != 1 {
		t.Fatalf("paused warnings = %+v, want exactly one for the episode", got)
	}
	if !strings.Contains(got[0].Message, "Sign in to openai, then send a message to continue") {
		t.Fatalf("paused warning = %q, want it to say how to resume", got[0].Message)
	}
}

// The episode ends when a root attention turn delivers: a later permanent
// failure is a new pause, worth saying again.
func TestAttentionPauseWarnsAgainAfterADelivery(t *testing.T) {
	s, _, _, _ := newAttentionLivelockSession(t, llm.ErrorFromHTTPStatus("openai", 401, "unauthorized", nil, nil))
	warnings := pausedWarnings(t, s)

	armOneRootAttention(t, s, "dlg_a", "delegate:dlg_a/delivery/1")
	_, _ = s.ProcessInputKind(context.Background(), "", nil, EntryNotification)
	// The deferred attention rides a later turn that succeeds (a user's
	// message, say) and resolves.
	if err := s.finishRootDelegateAttentionTurn([]string{"delegate:dlg_a/delivery/1"}, nil); err != nil {
		t.Fatalf("resolve the deferred attention: %v", err)
	}
	armOneRootAttention(t, s, "dlg_b", "delegate:dlg_b/delivery/1")
	_, _ = s.ProcessInputKind(context.Background(), "", nil, EntryNotification)

	if got := warnings(); len(got) != 2 {
		t.Fatalf("paused warnings = %d, want one per deferral episode (2)", len(got))
	}
}

// A model switch is a change that can make the deferred attention
// deliverable, so it re-arms the wake the permanent failure stood down.
func TestModelSwitchResumesAttentionAPermanentFailureDeferred(t *testing.T) {
	s, _, notifies, _ := newAttentionLivelockSession(t, llm.ErrorFromHTTPStatus("openai", 401, "unauthorized", nil, nil))
	serveSession(t, s)

	armOneRootAttention(t, s, "dlg_sw", "delegate:dlg_sw/delivery/1")
	_, _ = s.ProcessInputKind(context.Background(), "", nil, EntryNotification)
	if wake, _, pending := attentionRailState(s); wake || pending != 1 {
		t.Fatalf("after the 401: wake=%t pending=%d, want deferred (false, 1)", wake, pending)
	}
	baseline := notifies.Load()

	if err := s.SetModel("gpt-5.4"); err != nil {
		t.Fatalf("SetModel: %v", err)
	}
	if wake, _, pending := attentionRailState(s); !wake || pending != 1 {
		t.Fatalf("after a model switch: wake=%t pending=%d, want the deferred attention re-armed (true, 1)", wake, pending)
	}
	if notifies.Load() == baseline {
		t.Fatal("a model switch re-armed the wake without notifying the serve loop")
	}
}

// The pause says what would resume delivery, by the failure's kind.
func TestAttentionPausedMessageSaysHowToResume(t *testing.T) {
	t.Parallel()
	cases := []struct {
		err  error
		want string
	}{
		{llm.ErrorFromHTTPStatus("work", 401, "unauthorized", nil, nil), "work rejected the credential. Sign in to work, then send a message to continue."},
		{llm.ErrorFromHTTPStatus("work", 403, "forbidden", nil, nil), "work refused access. Check your access to work, then send a message to continue."},
		{llm.ErrorFromHTTPStatus("work", 404, "no such model", nil, nil), "work refused the request. Switch models or send a message to continue."},
	}
	for _, tc := range cases {
		if got := rootAttentionPausedMessage(tc.err); !strings.HasSuffix(got, tc.want) {
			t.Errorf("paused message for %v = %q, want it to end %q", tc.err, got, tc.want)
		}
	}
}

// The pause warning names the provider instance (Evener's own configuration)
// and nothing a provider said: its hint is the failure's kind and status. A
// provider's error body can carry anything, so none of it reaches the
// transcript.
func TestAttentionPausedWarningCarriesNoProviderText(t *testing.T) {
	const canary = "PROVIDER-BODY-CANARY sk-live-secret"
	s, _, _, _ := newAttentionLivelockSession(t, llm.ErrorFromHTTPStatus("openai", 401, canary, map[string]any{"error": map[string]any{"message": canary}}, nil))
	warnings := pausedWarnings(t, s)

	armOneRootAttention(t, s, "dlg_canary", "delegate:dlg_canary/delivery/1")
	_, _ = s.ProcessInputKind(context.Background(), "", nil, EntryNotification)

	got := warnings()
	if len(got) != 1 {
		t.Fatalf("paused warnings = %+v, want one", got)
	}
	encoded, err := json.Marshal(got[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "CANARY") || strings.Contains(string(encoded), "sk-live") {
		t.Fatalf("paused warning carries provider text: %s", encoded)
	}
	if got[0].Hint != "HTTP 401 (authentication)" {
		t.Fatalf("paused warning hint = %q, want the kind and status only", got[0].Hint)
	}
}

// A spent quota says when it resets, in UTC, since the daemon may run in
// another zone than the person reading.
func TestAttentionPausedMessageNamesTheQuotaResetInUTC(t *testing.T) {
	t.Parallel()
	body := map[string]any{"error": map[string]any{"type": "usage_limit_reached", "message": "The usage limit has been reached", "resets_at": json.Number("1785258150")}}
	err := llm.ErrorFromHTTPStatus("work", 429, "responses.create(stream) failed", body, nil)
	if llm.Kind(err) != llm.KindQuotaExceeded {
		t.Fatalf("fixture kind = %v, want quota exceeded", llm.Kind(err))
	}
	want := "work's usage limit is reached until " + time.Unix(1785258150, 0).UTC().Format("Jan 2 15:04 UTC") + "."
	if got := rootAttentionPausedMessage(err); !strings.Contains(got, want) {
		t.Fatalf("quota message = %q, want it to contain %q", got, want)
	}
}

// A model switch re-arms attention only when a permanent failure deferred
// it. Attention waiting out a transient failure keeps its backoff: a switch
// must not skip it.
func TestModelSwitchLeavesATransientBackoffAlone(t *testing.T) {
	s, _, _, _ := newAttentionLivelockSession(t, llm.ErrorFromHTTPStatus("openai", 503, "unavailable", nil, nil))
	serveSession(t, s)

	armOneRootAttention(t, s, "dlg_503", "delegate:dlg_503/delivery/1")
	_, _ = s.ProcessInputKind(context.Background(), "", nil, EntryNotification)
	if wake, retryActive, _ := attentionRailState(s); wake || !retryActive {
		t.Fatalf("after a 503: wake=%t retry=%t, want the paced retry armed", wake, retryActive)
	}
	if err := s.SetModel("gpt-5.4"); err != nil {
		t.Fatalf("SetModel: %v", err)
	}
	if wake, retryActive, _ := attentionRailState(s); wake || !retryActive {
		t.Fatalf("after a model switch: wake=%t retry=%t, want the backoff left alone", wake, retryActive)
	}
}

// A Stop's park wins over a model switch: the deferred attention waits for
// the user's re-engagement, not for a switch.
func TestModelSwitchDoesNotReopenAStopParkedRail(t *testing.T) {
	s, _, _, _ := newAttentionLivelockSession(t, llm.ErrorFromHTTPStatus("openai", 401, "unauthorized", nil, nil))
	// Unserved, as TestStopParksRootDelegateAttentionUntilReEngagement keeps
	// it, so nothing claims the parked queue behind the test's back.
	go func() {
		for range s.Events() {
		}
	}()
	queueOneMutation(t, s, "queue-behind-stop", "please still run me")
	armOneRootAttention(t, s, "dlg_stop", "delegate:dlg_stop/delivery/1")
	_, _ = s.ProcessInputKind(context.Background(), "", nil, EntryNotification)
	if _, err := s.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "stop-before-model-switch",
	}, func() {}); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !s.rootAttentionRailParked() {
		t.Fatal("this test is not in the state it means to be: the Stop did not park the rail")
	}
	if err := s.SetModel("gpt-5.4"); err != nil {
		t.Fatalf("SetModel: %v", err)
	}
	if wake, _, pending := attentionRailState(s); wake || pending != 1 {
		t.Fatalf("after a model switch on a Stop-parked rail: wake=%t pending=%d, want it still parked (false, 1)", wake, pending)
	}
}
