package difftest

import (
	"encoding/json"
	"fmt"
)

// logicalResponse is the provider-agnostic content we round-trip through every
// adapter. It is the "canonical" response the differential oracle generates
// once and then re-expresses in each provider's wire format. Only the fields
// here are meant to survive decode identically across providers; everything a
// real decoder additionally attaches (ids, raw payloads, reasoning encoding) is
// allow-listed out of the equivalence check (see projection / equalProjections).
type logicalResponse struct {
	Text      string            // assistant visible text (may be empty when tools are present)
	Reasoning string            // optional thinking text (allow-listed: encoded differently per provider)
	Tools     []logicalToolCall // ordered tool calls
	Finish    string            // canonical finish class: "stop" | "length" | "tool_calls"
	InTok     int               // usage: new input tokens
	OutTok    int               // usage: output tokens
}

// logicalToolCall is one requested tool call. Args is always a non-empty object
// so that every provider serializes a valid JSON object value (Anthropic's
// input_json_delta, Gemini's functionCall.args, OpenAI's arguments string).
type logicalToolCall struct {
	Name string
	Args map[string]string
}

// argsJSON renders the tool arguments as a canonical JSON object string. Go's
// json.Marshal sorts map keys, so two providers that reconstruct the arguments
// from different wire shapes still produce byte-identical JSON here.
func (tc logicalToolCall) argsJSON() string {
	b, err := json.Marshal(tc.Args)
	if err != nil {
		panic(fmt.Sprintf("difftest: marshal tool args: %v", err)) // generator only emits string maps
	}
	return string(b)
}

// totalTok is the usage total every encoder emits. All four adapters normalize
// total to input+output when no cache tokens are present (verified against each
// parseUsage / ParseChatUsage), so a single definition keeps the triple
// consistent across providers.
func (lr logicalResponse) totalTok() int { return lr.InTok + lr.OutTok }

// byteSource turns a fuzz input into a deterministic stream of small choices.
// Exhausted input reads as zero, so short seeds remain valid and reproducible.
type byteSource struct {
	b []byte
	i int
}

func (s *byteSource) next() byte {
	if s.i >= len(s.b) {
		return 0
	}
	v := s.b[s.i]
	s.i++
	return v
}

// intn returns a value in [0,n). n<=0 yields 0.
func (s *byteSource) intn(n int) int {
	if n <= 0 {
		return 0
	}
	return int(s.next()) % n
}

// textAlphabet deliberately includes a space, a double-quote and a newline so
// the generated strings exercise each encoder's JSON escaping. Because every
// encoder embeds strings via json.Marshal, correct escaping must round-trip
// identically through all four decoders.
var textAlphabet = []rune{'a', 'b', 'c', 'd', 'e', ' ', '"', '\n'}

func (s *byteSource) genString(maxLen int) string {
	n := s.intn(maxLen + 1)
	r := make([]rune, n)
	for i := range r {
		r[i] = textAlphabet[s.intn(len(textAlphabet))]
	}
	return string(r)
}

// generate derives a logical response from fuzz bytes. It guarantees at least
// one content unit (text or a tool call) so the OpenAI adapter's Responses path
// never trips its empty-stream fallback to Chat Completions, which would change
// the decode path under test.
func generate(b []byte) logicalResponse {
	s := &byteSource{b: b}
	var lr logicalResponse

	nTools := s.intn(4) // 0..3 tool calls
	for i := 0; i < nTools; i++ {
		tc := logicalToolCall{
			Name: "t" + s.genName(),
			Args: map[string]string{},
		}
		nArgs := 1 + s.intn(3) // 1..3 args, always non-empty
		for k := 0; k < nArgs; k++ {
			tc.Args[fmt.Sprintf("k%d", k)] = s.genString(4)
		}
		lr.Tools = append(lr.Tools, tc)
	}

	lr.Text = s.genString(7)
	if nTools == 0 && lr.Text == "" {
		lr.Text = "x" // ensure at least one content unit
	}

	if s.next()%2 == 0 {
		lr.Reasoning = s.genString(6)
	}

	switch {
	case nTools > 0:
		// Every adapter forces tool_calls when the message carries tool calls,
		// regardless of the wire stop reason, so this is the only consistent class.
		lr.Finish = "tool_calls"
	case s.next()%2 == 0:
		lr.Finish = "length"
	default:
		lr.Finish = "stop"
	}

	lr.InTok = s.intn(120)
	lr.OutTok = s.intn(120)
	return lr
}

// genName returns a short non-empty lowercase identifier for a tool name.
func (s *byteSource) genName() string {
	n := 1 + s.intn(4)
	r := make([]byte, n)
	for i := range r {
		r[i] = byte('a' + s.intn(6))
	}
	return string(r)
}

// qs returns s as a JSON-quoted string literal (with surrounding quotes),
// e.g. "a\"b" -> `"a\"b"`. Used to embed generated strings into hand-built SSE.
func qs(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(fmt.Sprintf("difftest: quote string: %v", err))
	}
	return string(b)
}
