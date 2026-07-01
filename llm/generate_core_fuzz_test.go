package llm

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// This file fuzzes the llm generation CORE — the provider-agnostic generate loop
// in generate.go / stream_generate.go / generate_object.go — by driving it through
// a SCRIPTED stub ProviderAdapter whose per-step model output is decoded from the
// fuzzer's bytes. The fuzzer NEVER makes a real network call: the stub stands in
// for the provider and replays fuzzed text, tool calls, finish reasons, usage, and
// injected transport errors.
//
// Target functions (all ~0% fuzz-reached before this harness): Generate,
// StreamGenerate, GenerateObject, StreamGenerateObject, tryParsePartialJSON,
// executeToolCalls, executeSingleToolCall, prepareGeneration.

// noSleep is a deterministic clock for the retry path: it never actually sleeps,
// so fuzz iterations stay fast and time-independent.
func noSleep(_ context.Context, _ time.Duration) error { return nil }

// scriptStep is one scripted model turn: the text and tool calls the stub emits,
// its finish reason and usage, and an optional injected fault.
type scriptStep struct {
	text     string
	calls    []ToolCallData
	finish   string
	inTok    int
	outTok   int
	totTok   int
	injErr   bool // adapter returns/emits an error for this round
	errKind  byte // selects which error to inject
	openFail bool // for Stream: fail at open time rather than mid-stream
}

func (s scriptStep) response() Response {
	content := []ContentPart{}
	if s.text != "" {
		content = append(content, ContentPart{Kind: ContentText, Text: s.text})
	}
	for i := range s.calls {
		c := s.calls[i]
		content = append(content, ContentPart{Kind: ContentToolCall, ToolCall: &c})
	}
	return Response{
		ID:      "resp",
		Model:   "m",
		Message: Message{Role: RoleAssistant, Content: content},
		Finish:  FinishReason{Reason: s.finish},
		Usage:   Usage{InputTokens: s.inTok, OutputTokens: s.outTok, TotalTokens: s.totTok},
	}
}

func (s scriptStep) injectedErr() error {
	switch s.errKind % 3 {
	case 0:
		// Permanent: surfaces on the first attempt (no retry burn).
		return ErrorFromHTTPStatus("stub", 400, "bad request", nil, nil)
	case 1:
		// Retryable: exhausts the retry budget, then surfaces.
		return ErrorFromHTTPStatus("stub", 429, "rate limited", nil, nil)
	default:
		return NewStreamError("stub", "boom", nil)
	}
}

// scriptedFuzzAdapter is a contract-honoring stub ProviderAdapter. It plays back a
// fixed script indexed by the tool-loop ROUND, computed from the request history
// (count of assistant messages) so that retries — which re-send an identical
// request — deterministically replay the SAME step. This makes injected faults
// stable across retries.
type scriptedFuzzAdapter struct {
	script []scriptStep
}

func (a *scriptedFuzzAdapter) Name() string { return "stub" }

func (a *scriptedFuzzAdapter) stepFor(req Request) scriptStep {
	round := 0
	for _, m := range req.Messages {
		if m.Role == RoleAssistant {
			round++
		}
	}
	if round >= len(a.script) {
		round = len(a.script) - 1
	}
	if round < 0 {
		return scriptStep{finish: FinishReasonStop}
	}
	return a.script[round]
}

func (a *scriptedFuzzAdapter) Complete(_ context.Context, req Request) (Response, error) {
	s := a.stepFor(req)
	if s.injErr {
		// Contract: an error result carries no usable response.
		return Response{}, s.injectedErr()
	}
	return s.response(), nil
}

func (a *scriptedFuzzAdapter) Stream(_ context.Context, req Request) (Stream, error) {
	s := a.stepFor(req)
	if s.injErr && s.openFail {
		// Contract: open failure returns (nil, err), never (nil, nil).
		return nil, s.injectedErr()
	}
	st := NewChanStream(nil)
	go func() {
		defer st.CloseSend()
		st.Send(StreamEvent{Type: StreamEventStreamStart})
		// Emit text in small chunks to stress incremental parsing.
		for i := 0; i < len(s.text); i += 3 {
			end := i + 3
			if end > len(s.text) {
				end = len(s.text)
			}
			st.Send(StreamEvent{Type: StreamEventTextDelta, Delta: s.text[i:end]})
		}
		for i := range s.calls {
			c := s.calls[i]
			st.Send(StreamEvent{Type: StreamEventToolCallStart, ToolCall: &c})
			st.Send(StreamEvent{Type: StreamEventToolCallEnd, ToolCall: &c})
		}
		if s.injErr {
			st.Send(StreamEvent{Type: StreamEventError, Err: s.injectedErr()})
			return
		}
		resp := s.response()
		st.Send(StreamEvent{Type: StreamEventFinish, Response: &resp, FinishReason: &resp.Finish, Usage: &resp.Usage})
	}()
	return st, nil
}

