package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/llm"
)

// pausedWarnings serves s, collecting the attention-paused warnings it emits.
func pausedWarnings(t *testing.T, s *Session) func() []events.WarningData {
	t.Helper()
	var mu sync.Mutex
	var warnings []events.WarningData
	s.ConsumeEventsLossless(func(ev events.SessionEvent) {
		if warning, ok := ev.Data.(events.WarningData); ok && warning.Code == events.WarningCodeAttentionPaused {
			mu.Lock()
			warnings = append(warnings, warning)
			mu.Unlock()
		}
	}, func() {})
	return func() []events.WarningData {
		mu.Lock()
		defer mu.Unlock()
		return append([]events.WarningData(nil), warnings...)
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
