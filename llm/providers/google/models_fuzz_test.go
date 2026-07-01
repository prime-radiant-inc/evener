package google

import (
	"context"
	"net/http"
	"sort"
	"testing"

	"primeradiant.com/serf/llm"
)

// FuzzGoogleListModels drives the google adapter's ListModels response-parsing
// path over a fake http.RoundTripper injected via the exported Adapter.Client
// field (no real network). Both the HTTP status and the /v1beta/models response
// body are fuzzed.
//
// Oracles (beyond never-panic):
//   - a non-2xx status always yields a non-nil error and a nil model list;
//   - a 2xx status either errors cleanly (undecodable body) or returns a list
//     sorted ascending by ID with every model stamped "google";
//   - decoding is deterministic across two identical calls.
func FuzzGoogleListModels(f *testing.F) {
	f.Add(200, []byte(`{"models":[{"name":"models/gemini-2.5-pro","displayName":"Gemini 2.5 Pro","supportedGenerationMethods":["generateContent"]}]}`))
	f.Add(200, []byte(`{"models":[{"name":"models/embedding-001","supportedGenerationMethods":["embedContent"]},{"name":"models/gemini-flash","supportedGenerationMethods":["generateContent"]}]}`))
	f.Add(200, []byte(`{"models":[{"name":"models/x"}]}`))
	f.Add(200, []byte(`{"models":[]}`))
	f.Add(200, []byte(`{}`))
	f.Add(200, []byte(`not json`))
	f.Add(200, []byte("{\"models\":\xff}"))
	f.Add(403, []byte(`{"error":{"message":"forbidden"}}`))
	f.Add(500, []byte(``))

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
			if m.Provider != "google" {
				t.Fatalf("ListModels: model %q stamped provider %q, want \"google\"", m.ID, m.Provider)
			}
		}

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
		BaseURL: "https://gemini.test",
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
