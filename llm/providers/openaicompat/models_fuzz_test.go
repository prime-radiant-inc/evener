package openaicompat

import (
	"context"
	"net/http"
	"sort"
	"testing"

	"primeradiant.com/serf/llm"
)

// FuzzOpenaicompatListModels drives the openai-compatible adapter's ListModels
// response-parsing path over a fake http.RoundTripper injected via the exported
// Adapter.Client field (no real network). Both the HTTP status and the /models
// response body are fuzzed.
//
// Oracles (beyond never-panic):
//   - a non-2xx status always yields a non-nil error and a nil model list;
//   - a 2xx status either errors cleanly (undecodable body) or returns a list
//     sorted ascending by ID with every model stamped "openai-compatible";
//   - decoding is deterministic: a second identical call yields an identical
//     ordered ID sequence (no nondeterministic ordering leaks into the output).
func FuzzOpenaicompatListModels(f *testing.F) {
	f.Add(200, []byte(`{"data":[{"id":"kimi-for-coding","context_length":262144},{"id":"llama3","context_length":8192}]}`))
	f.Add(200, []byte(`{"data":[{"id":"m1"},{"id":"m0"}]}`))
	f.Add(200, []byte(`{"data":[]}`))
	f.Add(200, []byte(`{}`))
	f.Add(200, []byte(`not json`))
	f.Add(200, []byte("{\"data\":\xff}"))
	f.Add(200, []byte(`{"data":[{"id":"x","context_length":-5}]}`))
	f.Add(404, []byte(`{"error":"nope"}`))
	f.Add(503, []byte(``))

	f.Fuzz(func(t *testing.T, statusSel int, body []byte) {
		status := normalizeStatus(statusSel)

		models, err := listModelsWithBody(t, status, body)

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
			return
		}

		if !sort.SliceIsSorted(models, func(i, j int) bool { return models[i].ID < models[j].ID }) {
			t.Fatalf("ListModels: returned models are not sorted by ID: %v", models)
		}
		for _, m := range models {
			if m.Provider != "openai-compatible" {
				t.Fatalf("ListModels: model %q stamped provider %q, want \"openai-compatible\"", m.ID, m.Provider)
			}
		}

		// Determinism: a fresh adapter fed the identical body must produce the
		// same ordered result.
		again, err2 := listModelsWithBody(t, status, body)
		if err2 != nil {
			t.Fatalf("ListModels: second identical call errored after first succeeded: %v", err2)
		}
		if len(again) != len(models) {
			t.Fatalf("ListModels: nondeterministic length %d vs %d for body %q", len(again), len(models), body)
		}
		for i := range models {
			if again[i].ID != models[i].ID {
				t.Fatalf("ListModels: nondeterministic order at %d: %q vs %q", i, again[i].ID, models[i].ID)
			}
		}
	})
}

func listModelsWithBody(t *testing.T, status int, body []byte) ([]llm.ModelInfo, error) {
	t.Helper()
	rt := &captureRoundTripper{status: status, body: body}
	a := &Adapter{
		APIKey:  "k",
		BaseURL: "https://compat.test/v1",
		Client:  &http.Client{Transport: rt},
	}
	return a.ListModels(context.Background())
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
