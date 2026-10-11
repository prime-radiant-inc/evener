package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/hooks"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// A recovery rerun runs a crashed input again under its TurnID without
// recording its user input again, so only the transcript's reopen marker
// separates its rounds from the crashed attempt's. Restore reads an end
// reason from the latest attempt alone: a rerun that failed before accepting
// a communicate rests idle, as it did live, even though the crashed attempt
// accepted one that said needs_response.
func TestRestoreReadsTheEndReasonOfTheLatestAttemptOnly(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// The first attempt accepts a needs_response communicate; a Stop hook
	// sends the input on, and the process dies with the next round in flight.
	inSecondRound := make(chan struct{})
	var calls atomic.Int32
	first := newSessionScriptAdapter("openai")
	first.otherwise = func(ctx context.Context, req llm.Request) (llm.Response, error) {
		if len(req.Tools) == 0 {
			// The session namer, not the turn.
			return llm.Response{Message: llm.Assistant("a name")}, nil
		}
		if calls.Add(1) == 1 {
			return endReasonResponse("which?", "needs_response"), nil
		}
		close(inSecondRound)
		<-ctx.Done()
		return llm.Response{}, ctx.Err()
	}
	c := llm.NewClient()
	c.Register(first)
	crashed, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	stop := hooks.NewRunner(nil, "")
	stop.Add(plugin.HookStop, plugin.RegisteredHook{Matcher: "*", Type: "command", Command: `printf '%s' '{"decision":"block","reason":"keep going"}'`, Timeout: 5})
	crashed.hookRunner = stop
	started, err := crashed.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "cm-crashed",
		Input:            []appwire.InputItem{{Type: "text", Text: "merge it?"}},
	})
	if err != nil {
		t.Fatalf("AcceptClientMutationStart: %v", err)
	}
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	go func() { _, _, _ = crashed.ProcessClientMutationStart(ctx, nil) }()
	select {
	case <-inSecondRound:
	case <-ctx.Done():
		t.Fatal("the first attempt never reached its second round")
	}
	// The crash image: the state directory as the dying process left it.
	crashDir := filepath.Join(t.TempDir(), "crashed")
	if err := os.CopyFS(crashDir, os.DirFS(dir)); err != nil {
		t.Fatalf("copy the crash image: %v", err)
	}
	cancel()
	crashed.Close()

	// The restarted process reruns the input; the rerun fails before
	// accepting any communicate.
	failing := newSessionScriptAdapter("openai")
	failing.otherwise = func(context.Context, llm.Request) (llm.Response, error) {
		return llm.Response{}, errors.New("provider unavailable")
	}
	rerun := restoreReopenSession(t, crashDir, crashed.ID(), failing)
	// TRIPWIRE: the rerun fails at its first model call; only a genuine hang waits this long.
	runCtx, runCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer runCancel()
	if _, ran, _ := rerun.ProcessClientMutationStart(runCtx, nil); !ran {
		t.Fatal("the restored process did not rerun the crashed input")
	}
	if got := rerun.State(); got != SessionIdle {
		t.Fatalf("live state after the failed rerun = %q, want idle", got)
	}
	reopened := slices.ContainsFunc(transcriptTurnsOf(t, rerun), func(turn schema.Turn) bool {
		return turn.Kind == schema.TurnReopen && turn.TurnID == started.Turn.ID
	})
	if !reopened {
		t.Fatal("the rerun recorded no reopen marker for the crashed turn")
	}
	rerun.Close()

	again := restoreReopenSession(t, crashDir, crashed.ID(), newSessionScriptAdapter("openai"))
	if got := again.State(); got != SessionIdle {
		t.Fatalf("restored state = %q, want idle: the latest attempt accepted no communicate", got)
	}
}

func restoreReopenSession(t *testing.T, dir, id string, adapter *sessionScriptAdapter) *Session {
	t.Helper()
	meta, err := schema.LoadSessionMeta(dir, id)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	c := llm.NewClient()
	c.Register(adapter)
	restored, err := RestoreSessionFromMetaWithConfig(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, RestoreSessionConfig{
		StateDir:       dir,
		LLMRetryPolicy: &llm.RetryPolicy{MaxRetries: 0},
		LLMSleep:       func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	t.Cleanup(restored.Close)
	return restored
}

// A reopen recorded before the compaction anchor resume starts from puts the
// whole retained window in the latest attempt: its start clamps to the
// window's first turn rather than an index before it.
func TestLatestAttemptStartsClampsAReopenBeforeTheRetainedWindow(t *testing.T) {
	t.Parallel()
	entry := func(kind schema.TurnKind) transcript.Entry {
		return transcript.Entry{Turn: schema.Turn{Kind: kind, TurnID: "t1"}}
	}
	entries := []transcript.Entry{
		entry(schema.TurnUserInput),
		entry(schema.TurnReopen),
		entry(schema.TurnAssistant),
		entry(schema.TurnSummary),
		entry(schema.TurnToolResults),
	}
	if got := latestAttemptStarts(entries, nil)["t1"]; got != 0 {
		t.Fatalf("latest attempt start = %d, want 0, the retained window's first turn", got)
	}
}
