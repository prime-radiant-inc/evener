package difftest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providers/anthropic"
	"primeradiant.com/serf/llm/providers/google"
	"primeradiant.com/serf/llm/providers/openai"
	"primeradiant.com/serf/llm/providers/openaicompat"
)

// provider couples a real adapter with the encoder that produces wire bytes its
// decoder consumes. driveStream runs the adapter's public Stream against an
// httptest server returning those bytes, then accumulates the final Response —
// exercising the genuine end-to-end decode path, not an in-package shortcut.
type provider struct {
	name   string
	encode func(logicalResponse) []byte
	drive  func(sse []byte) (*llm.Response, error)
}

func driveStream(stream func(ctx context.Context, req llm.Request) (llm.Stream, error), sse []byte, srv *sseServer) (*llm.Response, error) {
	srv.set(sse)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := stream(ctx, llm.Request{Model: "diff-model", Messages: []llm.Message{llm.User("hi")}})
	if err != nil {
		return nil, err
	}
	defer func() { _ = s.Close() }()
	acc := llm.NewStreamAccumulator()
	for ev := range s.Events() {
		if ev.Type == llm.StreamEventError {
			return nil, fmt.Errorf("%s stream error: %w", srv.label, ev.Err)
		}
		acc.Process(ev)
	}
	return acc.Response(), nil
}

// sseServer returns a fixed SSE body for every request. The body is swapped
// under a mutex before each drive; Go fuzzing calls the target sequentially per
// worker, but the lock keeps it safe regardless.
type sseServer struct {
	label string
	mu    sync.Mutex
	body  []byte
	srv   *httptest.Server
}

func newSSEServer(label string) *sseServer {
	s := &sseServer{label: label}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		body := append([]byte(nil), s.body...)
		s.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	return s
}

func (s *sseServer) set(body []byte) {
	s.mu.Lock()
	s.body = body
	s.mu.Unlock()
}

func (s *sseServer) close() { s.srv.Close() }

// providers builds the four real adapters wired to their own httptest servers.
// The caller must invoke the returned cleanup to release the servers.
func providers() ([]provider, func()) {
	anthSrv := newSSEServer("anthropic")
	googSrv := newSSEServer("google")
	oaiSrv := newSSEServer("openai")
	compatSrv := newSSEServer("openaicompat")

	anth := &anthropic.Adapter{BaseURL: anthSrv.srv.URL}
	goog := &google.Adapter{BaseURL: googSrv.srv.URL}
	oai := &openai.Adapter{BaseURL: oaiSrv.srv.URL}
	compat := &openaicompat.Adapter{BaseURL: compatSrv.srv.URL}

	ps := []provider{
		{
			name:   "anthropic",
			encode: encodeAnthropic,
			drive:  func(sse []byte) (*llm.Response, error) { return driveStream(anth.Stream, sse, anthSrv) },
		},
		{
			name:   "google",
			encode: encodeGoogle,
			drive:  func(sse []byte) (*llm.Response, error) { return driveStream(goog.Stream, sse, googSrv) },
		},
		{
			name:   "openai",
			encode: encodeOpenAIResponses,
			drive:  func(sse []byte) (*llm.Response, error) { return driveStream(oai.Stream, sse, oaiSrv) },
		},
		{
			name:   "openaicompat",
			encode: encodeOpenAICompat,
			drive:  func(sse []byte) (*llm.Response, error) { return driveStream(compat.Stream, sse, compatSrv) },
		},
	}
	cleanup := func() {
		anthSrv.close()
		googSrv.close()
		oaiSrv.close()
		compatSrv.close()
	}
	return ps, cleanup
}

// projection is the canonical, provider-agnostic view of a decoded response —
// the fields the differential oracle requires every adapter to agree on. See
// the ALLOW-LIST comment below for everything deliberately excluded.
type projection struct {
	Text   string
	Tools  []toolProj
	Finish string
	In     int
	Out    int
	Total  int
}

type toolProj struct {
	Name string
	Args string // canonical JSON (sorted keys, no whitespace)
}

func project(r *llm.Response) projection {
	p := projection{
		Text:   r.Text(),
		Finish: r.Finish.Reason,
		In:     r.Usage.InputTokens,
		Out:    r.Usage.OutputTokens,
		Total:  r.Usage.TotalTokens,
	}
	for _, tc := range r.ToolCalls() {
		p.Tools = append(p.Tools, toolProj{Name: tc.Name, Args: canonJSON(tc.Arguments)})
	}
	return p
}

// canonJSON normalizes a tool-call arguments payload so wire-format differences
// (key order, whitespace) don't read as divergence. Invalid JSON falls back to
// the verbatim bytes (a real divergence if only one provider produced garbage).
func canonJSON(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "raw:" + string(raw)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "raw:" + string(raw)
	}
	return string(b)
}

func (p projection) String() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "text=%q finish=%s usage=(%d,%d,%d) tools=[", p.Text, p.Finish, p.In, p.Out, p.Total)
	for i, t := range p.Tools {
		if i > 0 {
			sb.WriteString(", ")
		}
		fmt.Fprintf(&sb, "%s%s", t.Name, t.Args)
	}
	sb.WriteString("]")
	return sb.String()
}

func equalProjections(a, b projection) bool {
	if a.Text != b.Text || a.Finish != b.Finish || a.In != b.In || a.Out != b.Out || a.Total != b.Total {
		return false
	}
	if len(a.Tools) != len(b.Tools) {
		return false
	}
	for i := range a.Tools {
		if a.Tools[i] != b.Tools[i] {
			return false
		}
	}
	return true
}

