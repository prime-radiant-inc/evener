package protocolhttp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"strconv"
	"testing"
)

// randomBodyValue builds a value of one of the types the protocol builders put
// in a body, plus types that must fall back. It is seeded so the corpus is
// reproducible, and it is deliberately hostile: it emits the escapes, control
// bytes, invalid UTF-8, and float edges that a hand-written encoder gets wrong.
func randomBodyValue(r *rand.Rand, depth int) any {
	kinds := 11
	if depth >= 3 {
		kinds = 6 // stop growing the tree
	}
	switch r.Intn(kinds) {
	case 0:
		return nil
	case 1:
		return randomBodyString(r)
	case 2:
		return r.Intn(2) == 0
	case 3:
		switch r.Intn(4) {
		case 0:
			return r.Intn(1000000) - 500000
		case 1:
			return r.Int63() - (1 << 62)
		case 2:
			return math.Copysign(r.Float64()*math.Pow(10, float64(r.Intn(40)-20)), r.Float64()-0.5)
		default:
			return []float64{0, math.Copysign(0, -1), 1e-7, 1e21, math.MaxFloat64, 5e-324}[r.Intn(6)]
		}
	case 4:
		return json.Number(strconv.FormatInt(r.Int63(), 10))
	case 5:
		return fmt.Appendf(nil, "bytes-%d", r.Intn(1000))
	case 6:
		m := make(map[string]any, r.Intn(5))
		for range r.Intn(5) {
			m[randomBodyString(r)] = randomBodyValue(r, depth+1)
		}
		if r.Intn(8) == 0 {
			return map[string]any(nil)
		}
		return m
	case 7:
		n := r.Intn(4)
		if r.Intn(8) == 0 {
			return []map[string]any(nil)
		}
		out := make([]map[string]any, 0, n)
		for range n {
			if r.Intn(6) == 0 {
				out = append(out, nil)
				continue
			}
			out = append(out, map[string]any{"k": randomBodyValue(r, depth+1)})
		}
		return out
	case 8:
		n := r.Intn(4)
		if r.Intn(8) == 0 {
			return []any(nil)
		}
		out := make([]any, 0, n)
		for range n {
			out = append(out, randomBodyValue(r, depth+1))
		}
		return out
	case 9:
		n := r.Intn(4)
		out := make([]string, 0, n)
		for range n {
			out = append(out, randomBodyString(r))
		}
		return out
	default:
		return json.RawMessage(fmt.Sprintf(`{ "raw" : %d, "x": "<&>" }`, r.Intn(100)))
	}
}

// randomBodyString draws from the byte classes the encoder treats differently:
// plain ASCII, HTML metacharacters, JSON escapes, control bytes, DEL, and
// multibyte or invalid sequences.
func randomBodyString(r *rand.Rand) string {
	pieces := []string{
		"plain text", "<tag>", "&amp;", `"quoted"`, `back\slash`, "'single'",
		"\x00", "\x01", "\x1f", "\x7f", "\b", "\f", "\n", "\r", "\t",
		"café", "日本語", "\U0001F600", "\u2028", "\u2029",
	}
	switch r.Intn(6) {
	case 0:
		return ""
	case 1:
		return pieces[r.Intn(len(pieces))]
	case 2:
		return fmt.Sprintf("%s%s", pieces[r.Intn(len(pieces))], pieces[r.Intn(len(pieces))])
	case 3:
		return string([]byte{byte(r.Intn(256)), byte(r.Intn(256))})
	case 4:
		return fmt.Sprintf("%s%d", pieces[r.Intn(len(pieces))], r.Intn(100))
	default:
		return pieces[r.Intn(len(pieces))] + pieces[r.Intn(len(pieces))] + pieces[r.Intn(len(pieces))]
	}
}

// TestMarshalBodyMatchesStdlibRandomized is the differential fuzz that runs as
// a plain test: several thousand seeded random bodies, each encoded by both
// paths. A mismatch names the seed, the body, and both encodings, so it is
// reproducible without the fuzzer.
func TestMarshalBodyMatchesStdlibRandomized(t *testing.T) {
	t.Parallel()
	const seeds = 3000
	for seed := range seeds {
		r := rand.New(rand.NewSource(int64(seed)))
		body := make(map[string]any, 6)
		for range 1 + r.Intn(6) {
			body[randomBodyString(r)] = randomBodyValue(r, 0)
		}
		want, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("seed %d: stdlib marshal of %#v: %v", seed, body, err)
		}
		got, err := MarshalBody(body)
		if err != nil {
			t.Fatalf("seed %d: MarshalBody of %#v: %v", seed, body, err)
		}
		if !bytes.Equal(want, got) {
			t.Fatalf("seed %d: bytes differ\nbody %#v\nwant %s\ngot  %s", seed, body, want, got)
		}
	}
}

// FuzzMarshalBodyMatchesStdlib lets the fuzzer attack the same contract from a
// generated body. It is registered so `make fuzz` picks it up; the seed corpus
// is the fixture list, which keeps a plain `go test` run meaningful.
func FuzzMarshalBodyMatchesStdlib(f *testing.F) {
	for _, fixture := range bodyFixtures() {
		raw, err := json.Marshal(fixture.body)
		if err != nil {
			f.Fatalf("fixture %s: %v", fixture.name, err)
		}
		f.Add(raw)
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		// Both decodings, because they exercise different encoder paths.
		// Without UseNumber every number becomes a float64, which is what
		// reaches appendBodyFloat — UseNumber alone turns them all into
		// json.Number, which falls back to encoding/json and leaves the float
		// encoder unfuzzed.
		for _, useNumber := range []bool{false, true} {
			dec := json.NewDecoder(bytes.NewReader(raw))
			if useNumber {
				dec.UseNumber()
			}
			var body map[string]any
			if err := dec.Decode(&body); err != nil {
				continue // not a JSON object; the encoder is not asked about it
			}
			want, err := json.Marshal(body)
			if err != nil {
				t.Fatalf("stdlib (useNumber=%t): %v", useNumber, err)
			}
			got, err := MarshalBody(body)
			if err != nil {
				t.Fatalf("MarshalBody (useNumber=%t): %v", useNumber, err)
			}
			if !bytes.Equal(want, got) {
				t.Fatalf("bytes differ for %s (useNumber=%t)\nwant %s\ngot  %s", raw, useNumber, want, got)
			}
		}
	})
}
