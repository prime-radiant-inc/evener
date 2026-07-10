//go:build serffuzz

package agenttest

import (
	"context"
	"testing"

	"primeradiant.com/serf/llm"
)

func FuzzScriptedAdapterReplay(f *testing.F) {
	f.Add("first", "second", "reply")
	f.Add("", "model with spaces", "unicode \\x00 response")

	f.Fuzz(func(t *testing.T, firstModel, secondModel, reply string) {
		adapter := &ScriptedAdapter{
			Provider: "scripted",
			Responder: func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant(reply + "|" + req.Model)}
			},
		}
		requests := []llm.Request{
			{Model: "first/" + firstModel, PromptCacheKey: "one", Messages: []llm.Message{llm.User("first request")}},
			{Model: "second/" + secondModel, PromptCacheKey: "two", Messages: []llm.Message{llm.User("second request")}},
		}

		for i, req := range requests {
			got, err := adapter.Complete(context.Background(), req)
			if err != nil {
				t.Fatalf("Complete request %d: %v", i, err)
			}
			if got.Provider != "scripted" || got.Model != req.Model {
				t.Fatalf("response %d metadata = provider=%q model=%q, want scripted/%q", i, got.Provider, got.Model, req.Model)
			}
			if got.Text() != reply+"|"+req.Model {
				t.Fatalf("response %d text = %q, want scripted replay", i, got.Text())
			}
		}

		recorded := adapter.Requests()
		if len(recorded) != len(requests) {
			t.Fatalf("recorded %d requests, want %d", len(recorded), len(requests))
		}
		for i, got := range recorded {
			want := requests[i]
			if got.Model != want.Model || got.PromptCacheKey != want.PromptCacheKey || len(got.Messages) != 1 || got.Messages[0].Text() != want.Messages[0].Text() {
				t.Fatalf("recorded request %d = %+v, want ordered replay of %+v", i, got, want)
			}
		}

		// Requests returns its own slice, so callers cannot reorder the adapter's
		// recorded history by modifying the returned value.
		recorded[0].Model = "mutated"
		if again := adapter.Requests(); again[0].Model != requests[0].Model {
			t.Fatalf("Requests exposed its backing slice: %q", again[0].Model)
		}
	})
}
