package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/llm"
)

func TestNotificationQuietYieldAndBareTextRetry(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ctx := context.Background()

	bareTextResponse := func(text string) llm.Response {
		return llm.Response{
			Provider: "openai",
			Model:    "gpt-5.4",
			Message:  llm.Assistant(text),
			Finish:   llm.FinishReason{Reason: llm.FinishReasonStop},
		}
	}

	// Case 1a: Notification wake with zero prior tool activity yields quietly.
	var case1Step atomic.Int32
	adapter1 := newFakeAdapter("openai", func(req llm.Request) llm.Response {
		case1Step.Add(1)
		return quietYieldResponse("quiet-1", `{}`)
	})
	sess1 := newSession(t, withProfile(NewOpenAIProfile("gpt-5.4")), withAdapter(adapter1), withDir(dir), withStateDir(t.TempDir()))
	enqueueCompletedJobNotification(t, sess1, "job_notif_1")
	if _, err := sess1.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
		t.Fatalf("Case 1a ProcessInputKind: %v", err)
	}
	if case1Step.Load() != 1 {
		t.Fatalf("Case 1a model calls = %d, want 1 (quiet yield)", case1Step.Load())
	}

	// Case 1b: Round-0 pre-tool context-length recovery advances loop index to round 1
	// while a subsequent quiet call still finishes idle.
	var case1bCalls atomic.Int32
	adapter1b := &scriptedStreamAdapter{
		provider: "openai",
		script: map[string]func(*llm.ChanStream){
			"gpt-5.4": func(st *llm.ChanStream) {
				n := case1bCalls.Add(1)
				if n == 1 {
					st.Send(llm.StreamEvent{Type: llm.StreamEventError, Err: llm.ErrorFromHTTPStatus("openai", 413, "context length exceeded", nil, nil)})
					return
				}
				resp := quietYieldResponse("quiet-413", `{}`)
				st.Send(llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &resp.Finish, Usage: &resp.Usage, Response: &resp})
			},
		},
	}
	sess1b := newSession(t, withProfile(NewOpenAIProfile("gpt-5.4")), withAdapter(adapter1b), withDir(dir), withStateDir(t.TempDir()))
	enqueueCompletedJobNotification(t, sess1b, "job_notif_1b")
	if _, err := sess1b.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
		t.Fatalf("Case 1b ProcessInputKind: %v", err)
	}
	if case1bCalls.Load() != 2 {
		t.Fatalf("Case 1b model calls = %d, want 2 (1 context-length recovery + 1 quiet yield)", case1bCalls.Load())
	}

	// Bare text retries even when it is the first response to a wake.
	adapterBare := newFakeAdapter("openai", func(llm.Request) llm.Response {
		return bareTextResponse("Acknowledged in prose.")
	}, func(llm.Request) llm.Response {
		return quietYieldResponse("quiet-after-bare", `{}`)
	})
	sessBare := newSession(t, withAdapter(adapterBare), withStateDir(t.TempDir()))
	enqueueCompletedJobNotification(t, sessBare, "job_bare_first")
	if _, err := sessBare.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
		t.Fatal(err)
	}
	if got := len(adapterBare.Requests()); got != 2 {
		t.Fatalf("provider requests = %d, want bare-text retry and quiet yield", got)
	}
	requireQuietToolResult(t, sessBare, "quiet-after-bare")

	// Case 2: Notification wake executes a tool on round 0, then emits bare text on round 1 ->
	// must NOT finishIdle; must steer via handleNoToolCalls and complete on communicate in round 2.
	var case2Step atomic.Int32
	markerPath := filepath.Join(dir, "step1.txt")
	adapter2 := newFakeAdapter("openai", func(req llm.Request) llm.Response {
		case2Step.Add(1)
		args, _ := json.Marshal(map[string]any{
			"file_path": markerPath,
			"content":   "step1",
			"intent":    "Writing step1",
		})
		return agenttest.ToolCallResponse(llm.ToolCallData{ID: "w1", Name: "write_file", Arguments: args})
	}, func(req llm.Request) llm.Response {
		case2Step.Add(1)
		return bareTextResponse("Mid-work bare text after running a tool.")
	}, func(req llm.Request) llm.Response {
		case2Step.Add(1)
		return agenttest.FinalResponse("Completed wake work.")
	})
	sess2 := newSession(t, withProfile(NewOpenAIProfile("gpt-5.4")), withAdapter(adapter2), withDir(dir), withStateDir(t.TempDir()))
	enqueueCompletedJobNotification(t, sess2, "job_notif_2")
	if _, err := sess2.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
		t.Fatalf("Case 2 ProcessInputKind: %v", err)
	}
	if case2Step.Load() != 3 {
		t.Fatalf("Case 2 model calls = %d, want 3 (tool -> bare-text steered -> communicate)", case2Step.Load())
	}
	if got, err := os.ReadFile(markerPath); err != nil || string(got) != "step1" {
		t.Fatalf("Case 2 marker file = (%q, %v), want (%q, nil)", string(got), err, "step1")
	}
}
