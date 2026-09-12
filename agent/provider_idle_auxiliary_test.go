package agent

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/hooks"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/llm"
)

func TestProviderIdleCompactionFallbackUsesSessionPolicy(t *testing.T) {
	adapter := &agenttest.ModelTrackingAdapter{Provider: "openai"}
	adapter.Respond = func(req llm.Request) (llm.Response, error) {
		if req.Model == "gpt-4.1-nano" {
			return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 400, "bad request", nil, nil)
		}
		return llm.Response{Message: llm.Assistant("summary")}, nil
	}
	sess := newSession(t, withAdapter(adapter), withProfile(WithCheapModel(NewOpenAIProfile("gpt-5.2"), "gpt-4.1-nano")), withConfig(SessionConfig{ProviderIdleTimeout: "45s"}))
	drainSessionEvents(sess)
	if _, err := sess.contextMgr.ElicitNote(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	requests := adapter.Requests()
	if len(requests) != 2 || requests[0].Model != "gpt-4.1-nano" || requests[1].Model != "gpt-5.2" {
		t.Fatalf("expected cheap then session-model requests, got %+v", requests)
	}
	for _, req := range requests {
		if req.AdapterTimeout == nil || req.AdapterTimeout.StreamRead != 45*time.Second || req.AdapterTimeout.Request != 0 {
			t.Errorf("model %s timeout = %+v, want 45s idle and no total", req.Model, req.AdapterTimeout)
		}
	}
}

func TestProviderIdleAuxiliarySessionRequests(t *testing.T) {
	for _, value := range []string{"", "45s"} {
		for _, path := range []string{"web search", "compaction", "cheap caller", "naming", "prompt hook"} {
			t.Run(value+"/"+path, func(t *testing.T) {
				want := 10 * time.Minute
				if value != "" {
					want = 45 * time.Second
				}
				var captured []llm.Request
				adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{func(req llm.Request) llm.Response {
					captured = append(captured, req)
					return llm.Response{Message: llm.Assistant(`{"name":"Test Session"}`)}
				}}}
				sess := newSession(t, withAdapter(adapter), withProfile(WithCheapModel(NewOpenAIProfile("gpt-5.2"), "gpt-4.1-nano")), withConfig(SessionConfig{ProviderIdleTimeout: value, PluginDirs: []string{t.TempDir()}}))
				drainSessionEvents(sess)
				sess.client.SetDefaultProvider("openai")
				ctx := context.Background()
				var err error
				switch path {
				case "web search":
					_, err = sess.webSearch(ctx, "question")
				case "compaction":
					_, err = sess.contextMgr.ElicitNote(ctx, nil)
				case "cheap caller":
					_, err = sess.cheap.Complete(ctx, sess.currentProfile(), llm.Request{Messages: []llm.Message{llm.User("summarize")}})
				case "prompt hook":
					sess.hookRunner.Add(plugin.HookUserPromptSubmit, plugin.RegisteredHook{Type: "prompt", Prompt: "check request"})
					sess.hookRunner.RunUserPromptSubmit(ctx, hooks.Input{})
				case "naming":
					err = sess.nameSessionFromText(ctx, sessionNameSourcePrompt, "test the session")
				}
				if err != nil {
					t.Fatal(err)
				}
				if len(captured) != 1 {
					t.Fatalf("captured %d requests, want 1", len(captured))
				}
				for _, req := range captured {
					if req.AdapterTimeout == nil {
						t.Fatal("auxiliary request omitted session timeout")
					}
					if req.AdapterTimeout.StreamRead != want {
						t.Errorf("idle = %v, want %v", req.AdapterTimeout.StreamRead, want)
					}
					if req.AdapterTimeout.Request != 0 {
						t.Errorf("total = %v, want disabled", req.AdapterTimeout.Request)
					}
				}
			})
		}
	}
}
