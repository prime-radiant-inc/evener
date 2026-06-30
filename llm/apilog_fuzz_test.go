package llm

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// FuzzAPILogBuilders drives the pure request/response -> log-entry transforms
// (BuildAPILogRequest, buildLogResponse, buildAPILogEntry, rawBodiesFromAttempt,
// RecordAdapterAttempt) over arbitrary Request/Response values decoded from fuzzed
// JSON. These ~5 builders shape every API-log line yet were only unit-tested with
// a handful of cases; this puts adversarial structures (odd Raw maps, empty/huge
// message and tool lists, missing fields) through them.
//
// Oracles: none panic, and BuildAPILogRequest preserves the request's structure —
// MessageCount/ToolCount/len(ToolNames) exactly mirror the request — so a transform
// that silently drops or invents messages/tools reddens it.
func FuzzAPILogBuilders(f *testing.F) {
	f.Add(
		[]byte(`{"model":"gpt-5.2","provider":"openai","messages":[{"role":"user"}],"tools":[{"name":"shell"}]}`),
		[]byte(`{"id":"resp_1","model":"gpt-5.2","raw":{"endpoint_url":"https://x","id_hash":"abc"}}`),
	)
	f.Add([]byte(`{}`), []byte(`{"raw":{"endpoint_url":123}}`)) // non-string Raw value
	f.Add([]byte(`{"messages":[]}`), []byte(`null`))

	f.Fuzz(func(t *testing.T, reqBytes, respBytes []byte) {
		// json.Unmarshal never panics; a partial/failed decode leaves a usable
		// zero-ish value, which is itself a valid input to the builders.
		var req Request
		_ = json.Unmarshal(reqBytes, &req)
		var resp Response
		_ = json.Unmarshal(respBytes, &resp)

		lr := BuildAPILogRequest(req)
		if lr.MessageCount != len(req.Messages) {
			t.Fatalf("MessageCount=%d, want len(Messages)=%d", lr.MessageCount, len(req.Messages))
		}
		if lr.ToolCount != len(req.Tools) {
			t.Fatalf("ToolCount=%d, want len(Tools)=%d", lr.ToolCount, len(req.Tools))
		}
		if len(req.Tools) > 0 && len(lr.ToolNames) != len(req.Tools) {
			t.Fatalf("ToolNames len=%d, want %d", len(lr.ToolNames), len(req.Tools))
		}

		_ = buildLogResponse(resp)
		_ = buildAPILogEntry(context.Background(), req, time.Unix(0, 0).UTC())

		rec := AdapterAttemptRecord{Request: req, Response: &resp}
		_, _ = rawBodiesFromAttempt(rec)
		// RecordAdapterAttempt is a normalize+passthrough with no recorder set; it
		// must return a record, never panic, and default the history mode.
		out := RecordAdapterAttempt(context.Background(), rec)
		if out.HistoryMode == "" && req.HistoryMode != "" {
			t.Fatalf("RecordAdapterAttempt dropped HistoryMode %q", req.HistoryMode)
		}
	})
}
