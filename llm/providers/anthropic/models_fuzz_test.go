package anthropic

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"sort"
	"testing"

	"primeradiant.com/serf/llm"
)

// pagedRoundTripper is a fake http.RoundTripper for the paginating ListModels
// path. It replays the fuzzer-controlled status+body for the FIRST request, then
// returns a terminal empty page (has_more:false) for every subsequent request.
// A real paginating server always terminates its pages, so bounding the loop
// this way honors the transport/pagination contract while preventing a fuzzed
// `has_more:true` body from looping forever. It always returns a non-nil
// response with a readable body and a nil error, and drains the request body —
// so any panic reproduced through it is a real adapter bug.
type pagedRoundTripper struct {
	status int
	body   []byte
	calls  int
}

func (rt *pagedRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		_, _ = io.ReadAll(req.Body)
		_ = req.Body.Close()
	}
	body := rt.body
	status := rt.status
	if rt.calls > 0 {
		// Terminal page for any follow-up request keeps pagination bounded.
		body = []byte(`{"data":[],"has_more":false}`)
		status = http.StatusOK
	}
	rt.calls++

	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     header,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Request:    req,
	}, nil
}

// FuzzAnthropicListModels drives the anthropic adapter's ListModels
// response-parsing path (including its pagination loop and synthetic [1m]
// variant generation) over a fake http.RoundTripper injected via the exported
// Adapter.Client field (no real network). Both the HTTP status and the
// /v1/models page body are fuzzed.
//
// Oracles (beyond never-panic):
//   - a non-2xx status on the first page always yields a non-nil error and a nil
//     model list;
//   - a 2xx status either errors cleanly (undecodable body) or returns a list
//     sorted ascending by ID with every model stamped "anthropic";
//   - decoding is deterministic across two identical calls.
func FuzzAnthropicListModels(f *testing.F) {
	f.Add(200, []byte(`{"data":[{"id":"claude-opus-4-1","display_name":"Opus 4.1"},{"id":"claude-haiku-3-5"}],"has_more":false}`))
	f.Add(200, []byte(`{"data":[{"id":"claude-sonnet-4-0"}],"has_more":true,"last_id":"claude-sonnet-4-0"}`))
	f.Add(200, []byte(`{"data":[{"id":"b"},{"id":"a"}],"has_more":false}`))
	f.Add(200, []byte(`{"data":[]}`))
	f.Add(200, []byte(`{}`))
	f.Add(200, []byte(`not json`))
	f.Add(200, []byte("{\"data\":\xff}"))
	f.Add(200, []byte(`{"data":[{"id":"claude-opus-4-x[1m]"}],"has_more":false}`))
	f.Add(401, []byte(`{"error":{"message":"bad key"}}`))
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
			if m.Provider != "anthropic" {
				t.Fatalf("ListModels: model %q stamped provider %q, want \"anthropic\"", m.ID, m.Provider)
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
	rt := &pagedRoundTripper{status: status, body: body}
	a := &Adapter{
		APIKey:  "k",
		BaseURL: "https://anthropic.test",
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
