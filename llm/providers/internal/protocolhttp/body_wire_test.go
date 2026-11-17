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
			// store is the field renamingPreparer.PrepareRequest renames to
			// store_renamed, so the prepare step edits a real field of this body
			// and the byte comparison below sees the result. Without it the
			// preparer's body rewrite was a no-op here and the prepare step of
			// the full path was unproven.
			"store": true,
			"input": []any{
				// longText.String(), not longText: a strings.Builder has no
				// exported fields, so both encoders emit {} for it and the
				// escaped text never reaches the []any path this fixture
				// exists to cover.
				map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": longText.String()}}},
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
	// Not parallel: registerTestSchemes wires the package's preparerSawAuth flag into the registered authenticator, and these subtests all run Prepare through the "test-runner-preparer" scheme. In parallel they write that flag concurrently, which is a data race.
	registerTestSchemes()
	for name, body := range prepareBodyFixtures() {
		t.Run(name, func(t *testing.T) {
			res := testRes("https://api.example.test/v1", "test-runner-preparer")
			// The preparer edits the body in place, so encode the shape it left
			// behind: c.Body is the wire body after Prepare (see Prepare's doc).
			wire := body
			p, err := Prepare(context.Background(), &Call{Method: http.MethodPost, URL: URL(res, res.Transport.Endpoint), Body: wire, Req: llm.Request{Model: "m"}, Res: res})
			if err != nil {
				t.Fatal(err)
			}
			// The byte comparison below compares two encoders reading the same
			// mutated map, so it passes whether or not the prepare step ran.
			// Assert the rewrite itself, or the prepare leg of the full path is
			// unproven: renamingPreparer moves "store" to "store_renamed".
			if name == "reasoning" {
				if _, still := wire["store"]; still {
					t.Fatalf("prepare did not rename store: %v", wire["store"])
				}
				if renamed := wire["store_renamed"]; renamed != true {
					t.Fatalf("store_renamed = %v, want the preparer to have carried true across", renamed)
				}
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

// TestPrepareBodyFixturesCarryRealText guards the fixtures themselves. The
// reasoning fixture once stored a strings.Builder, which both encoders render
// as {}, so the test above compared two empty objects and passed while the
// escaped text never travelled the []any path it exists to cover. A fixture
// that loses its payload fails here instead of going quiet.
func TestPrepareBodyFixturesCarryRealText(t *testing.T) {
	t.Parallel()
	for name, body := range prepareBodyFixtures() {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if bytes.Contains(raw, []byte(`{}`)) {
			// Empty containers are legitimate in the "empty" fixture only.
			if name != "empty" {
				t.Fatalf("%s: encoded body contains an empty object, so a fixture value is not a string: %s", name, raw)
			}
		}
		if name == "reasoning" {
			// The escaped text must actually be in the bytes.
			if !bytes.Contains(raw, []byte(`some tool output line`)) {
				t.Fatalf("reasoning fixture does not carry the tool text: %s", raw)
			}
			if !bytes.Contains(raw, []byte(`\t`)) || !bytes.Contains(raw, []byte(`\n`)) {
				t.Fatalf("reasoning fixture does not carry escapes: %s", raw)
			}
		}
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