// cursor is a saturating byte reader over the fuzzer's script bytes.
type cursor struct {
	b []byte
	i int
}

func (c *cursor) u8() byte {
	if c.i >= len(c.b) {
		return 0
	}
	v := c.b[c.i]
	c.i++
	return v
}

// fuzzTools returns the tool set the harness exposes to the generate loop. Their
// Execute handlers are deterministic. Schemas exercise both validation-failure and
// success branches of executeSingleToolCall/parseAndValidateArgs.
func fuzzTools() []Tool {
	strObj := func(prop string, required bool) map[string]any {
		m := map[string]any{
			"type":       "object",
			"properties": map[string]any{prop: map[string]any{"type": "string"}},
		}
		if required {
			m["required"] = []any{prop}
		}
		return m
	}
	return []Tool{
		{
			Definition: ToolDefinition{Name: "read_file", Parameters: strObj("path", true)},
			ReadOnly:   true,
			Execute:    func(_ context.Context, _ any) (any, error) { return "file-contents", nil },
		},
		{
			Definition: ToolDefinition{Name: "grep", Parameters: map[string]any{"type": "object"}},
			ReadOnly:   true,
			Execute:    func(_ context.Context, _ any) (any, error) { return map[string]any{"hits": 1}, nil },
		},
		{
			Definition: ToolDefinition{Name: "run", Parameters: strObj("cmd", false)},
			Execute: func(_ context.Context, args any) (any, error) {
				if m, ok := args.(map[string]any); ok {
					if _, boom := m["boom"]; boom {
						return nil, context.Canceled
					}
				}
				return "ok", nil
			},
		},
		// Passive tool: no Execute handler, exercises the passive-return path.
		{Definition: ToolDefinition{Name: "passive", Parameters: map[string]any{"type": "object"}}},
	}
}

var fuzzToolNames = []string{"read_file", "grep", "run", "passive", "unknown_tool"}

var fuzzFinishReasons = []string{
	FinishReasonStop, FinishReasonToolCalls, FinishReasonPauseTurn,
	FinishReasonLength, FinishReasonContentFilter, "weird",
}

// decodeScript turns fuzzer bytes into a bounded multi-step script.
func decodeScript(scriptBytes, argsJSON []byte) []scriptStep {
	c := &cursor{b: scriptBytes}
	nSteps := 1 + int(c.u8()%4) // 1..4
	steps := make([]scriptStep, 0, nSteps)
	for s := 0; s < nSteps; s++ {
		flags := c.u8()
		nCalls := int(c.u8() % 4) // 0..3
		var calls []ToolCallData
		for k := 0; k < nCalls; k++ {
			name := fuzzToolNames[int(c.u8())%len(fuzzToolNames)]
			calls = append(calls, ToolCallData{
				ID:        "call-" + string(rune('a'+s)) + string(rune('0'+k)),
				Name:      name,
				Arguments: json.RawMessage(argsJSON),
				Type:      "function",
			})
		}
		text := ""
		if flags&1 != 0 {
			n := int(c.u8()) % 64
			buf := make([]byte, 0, n)
			for i := 0; i < n; i++ {
				buf = append(buf, c.u8())
			}
			text = string(buf)
		}
		steps = append(steps, scriptStep{
			text:     text,
			calls:    calls,
			finish:   fuzzFinishReasons[int(c.u8())%len(fuzzFinishReasons)],
			inTok:    int(c.u8()),
			outTok:   int(c.u8()),
			totTok:   int(c.u8()),
			injErr:   flags&2 != 0,
			errKind:  c.u8(),
			openFail: flags&4 != 0,
		})
	}
	return steps
}

