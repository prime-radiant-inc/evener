package protocolhttp

import (
	"bytes"
	"encoding/json"
	"math"
	"testing"
)

// bodyFixture is one value the encoder must reproduce. Every fixture is run
// through both paths and compared byte for byte, so a fixture that disagrees
// fails rather than being skipped.
type bodyFixture struct {
	name string
	body map[string]any
}

func bodyFixtures() []bodyFixture {
	return []bodyFixture{
		{"empty", map[string]any{}},
		{"nilBodyValue", map[string]any{"a": nil}},
		{"scalars", map[string]any{"s": "x", "b": true, "bf": false, "i": 0, "neg": -17}},
		{"numbers", map[string]any{
			"i64":      int64(1 << 40),
			"f":        1.5,
			"fneg":     -0.000001,
			"small":    1e-7,
			"big":      1e21,
			"bigger":   1e22,
			"zero":     0.0,
			"negzero":  math.Copysign(0, -1),
			"third":    1.0 / 3.0,
			"pointone": 0.1 + 0.2,
			"huge":     math.MaxFloat64,
			"tiny":     5e-324,
			"round":    1e20,
		}},
		{"escapedStrings", map[string]any{
			"plain":   "plain text",
			"html":    "<html> & \"quotes\" \\ 'single'",
			"control": "tab\t newline\n cr\r backspace\b formfeed\f",
			"low":     "\x00\x01\x1f",
			"del":     "\x7f",
			"empty":   "",
			"tags":    "</script><script>alert(1)</script>",
		}},
		{"multibyteStrings", map[string]any{
			"latin":     "café",
			"cjk":       "日本語テキスト",
			"emoji":     "\U0001F600 emoji",
			"linesep":   "before\u2028after",
			"parasep":   "before\u2029after",
			"invalid":   "invalid \xff byte",
			"loneHigh":  "surrogate \xed\xa0\x80",
			"mixed":     "ok then \xff then <tag>",
			"truncated": "truncated \xe6\x97",
		}},
		{"nested", map[string]any{
			"a": []any{map[string]any{"b": []any{1, "two", nil, true}}, []string{"x", "y"}},
			"c": map[string]any{},
			"d": map[string]any{"e": map[string]any{"f": map[string]any{"g": "deep"}}},
		}},
		{"typedSlices", map[string]any{
			"maps":      []map[string]any{{"k": "v"}, {"k": "w"}},
			"strings":   []string{"a", "", "<&>"},
			"anys":      []any{"a", 1, nil, true, 2.5},
			"mapOfMaps": []map[string]any{{"nested": map[string]any{"x": 1}}},
		}},
		{"nilContainers", map[string]any{
			"map":    map[string]any(nil),
			"maps":   []map[string]any(nil),
			"anys":   []any(nil),
			"string": []string(nil),
		}},
		{"nilElements", map[string]any{
			// A nil element is null in encoding/json even though a nil map
			// value would encode as {} if it were encoded at all.
			"maps":   []map[string]any{nil, {"a": 1}},
			"nested": []any{[]map[string]any{nil}, map[string]any{"b": nil}},
		}},
		{"emptyContainers", map[string]any{
			"map":    map[string]any{},
			"maps":   []map[string]any{},
			"anys":   []any{},
			"string": []string{},
		}},
		{"fallbackTypes", map[string]any{
			"raw":       json.RawMessage(`{ "a" : "<tag> & stuff", "b":"\u2028" }`),
			"rawArray":  json.RawMessage(`[1, 2,  3]`),
			"stringMap": map[string]string{"k": "<&>"},
			"bytes":     []byte{0x01, 0x02, 0xFA},
			"number":    json.Number("12345678901234567890"),
			"ints":      []int{1, 2, 3},
			"float32":   float32(1.5),
			"uint8":     uint8(200),
		}},
		{"keysNeedingEscapes", map[string]any{
			"<key>":      1,
			"quote\"key": 2,
			"newline\nk": 3,
			"é":          4,
			"":           5,
		}},
	}
}

// TestMarshalBodyMatchesStdlib is the differential contract: for every
// fixture, MarshalBody must return the bytes encoding/json returns.
func TestMarshalBodyMatchesStdlib(t *testing.T) {
	t.Parallel()
	for _, f := range bodyFixtures() {
		t.Run(f.name, func(t *testing.T) {
			t.Parallel()
			want, err := json.Marshal(f.body)
			if err != nil {
				t.Fatalf("stdlib marshal: %v", err)
			}
			got, err := MarshalBody(f.body)
			if err != nil {
				t.Fatalf("MarshalBody: %v", err)
			}
			if !bytes.Equal(want, got) {
				t.Fatalf("bytes differ from encoding/json\nwant %s\ngot  %s", want, got)
			}
		})
	}
}

// TestMarshalBodyReturnsOwnCopy pins that the returned bytes do not alias the
// pooled scratch buffer: a second call must not be able to change the first
// call's result, which is what the wire body is built from.
func TestMarshalBodyReturnsOwnCopy(t *testing.T) {
	t.Parallel()
	first, err := MarshalBody(map[string]any{"a": "first call payload"})
	if err != nil {
		t.Fatal(err)
	}
	retained := append([]byte(nil), first...)
	for i := 0; i < 4; i++ {
		if _, err := MarshalBody(map[string]any{"b": "another call's much longer payload body"}); err != nil {
			t.Fatal(err)
		}
	}
	again, err := MarshalBody(map[string]any{"a": "first call payload"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, retained) {
		t.Fatalf("first result changed after later calls: %s", first)
	}
	if !bytes.Equal(first, again) {
		t.Fatalf("same body encoded differently: %s vs %s", first, again)
	}
}

// TestMarshalBodyAllocationsDoNotScaleWithBody pins the property that made the
// old path expensive: encoding/json allocates per node, so its allocation
// count grows with history. MarshalBody sizes one buffer and then walks the
// tree with no per-node allocation, so a 400-turn body must cost about what a
// 4-turn body costs.
func TestMarshalBodyAllocationsDoNotScaleWithBody(t *testing.T) {
	// Not parallel: testing.AllocsPerRun pins GOMAXPROCS to 1 and panics in a
	// parallel test.
	small := benchPrepareBody(4, 512)
	large := benchPrepareBody(400, 512)
	measure := func(body map[string]any) float64 {
		return testing.AllocsPerRun(20, func() {
			if _, err := MarshalBody(body); err != nil {
				t.Fatal(err)
			}
		})
	}
	smallAllocs, largeAllocs := measure(small), measure(large)
	// 100x the turns may cost more key slices, but nothing per node: the old
	// encoder allocated once per map, slice and string (25,600 at 800 turns).
	if largeAllocs > 4*smallAllocs+8 {
		t.Fatalf("MarshalBody allocated %.0f times for 400 turns and %.0f for 4 turns: allocation scales with history", largeAllocs, smallAllocs)
	}
}
