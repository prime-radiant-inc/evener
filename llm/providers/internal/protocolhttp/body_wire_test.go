package protocolhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// prepareBodyFixtures builds the wire bodies Prepare actually marshals: the
// pruned, constant-applied, preparer-edited shapes the protocols hand it.
func prepareBodyFixtures() map[string]map[string]any {
	var longText strings.Builder
	for range 200 {
		longText.WriteString("some tool output line with <html> & \"quotes\" and tabs\there\n")
	}
	var messages []map[string]any
	for range 40 {
		messages = append(messages,
			map[string]any{"role": "assistant", "content": []map[string]any{
				{"type": "tool_use", "id": "toolu_01", "name": "grep", "input": map[string]any{"pattern": "func main"}},
			}},
			map[string]any{"role": "user", "content": []map[string]any{
				{"type": "tool_result", "tool_use_id": "toolu_01", "content": longText.String()},
			}},
		)
	}
	return map[string]map[string]any{
		"messages": {
			"max_tokens":    32000,
			"messages":      messages,
			"cache_control": map[string]any{"type": "ephemeral"},
			"model":         "claude-x-wire",
			"system":        []map[string]any{{"type": "text", "text": "sys prompt with <tag> & things"}},
			"tools": []map[string]any{{
				"name": "grep", "description": "search <files>",
				"input_schema": map[string]any{"type": "object", "properties": map[string]any{"pattern": map[string]any{"type": "string"}}},
			}},
			"temperature": 0.7,
		},
		"reasoning": {
			"model": "m", "max_output_tokens": 16000,
			"input": []any{
				map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": longText}}},
				map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "answer \u2028 with & escapes"}}},
			},
			"reasoning": map[string]any{"effort": "high", "context": "all_turns"},
			"tools":     []any{map[string]any{"type": "function", "name": "grep", "parameters": map[string]any{"type": "object"}}},
			"stream":    true,
		},
		"empty": {},
	}
}

// TestPrepareBodyBytesMatchStdlib is the protocol-level contract: the bytes
// Prepare puts on the wire are the bytes json.Marshal would have produced from
// the same final body. It runs the full prune → constants → auth → prepare
// path and re-marshals the mutated body with encoding/json afterwards, so a
// difference in any key order, escape, or number spelling fails here.
func TestPrepareBodyBytesMatchStdlib(t *testing.T) {
	t.Parallel()
	registerTestSchemes()
	for name, body := range prepareBodyFixtures() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			res := testRes("https://api.example.test/v1", "test-runner-preparer")
			// The preparer edits the body in place, so encode the shape it left
			// behind: c.Body is the wire body after Prepare (see Prepare's doc).
			wire := body
			p, err := Prepare(context.Background(), &Call{Method: http.MethodPost, URL: URL(res, res.Transport.Endpoint), Body: wire, Req: llm.Request{Model: "m"}, Res: res})
			if err != nil {
				t.Fatal(err)
			}
			want, err := json.Marshal(wire)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(p.Body, want) {
				t.Fatalf("wire bytes differ from encoding/json\nwant %s\ngot  %s", want, p.Body)
			}
			if p.Request.ContentLength != int64(len(want)) {
				t.Fatalf("ContentLength = %d, want %d", p.Request.ContentLength, len(want))
			}
		})
	}
}

// TestPrepareDifferentBodiesSendDifferentBytes is the issue's own check for a
// stale-cached-body regression (#6409): two attempts whose bodies differ in a
// constant must put different bytes on the wire, even back to back on a warm
// encoder.
func TestPrepareDifferentBodiesSendDifferentBytes(t *testing.T) {
	t.Parallel()
	for range 3 {
		first, err := Prepare(context.Background(), &Call{
			Method: http.MethodPost, URL: "https://example.test/v1/responses",
			Body: map[string]any{"input": "a", "reasoning": map[string]any{"effort": "low"}},
			Res:  registry.Resolved{Instance: "i", Transport: registry.Transport{Auth: registry.AuthNone, Body: map[string]any{"reasoning.context": "all_turns"}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		second, err := Prepare(context.Background(), &Call{
			Method: http.MethodPost, URL: "https://example.test/v1/responses",
			Body: map[string]any{"input": "a", "reasoning": map[string]any{"effort": "high"}},
			Res:  registry.Resolved{Instance: "i", Transport: registry.Transport{Auth: registry.AuthNone, Body: map[string]any{"reasoning.context": "turns"}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		var gotFirst, gotSecond map[string]any
		if err := json.Unmarshal(first.Body, &gotFirst); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(second.Body, &gotSecond); err != nil {
			t.Fatal(err)
		}
		firstReasoning := gotFirst["reasoning"].(map[string]any)
		secondReasoning := gotSecond["reasoning"].(map[string]any)
		if firstReasoning["effort"] != "low" || firstReasoning["context"] != "all_turns" {
			t.Fatalf("first body = %s", first.Body)
		}
		if secondReasoning["effort"] != "high" || secondReasoning["context"] != "turns" {
			t.Fatalf("second body = %s", second.Body)
		}
		if bytes.Equal(first.Body, second.Body) {
			t.Fatalf("different bodies produced identical bytes: %s", first.Body)
		}
	}
}

// TestPrepareAllocationsDoNotScaleWithHistory is #6409's claim as a test: the
// body pass in Prepare must not allocate per node. encoding/json allocates once
// per map, slice and string, so a 400-turn body cost hundreds of times the
// allocations of a 4-turn body. After the encoder change both are flat.
func TestPrepareAllocationsDoNotScaleWithHistory(t *testing.T) {
	// Not parallel: testing.AllocsPerRun pins GOMAXPROCS to 1 and panics in a
	// parallel test.
	res := benchPrepareRes()
	measure := func(turns int) float64 {
		body := benchPrepareBody(turns, 64)
		return testing.AllocsPerRun(20, func() {
			if _, err := Prepare(context.Background(), &Call{Method: http.MethodPost, URL: URL(res, res.Transport.Endpoint), Body: body, Res: res}); err != nil {
				t.Fatal(err)
			}
		})
	}
	smallAllocs, largeAllocs := measure(4), measure(400)
	if largeAllocs > smallAllocs+8 {
		t.Fatalf("Prepare allocated %.0f times for 4 turns and %.0f for 400 turns: the body pass allocates per node", smallAllocs, largeAllocs)
	}
}
