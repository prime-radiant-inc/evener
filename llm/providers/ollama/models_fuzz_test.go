package ollama

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"sort"
	"testing"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providers/openaicompat"
)

// modelsRoundTripper is a fake http.RoundTripper: it replays fuzzer-controlled
// status+body for every request. It honors the RoundTripper contract — always a
// non-nil response with a readable non-nil body and a nil error, and it drains
// the request body — so any panic reproduced through it is a real adapter bug.
type modelsRoundTripper struct {
	status int
	body   []byte
}

func (rt *modelsRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		_, _ = io.ReadAll(req.Body)
		_ = req.Body.Close()
	}
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	return &http.Response{
		StatusCode: rt.status,
		Status:     http.StatusText(rt.status),
		Header:     header,
		Body:       io.NopCloser(bytes.NewReader(rt.body)),
		Request:    req,
	}, nil
}

// FuzzOllamaListModels drives the ollama adapter's ListModels response-parsing
// path over a fake http.RoundTripper injected via the backing
// openai-compatible Adapter.Client field (no real network). The ollama adapter
// delegates to that backing /models fetch and re-stamps each model's provider
// to "ollama"; this fuzzer exercises the delegate-and-restamp path over fuzzed
// status codes and response bodies.
//
// Oracles (beyond never-panic):
//   - a non-2xx status always yields a non-nil error and a nil model list;
//   - a 2xx status either errors cleanly (undecodable body) or returns a list
//     sorted ascending by ID with every model stamped "ollama" (never the
//     backing "openai-compatible" stamp);
//   - decoding is deterministic across two identical calls.
func FuzzOllamaListModels(f *testing.F) {
	f.Add(200, []byte(`{"data":[{"id":"llama3.1","context_length":131072},{"id":"qwen2.5"}]}`))
	f.Add(200, []byte(`{"data":[{"id":"m1"},{"id":"m0"}]}`))
	f.Add(200, []byte(`{"data":[]}`))
	f.Add(200, []byte(`{}`))
	f.Add(200, []byte(`not json`))
	f.Add(200, []byte("{\"data\":\xff}"))
	f.Add(404, []byte(`{"error":"nope"}`))
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
			if m.Provider != "ollama" {
				t.Fatalf("ListModels: model %q stamped provider %q, want \"ollama\"", m.ID, m.Provider)
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
	rt := &modelsRoundTripper{status: status, body: body}
	a := newAdapter("", &openaicompat.Adapter{
		APIKey:  "k",
		BaseURL: "https://ollama.test/v1",
		Client:  &http.Client{Transport: rt},
	})
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
