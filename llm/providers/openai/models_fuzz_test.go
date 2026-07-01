package openai

import (
	"context"
	"net/http"
	"sort"
	"testing"
)

// FuzzOpenaiListModels drives the openai adapter's ListModels response-parsing
// path over a fake http.RoundTripper injected via the exported Adapter.Client
// field (no real network). Both the HTTP status and the models-list response
// body are fuzzed.
//
// Oracles (beyond never-panic):
//   - a non-2xx status always yields a non-nil error and a nil model list;
//   - a 2xx status either errors cleanly (undecodable body) or returns a
//     well-formed list: sorted ascending by ID, every model stamped with the
//     "openai" provider, and no model whose ID the adapter is meant to skip
//     (embeddings, tts, image, etc.) leaking through.
func FuzzOpenaiListModels(f *testing.F) {
	f.Add(200, []byte(`{"data":[{"id":"gpt-4o","context_window":128000},{"id":"gpt-4o-mini","max_output_tokens":16384}]}`))
	f.Add(200, []byte(`{"data":[{"id":"text-embedding-3-small"},{"id":"whisper-1"},{"id":"gpt-4.1"}]}`))
	f.Add(200, []byte(`{"models":[{"slug":"gpt-5","display_name":"GPT-5","max_context_window":400000,"supported_reasoning_levels":[{"effort":"low"},{"effort":"high"}],"input_modalities":["text","image"]}]}`))
	f.Add(200, []byte(`{"data":[{"id":""}],"models":[{"slug":""}]}`))
	f.Add(200, []byte(`{}`))
	f.Add(200, []byte(`not json`))
	f.Add(200, []byte("{\"data\":\xff}"))
	f.Add(429, []byte(`{"error":{"message":"rate limited"}}`))
	f.Add(500, []byte(``))

	f.Fuzz(func(t *testing.T, statusSel int, body []byte) {
		status := normalizeStatus(statusSel)
		rt := &captureRoundTripper{status: status, body: body}
		a := &Adapter{
			APIKey:  "k",
			BaseURL: "https://api.openai.test/v1",
			Client:  &http.Client{Transport: rt},
		}

		models, err := a.ListModels(context.Background())

		if status < 200 || status >= 300 {
			if err == nil {
				t.Fatalf("ListModels: nil error for HTTP status %d (body %q)", status, body)
			}
			if models != nil {
				t.Fatalf("ListModels: non-nil models %v alongside HTTP %d error", models, status)
			}
			return
		}
		if err != nil {
			if models != nil {
				t.Fatalf("ListModels: error %v returned with non-nil models %v", err, models)
			}
			return // an undecodable 2xx body is an acceptable clean error
		}

		if !sort.SliceIsSorted(models, func(i, j int) bool { return models[i].ID < models[j].ID }) {
			t.Fatalf("ListModels: returned models are not sorted by ID: %v", models)
		}
		for _, m := range models {
			if m.Provider != "openai" {
				t.Fatalf("ListModels: model %q stamped provider %q, want \"openai\"", m.ID, m.Provider)
			}
			if skipOpenAIModel(m.ID) {
				t.Fatalf("ListModels: skip-listed model %q leaked into the result", m.ID)
			}
			if m.MaxOutputTokens != nil && *m.MaxOutputTokens <= 0 {
				t.Fatalf("ListModels: model %q has non-positive MaxOutputTokens pointer %d", m.ID, *m.MaxOutputTokens)
			}
		}
	})
}

// normalizeStatus maps a fuzzed int to an HTTP status code. Values already in
// the valid 100..599 range pass through unchanged (so literal seed codes like
// 200/404/500 are honored); anything else is folded into 200..599.
func normalizeStatus(sel int) int {
	if sel >= 100 && sel <= 599 {
		return sel
	}
	n := sel % 400
	if n < 0 {
		n += 400
	}
	return 200 + n
}
