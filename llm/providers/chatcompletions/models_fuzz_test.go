package chatcompletions

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

// FuzzChatCompletionsListModels pins the listing's decode contract: a row
// of any shape, including shapes no reader models, must never fail the
// listing while a healthy sibling row still loads. The boolean reasoning
// verdict broke exactly this contract: one alien row took the whole live
// refresh down with it.
func FuzzChatCompletionsListModels(f *testing.F) {
	f.Add([]byte(`{"reasoning":true}`))
	f.Add([]byte(`{"id":"m","reasoning":false}`))
	f.Add([]byte(`{"reasoning":{"mandatory":true,"supported_efforts":["low","high"]}}`))
	f.Add([]byte(`{"reasoning":{"mandatory":"yes"}}`))
	f.Add([]byte(`{"reasoning":"always"}`))
	f.Add([]byte(`{"supported_reasoning_levels":["low",{"effort":"high"},5,null]}`))
	f.Add([]byte(`{"id":"m","default_reasoning_level":5}`))
	f.Add([]byte(`{"id":"m","limits":{"max_output_tokens":"big"}}`))
	f.Add([]byte(`{"id":"m","pricing":{"prompt":5}}`))
	f.Add([]byte(`{"id":123}`))
	f.Add([]byte(`{"context_length":null}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))
	f.Add([]byte(`5`))
	f.Add([]byte(`"text"`))
	f.Add([]byte(`[]`))
	f.Add([]byte(`{"id":"","reasoning":true}`))

	f.Fuzz(func(t *testing.T, row []byte) {
		// Arbitrary bytes exercise the malformed-body arm: any error or
		// empty result is acceptable there, a panic is not.
		srv, _ := server(t, 200, `{"data":[`+string(row)+`,{"id":"healthy-model","context_length":8192}]}`)
		_, _ = (&Protocol{Client: srv.Client()}).ListModels(context.Background(), liveRes(srv, nil))

		if !json.Valid(row) {
			return
		}
		// A syntactically valid row, whatever its shape, must not take the
		// healthy sibling down with it.
		body, err := json.Marshal(struct {
			Data []json.RawMessage `json:"data"`
		}{[]json.RawMessage{row, json.RawMessage(`{"id":"healthy-model","context_length":8192}`)}})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		srv2, _ := server(t, 200, string(body))
		rows, err := (&Protocol{Client: srv2.Client()}).ListModels(context.Background(), liveRes(srv2, nil))
		if err != nil {
			t.Fatalf("an alien row must not fail the listing: %v", err)
		}
		if !slices.ContainsFunc(rows, func(m registry.Model) bool { return m.ID == "healthy-model" }) {
			t.Fatalf("the healthy row must load: %+v", rows)
		}
	})
}