// usageSumMatchesTotal is the step-accounting oracle: TotalUsage must equal the
// field-wise sum of every step's Usage (the loop adds exactly one response's usage
// and appends exactly one step per round).
func usageSumMatchesTotal(t *testing.T, steps []StepResult, total Usage, where string) {
	t.Helper()
	var in, out, tot int
	for _, s := range steps {
		in += s.Usage.InputTokens
		out += s.Usage.OutputTokens
		tot += s.Usage.TotalTokens
	}
	if in != total.InputTokens || out != total.OutputTokens || tot != total.TotalTokens {
		t.Fatalf("%s: TotalUsage {%d,%d,%d} != sum of steps {%d,%d,%d}",
			where, total.InputTokens, total.OutputTokens, total.TotalTokens, in, out, tot)
	}
}

// FuzzGenerateCore drives the whole provider-agnostic generation core through a
// scripted stub adapter: Generate + StreamGenerate (tool loop, passive calls,
// budgets, retries, fault injection) plus GenerateObject + StreamGenerateObject
// (partial-JSON parse + schema validation). The stub replaces the network.
//
// Oracles:
//   - Never panics on any scripted output (all four entry points).
//   - A completed Generate/StreamGenerate is internally consistent: TotalUsage
//     equals the field-wise sum of the per-step usages, and the result mirrors the
//     final step's finish reason.
//   - A fault injected on the very first round surfaces as an error, never a lost
//     turn (Generate returns a non-nil error and no result).
//   - prepareGeneration's mutual-exclusion rule holds: prompt+messages together is
//     always rejected.
//   - GenerateObject success implies the parsed Output re-validates against the
//     schema (independent re-check).
func FuzzGenerateCore(f *testing.F) {
	f.Add([]byte{0}, []byte(`{"path":"/x"}`), `{"name":"a"}`, uint32(0))
	f.Add([]byte{1, 0x03, 0x01, 0x00, 0x05, 0x02, 0x02, 0x02}, []byte(`{}`), `[1,2]`, uint32(7))
	f.Add([]byte{2, 0x07, 0x02, 0x00, 0x00, 0x01, 0x01, 0x01, 0x01}, []byte(`not json`), `oops`, uint32(11))
	// Two-step tool loop: round 0 emits a single read_file call with finish=tool_calls
	// (drives executeToolCalls/executeSingleToolCall/parseAndValidateArgs), round 1 stops.
	f.Add([]byte{1, 0, 1, 0, 1, 2, 0, 2, 0, 0, 0, 0, 1, 1, 2, 0}, []byte(`{"path":"/x"}`), `{"name":"a"}`, uint32(1))
	// Round 0 emits two consecutive read-only calls (read_file + grep) with invalid
	// args: exercises the parallel read-batch path, a validation failure, and repair.
	f.Add([]byte{1, 0, 2, 0, 1, 1, 2, 0, 2, 0, 0, 0, 0, 1, 1, 2, 0}, []byte(`{}`), `{}`, uint32(0x101))

	f.Fuzz(func(t *testing.T, scriptBytes, argsJSON []byte, objText string, sel uint32) {
		if len(argsJSON) > 512 {
			argsJSON = argsJSON[:512]
		}
		if len(objText) > 1024 {
			objText = objText[:1024]
		}
		script := decodeScript(scriptBytes, argsJSON)

		maxRounds := int(sel % 4) // 0..3, exercises passive + budget paths
		policy := RetryPolicy{MaxRetries: int((sel >> 2) % 3)}
		var repair func(context.Context, ToolCallData, error) (json.RawMessage, error)
		if sel&0x100 != 0 {
			repair = func(context.Context, ToolCallData, error) (json.RawMessage, error) {
				return json.RawMessage(`{"path":"/repaired"}`), nil
			}
		}

		buildOpts := func() GenerateOptions {
			c := NewClient()
			c.Register(&scriptedFuzzAdapter{script: script})
			opts := GenerateOptions{
				Client:         c,
				Model:          "m",
				Provider:       "stub",
				Tools:          fuzzTools(),
				MaxToolRounds:  &maxRounds,
				RetryPolicy:    &policy,
				Sleep:          noSleep,
				RepairToolCall: repair,
			}
			// Mode selects prompt / messages / both (both is a config error).
			switch (sel >> 12) % 3 {
			case 0:
				p := "hello"
				opts.Prompt = &p
			case 1:
				opts.Messages = []Message{User("hello")}
			default:
				p := "hello"
				opts.Prompt = &p
				opts.Messages = []Message{User("hello")}
			}
			if sel&0x200 != 0 {
				sys := "be terse"
				opts.System = &sys
			}
			return opts
		}

		bothSet := (sel>>12)%3 == 2

		// --- Generate ---
		{
			res, err := Generate(context.Background(), buildOpts())
			switch {
			case bothSet:
				if err == nil {
					t.Fatalf("Generate accepted both prompt and messages")
				}
			case len(script) > 0 && script[0].injErr:
				if err == nil {
					t.Fatalf("Generate lost a first-round injected fault (res=%v)", res != nil)
				}
			}
			if err == nil {
				if res == nil {
					t.Fatalf("Generate returned nil result and nil error")
				}
				if len(res.Steps) == 0 {
					t.Fatalf("Generate succeeded with zero steps")
				}
				usageSumMatchesTotal(t, res.Steps, res.TotalUsage, "Generate")
				last := res.Steps[len(res.Steps)-1]
				if res.FinishReason.Reason != last.FinishReason.Reason {
					t.Fatalf("Generate result finish %q != final step finish %q",
						res.FinishReason.Reason, last.FinishReason.Reason)
				}
			}
		}

		// --- StreamGenerate ---
		{
			sr, err := StreamGenerate(context.Background(), buildOpts())
			if err == nil && sr != nil {
				for range sr.Events() {
				}
				resp, rerr := sr.Response()
				if rerr == nil && resp != nil {
					steps := sr.Steps()
					if len(steps) > 0 {
						usageSumMatchesTotal(t, steps, sr.TotalUsage(), "StreamGenerate")
					}
				}
				_ = sr.Close()
			}
		}

		// --- GenerateObject / StreamGenerateObject ---
		if !bothSet {
			objSchema := map[string]any{
				"type":       "object",
				"properties": map[string]any{"name": map[string]any{"type": "string"}},
			}
			objAdapter := func() *scriptedFuzzAdapter {
				return &scriptedFuzzAdapter{script: []scriptStep{{text: objText, finish: FinishReasonStop}}}
			}
			objOpts := func() GenerateObjectOptions {
				c := NewClient()
				c.Register(objAdapter())
				p := "give me an object"
				return GenerateObjectOptions{
					GenerateOptions: GenerateOptions{
						Client:   c,
						Model:    "m",
						Provider: "stub",
						Prompt:   &p,
						Sleep:    noSleep,
					},
					Schema: objSchema,
				}
			}

			// GenerateObject: success implies Output re-validates.
			if res, err := GenerateObject(context.Background(), objOpts()); err == nil {
				if res == nil || res.Output == nil {
					t.Fatalf("GenerateObject success but nil Output")
				}
				schema, cerr := compileSchema(objSchema)
				if cerr != nil {
					t.Fatalf("schema compile failed: %v", cerr)
				}
				if verr := schema.Validate(res.Output); verr != nil {
					t.Fatalf("GenerateObject Output failed re-validation: %v", verr)
				}
			}

			// StreamGenerateObject: drain, then success implies Output re-validates.
			if sor, err := StreamGenerateObject(context.Background(), objOpts()); err == nil && sor != nil {
				for range sor.Events() {
				}
				out := sor.Output()
				if _, rerr := sor.Response(); rerr == nil && out != nil {
					schema, cerr := compileSchema(objSchema)
					if cerr != nil {
						t.Fatalf("schema compile failed: %v", cerr)
					}
					if verr := schema.Validate(out); verr != nil {
						t.Fatalf("StreamGenerateObject Output failed re-validation: %v", verr)
					}
				}
				_ = sor.Close()
			}
		}
	})
}

// FuzzTryParsePartialJSON drives the incremental JSON parser used by the streaming
// object path. Oracles: it is deterministic (equal inputs give equal results), and
// any non-nil success is well-formed JSON that re-marshals cleanly.
func FuzzTryParsePartialJSON(f *testing.F) {
	f.Add(`{"a":1`)
	f.Add(`{"a":[1,2,{"b":"c`)
	f.Add(`[1,2,3`)
	f.Add(``)
	f.Add(`"unterminated`)
	f.Add("```json\n{\"x\":1}\n```")

	f.Fuzz(func(t *testing.T, s string) {
		if len(s) > 4096 {
			s = s[:4096]
		}
		got := tryParsePartialJSON(s)

		// Determinism: a second parse of the same input must match.
		again := tryParsePartialJSON(s)
		if !reflect.DeepEqual(got, again) {
			t.Fatalf("tryParsePartialJSON not deterministic for %q", s)
		}

		// Success implies valid JSON: the returned value re-marshals cleanly.
		if got != nil {
			if _, err := json.Marshal(got); err != nil {
				t.Fatalf("tryParsePartialJSON returned unmarshalable value for %q: %v", s, err)
			}
		}
	})
}
