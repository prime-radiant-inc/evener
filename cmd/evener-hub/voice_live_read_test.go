package hub

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/llm"
)

// A voice history or renewal read must not accept saved history when the live
// daemon read fails: the retryable transport error has to reach the bridge so
// it can hold speech instead of closing on a daemonless authority response.
func TestLiveOnlyReadRejectsSavedHistoryAfterOutage(t *testing.T) {
	const id = "02wMz5Txv5aIxgf9yVdd0N"
	root := t.TempDir()
	state := filepath.Join(root, "projects", "project-voice-0000000000")
	if err := os.MkdirAll(filepath.Join(state, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMeta(state, schema.SessionMeta{ID: id}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(state, "sessions", id+".transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: id})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Append(schema.Turn{
		Format: schema.TurnFormatIdentity, TurnID: "turn_saved",
		TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput,
		Message: llm.User("saved history"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	indexPastTranscriptForTest(t, state, id)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	cfg := hubcore.WebConfig{Past: past}
	params := appwire.ThreadReadParams{
		Ref: "local:" + id, IncludeTurns: true, ItemLimit: 40, LiveOnly: true,
	}
	outage := appwire.RetryableSessionUnavailable("relay connection ended before route publication")
	if _, ok, err := unavailableThreadReadResponse(t.Context(), cfg, appsource.NewRegistry(), params); err != nil || ok {
		t.Fatalf("live-only read served saved history after an outage: ok=%v err=%v", ok, err)
	}
	if allowsPastFallbackAfterLiveReadFailure(nil, params, outage) {
		t.Fatal("a live-only read allowed saved-history fallback after a relay outage")
	}
}

// A live-only read that loses its relay, then blocks through the hub's retry
// budget, must surface the retryable outage rather than the budget's
// DeadlineExceeded: the bridge classifies only the former as an outage, and a
// deadline here would close it and interrupt speech.
func TestLiveOnlyReadPreservesRetryableOutagePastRetryBudget(t *testing.T) {
	var calls atomic.Int32
	client, _, sessionID := newLiveReadHub(t, func(string) (appsource.RelayReadResult, error) {
		return appsource.RelayReadResult{}, appwire.RetryableSessionUnavailable("unexpected source read")
	}, hubcore.RelayLifecycleHooks{ReadThreadFailure: func(ctx context.Context) error {
		if calls.Add(1) == 1 {
			return appwire.RetryableSessionUnavailable("relay connection ended before route publication")
		}
		<-ctx.Done()
		return ctx.Err()
	}})
	_, err := client.ThreadRead(t.Context(), appwire.ThreadReadParams{
		Ref: "local:" + sessionID, IncludeTurns: true,
		ItemLimit: appwire.TranscriptItemPageLimit, LiveOnly: true,
	})
	if err == nil {
		t.Fatal("live-only read succeeded after its source failed")
	}
	if !voiceRelayOutage(err) {
		t.Fatalf("retry budget replaced the retryable outage with %T %v", err, err)
	}
}

// A retry that dies on the budget must not replace a terminal verdict the read
// returned as the deadline fired: only the budget's own DeadlineExceeded keeps
// the earlier retryable failure.
func TestLiveOnlyReadBudgetKeepsTerminalVerdict(t *testing.T) {
	var calls atomic.Int32
	client, _, sessionID := newLiveReadHub(t, func(string) (appsource.RelayReadResult, error) {
		return appsource.RelayReadResult{}, appwire.RetryableSessionUnavailable("unexpected source read")
	}, hubcore.RelayLifecycleHooks{ReadThreadFailure: func(ctx context.Context) error {
		if calls.Add(1) == 1 {
			return appwire.RetryableSessionUnavailable("relay connection ended before route publication")
		}
		<-ctx.Done()
		return appwire.InvalidParams("cursor is no longer valid")
	}})
	_, err := client.ThreadRead(t.Context(), appwire.ThreadReadParams{
		Ref: "local:" + sessionID, IncludeTurns: true,
		ItemLimit: appwire.TranscriptItemPageLimit, LiveOnly: true,
	})
	if err == nil {
		t.Fatal("live-only read succeeded after a terminal retry failure")
	}
	if voiceRelayOutage(err) {
		t.Fatalf("the retry budget masked a terminal verdict with %T %v", err, err)
	}
}

// A live-only read that loses its relay must return the retryable outage before
// ownership recovery: a roster-backed hub would otherwise run an unbudgeted
// re-read and replace the outage with a daemonless Unavailable, which the voice
// bridge treats as terminal.
func TestLiveOnlyReadPreservesOutageBeforeOwnershipRecovery(t *testing.T) {
	var calls atomic.Int32
	client, _, sessionID := newDaemonLiveReadHub(t, func(string, int32) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{}, appwire.Unavailable("ownership recovery read")
	}, true, hubcore.RelayLifecycleHooks{ReadThreadFailure: func(ctx context.Context) error {
		switch calls.Add(1) {
		case 1:
			return appwire.RetryableSessionUnavailable("relay connection ended before route publication")
		case 2:
			<-ctx.Done()
			return ctx.Err()
		default:
			return appwire.Unavailable("ownership recovery re-read failed")
		}
	}})
	_, err := client.ThreadRead(t.Context(), appwire.ThreadReadParams{
		Ref: "local:" + sessionID, IncludeTurns: true,
		ItemLimit: appwire.TranscriptItemPageLimit, LiveOnly: true,
	})
	if err == nil || !voiceRelayOutage(err) {
		t.Fatalf("live-only read after ownership recovery = %T %v, want the retryable outage", err, err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("relay reads = %d, want 2 (the retry budget, no ownership re-read)", got)
	}
}