// ALLOW-LIST — fields INTENTIONALLY excluded from the cross-provider
// equivalence check, each legitimately provider-specific. The oracle compares
// only the projection (text, tool name+canonical args, finish class, usage
// triple); everything below is excluded by construction (project() never reads
// it), so a difference in these is NOT a finding:
//
//   - Response.ID / Model / Provider — provider identity & ids; each adapter
//     stamps its own ("msg_*", "resp_*", "c_*", provider name).
//   - Response.Raw / Usage.Raw / FinishReason.Raw — verbatim provider payloads
//     and the un-normalized wire stop reason ("end_turn" vs "STOP" vs "stop");
//     only the normalized FinishReason.Reason class is compared.
//   - tool call ID / ItemID / Type / ThoughtSignature — Gemini mints a random
//     ULID ("call_"+ulid), others carry the wire id; Type is cosmetic;
//     ThoughtSignature is Gemini-only continuation state.
//   - ALL reasoning/thinking content — encoded fundamentally differently:
//     Anthropic/Google/openaicompat carry thinking TEXT, OpenAI-Responses carries
//     encrypted_content + a summary array (no .Text). Comparing it would flag a
//     legitimate encoding difference, not decode drift.
//   - Usage pointer fields (ReasoningTokens, ReasoningTokensEstimated, Cache*) —
//     provider-specific native counts / char-based estimates.
//   - RawRequestBody / RawResponseBody / Warnings / RateLimit / ContentPart.Phase
//     — framing/diagnostic metadata, not model content.

// crossProviderDivergence drives every provider, projects, and returns a
// non-empty message describing the first divergence from the reference
// (provider 0), or "" if all providers agree. A provider that fails to decode
// (or never completes its stream) is itself reported as a divergence.
func crossProviderDivergence(t *testing.T, ps []provider, lr logicalResponse) string {
	t.Helper()
	type result struct {
		name string
		proj projection
	}
	var results []result
	for _, p := range ps {
		sse := p.encode(lr)
		resp, err := p.drive(sse)
		if err != nil {
			return fmt.Sprintf("provider %s failed to decode: %v\n  sse=%q", p.name, err, sse)
		}
		if resp == nil {
			return fmt.Sprintf("provider %s produced no response (stream never completed)\n  sse=%q", p.name, sse)
		}
		results = append(results, result{name: p.name, proj: project(resp)})
	}
	ref := results[0]
	for _, r := range results[1:] {
		if !equalProjections(ref.proj, r.proj) {
			return fmt.Sprintf(
				"cross-provider decode divergence:\n  %-12s %s\n  %-12s %s\n  logical=%+v",
				ref.name, ref.proj.String(), r.name, r.proj.String(), lr)
		}
	}
	return ""
}

// FuzzCrossProviderDifferential is serf's first differential oracle. It
// generates one canonical logical response from the fuzz bytes, encodes it into
// all four providers' SSE wire formats, decodes each back through the REAL
// adapter's public Stream path, and asserts the decoded responses are
// equivalent modulo the documented allow-list (see allowList). A divergence is
// either a genuine cross-provider decode bug or an over-strict equivalence —
// investigate which; do not weaken the check to hide a real bug.
func FuzzCrossProviderDifferential(f *testing.F) {
	seeds := [][]byte{
		{},                                   // empty → minimal text "x", stop
		{0x00},                               // text-only path
		{0x01, 0x05, 0x41, 0x42},             // one tool call
		{0x02, 0x03, 0x61, 0x62, 0x07, 0x09}, // two tool calls
		{0x00, 0x10, 0x20, 0x30, 0x40, 0x50}, // text + reasoning + usage
		{0x01, 0x02, 0x63, 0x64, 0x65, 0x66, 0xAA, 0xBB},       // tool + reasoning
		{0x00, 0x00, 0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06}, // length finish
	}
	for _, s := range seeds {
		f.Add(s)
	}

	ps, cleanup := providers()
	f.Cleanup(cleanup)

	f.Fuzz(func(t *testing.T, raw []byte) {
		lr := generate(raw)
		if msg := crossProviderDivergence(t, ps, lr); msg != "" {
			t.Fatal(msg)
		}
	})
}

// TestDifferentialSanity is a fast, explicit seed check (no fuzzing): a fixed
// logical response with text, reasoning, two tool calls and a usage triple must
// decode equivalently across all four real adapters. It documents the oracle's
// intent and guards the encoders/drivers independently of the fuzz engine.
func TestDifferentialSanity(t *testing.T) {
	ps, cleanup := providers()
	defer cleanup()

	cases := []logicalResponse{
		{Text: "hello world", Finish: "stop", InTok: 7, OutTok: 3},
		{Text: "stopped early", Finish: "length", InTok: 10, OutTok: 5},
		{Text: "", Finish: "tool_calls", InTok: 4, OutTok: 8, Tools: []logicalToolCall{
			{Name: "shell", Args: map[string]string{"cmd": "ls"}},
		}},
		{Text: "calling tools", Reasoning: "let me think", Finish: "tool_calls", InTok: 11, OutTok: 9, Tools: []logicalToolCall{
			{Name: "grep", Args: map[string]string{"q": "needle", "path": "."}},
			{Name: "shell", Args: map[string]string{"cmd": "pwd"}},
		}},
	}

	for i, lr := range cases {
		t.Run(fmt.Sprintf("case%d", i), func(t *testing.T) {
			if msg := crossProviderDivergence(t, ps, lr); msg != "" {
				t.Fatal(msg)
			}
		})
	}
}
