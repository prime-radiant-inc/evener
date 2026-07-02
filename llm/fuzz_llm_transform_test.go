package llm

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"
)

// FuzzLmParseRetryAfter drives ParseRetryAfter, the Retry-After header parser
// that turns a provider's raw header value into a bounded backoff. Only the
// empty-string branch was ever driven by a fuzzer; the delay-seconds and
// HTTP-date branches (the ones a real 429/503 exercises) were unit-tested only.
//
// Oracles (beyond never-panic):
//   - determinism: the same (value, now) yields an identical result.
//   - non-negative: a returned wait is never negative, unconditionally. This
//     fuzzer FOUND a real bug — an absurd delay-seconds value (e.g.
//     "10000000000") overflowed int64 nanoseconds and wrapped to a NEGATIVE
//     Duration, so a hostile proxy could turn a rate-limit backoff into a
//     negative (effectively zero) wait. Fixed by saturating the conversion in
//     ParseRetryAfter; this oracle now guards the fix.
//   - integer round-trip: a non-negative, in-range integer parses back to
//     exactly that many seconds.
//   - HTTP-date semantics: a date at/in the past clamps to 0; a future date of
//     k seconds ahead yields exactly k seconds against a whole-second now.
func FuzzLmParseRetryAfter(f *testing.F) {
	f.Add("", int64(0), 0)
	f.Add("120", int64(1_700_000_000), 30)
	f.Add("0", int64(0), 0)
	f.Add("  42 ", int64(1), 5)
	f.Add("-5", int64(0), 0)
	f.Add("not-a-number", int64(0), 0)
	f.Add("10000000000", int64(0), 0)   // overflow regime (the bug)
	f.Add("9999999999999", int64(0), 0) // still parses via Atoi, saturates
	f.Add("Wed, 21 Oct 2099 07:28:00 GMT", int64(1_700_000_000), 0)

	f.Fuzz(func(t *testing.T, raw string, nowUnix int64, futureDelta int) {
		// Whole-second now so http.ParseTime's second granularity round-trips
		// exactly against the deltas below.
		if nowUnix < 0 {
			nowUnix = -nowUnix
		}
		now := time.Unix(nowUnix, 0).UTC()

		got := ParseRetryAfter(raw, now)
		if got2 := ParseRetryAfter(raw, now); !durEqual(got, got2) {
			t.Fatalf("ParseRetryAfter nondeterministic for %q", raw)
		}
		if got != nil && *got < 0 {
			t.Fatalf("ParseRetryAfter(%q) returned negative wait %v", raw, *got)
		}

		// Integer round-trip: build a non-negative, non-overflowing seconds value
		// and require an exact match.
		if futureDelta < 0 {
			futureDelta = -futureDelta
		}
		secs := futureDelta % 100_000 // in-range, no overflow
		if d := ParseRetryAfter(strconv.Itoa(secs), now); d == nil || *d != time.Duration(secs)*time.Second {
			t.Fatalf("integer round-trip failed for %d: got %v", secs, d)
		}

		// HTTP-date branch: a date k seconds in the future yields exactly k
		// seconds; the same date k seconds in the past clamps to 0.
		future := now.Add(time.Duration(secs) * time.Second)
		if d := ParseRetryAfter(future.Format(http.TimeFormat), now); d == nil || *d != time.Duration(secs)*time.Second {
			t.Fatalf("future-date parse failed: k=%d got %v", secs, d)
		}
		past := now.Add(-time.Duration(secs) * time.Second)
		if d := ParseRetryAfter(past.Format(http.TimeFormat), now); d == nil || *d < 0 {
			t.Fatalf("past-date parse should clamp to >=0, got %v", d)
		}
	})
}

func durEqual(a, b *time.Duration) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// FuzzLmCloneProviderOptions drives cloneProviderOptions / cloneProviderOptionValue,
// the recursive deep-clone that snapshots a request's ProviderOptions before it
// is stashed for a Responses-API continuation. No fuzzer drove it — a shallow
// clone here would let a later mutation of the stored request corrupt the live
// one. The fuzzed nested value is assembled deterministically from a byte
// script so the recursion, container, and scalar arms are all reachable.
//
// Oracles (beyond never-panic):
//   - identity: the clone deep-equals the original.
//   - determinism: cloning twice yields deep-equal results.
//   - deep independence: aggressively mutating the clone's containers leaves the
//     original byte-for-byte unchanged (no shared mutable structure).
func FuzzLmCloneProviderOptions(f *testing.F) {
	f.Add([]byte{}, 0)
	f.Add([]byte{0, 1, 2, 3, 4, 5, 6, 7}, 3)
	f.Add([]byte{5, 5, 5, 2, 2, 1, 1, 9, 9}, 4)
	f.Add([]byte{2, 0, 3, 1, 6, 4, 7, 8, 2, 3}, 5)
	// One constant-byte seed per builder arm so the seed-corpus replay drives
	// every clone branch (nested map, []any, []string, map[string]string, and
	// each scalar leaf) deterministically, not only under a fuzz search.
	for b := byte(0); b <= 8; b++ {
		f.Add(bytes.Repeat([]byte{b}, 12), int(b))
	}

	f.Fuzz(func(t *testing.T, script []byte, seed int) {
		orig := buildProviderOptions(script, seed)

		clone := cloneProviderOptions(orig)
		if !reflect.DeepEqual(orig, clone) {
			t.Fatalf("clone not equal to original:\n orig=%#v\nclone=%#v", orig, clone)
		}
		if clone2 := cloneProviderOptions(orig); !reflect.DeepEqual(clone, clone2) {
			t.Fatalf("cloneProviderOptions nondeterministic")
		}

		// A fresh snapshot to compare the original against after we vandalize the
		// clone; if any mutable structure is shared, this will diverge.
		snapshot := cloneProviderOptions(orig)
		vandalize(clone)
		if !reflect.DeepEqual(orig, snapshot) {
			t.Fatalf("mutating the clone changed the original (shallow copy):\n orig=%#v\n snap=%#v", orig, snapshot)
		}

		// nil in -> nil out is part of the contract.
		if cloneProviderOptions(nil) != nil {
			t.Fatalf("cloneProviderOptions(nil) should be nil")
		}
	})
}

// buildProviderOptions assembles an arbitrary top-level map[string]any from a
// byte script so the fuzzer can reach every clone arm (nested maps, []any,
// []string, map[string]string, and scalar leaves).
func buildProviderOptions(script []byte, seed int) map[string]any {
	pos := 0
	next := func() byte {
		if len(script) == 0 {
			return byte(seed)
		}
		b := script[pos%len(script)]
		pos++
		return b
	}
	n := int(next()%4) + 1
	out := make(map[string]any, n)
	for i := 0; i < n; i++ {
		out["k"+strconv.Itoa(i)] = buildValue(next, 0)
	}
	return out
}

func buildValue(next func() byte, depth int) any {
	kind := next() % 9
	if depth >= 3 {
		kind %= 5 // force a leaf past the depth cap
	}
	switch kind {
	case 0:
		return "s" + strconv.Itoa(int(next()))
	case 1:
		return int(next())
	case 2:
		return float64(next()) / 3
	case 3:
		return next()%2 == 0
	case 4:
		return nil
	case 5:
		m := map[string]any{}
		for i := 0; i < int(next()%3); i++ {
			m["n"+strconv.Itoa(i)] = buildValue(next, depth+1)
		}
		return m
	case 6:
		s := []any{}
		for i := 0; i < int(next()%3); i++ {
			s = append(s, buildValue(next, depth+1))
		}
		return s
	case 7:
		s := []string{}
		for i := 0; i < int(next()%3); i++ {
			s = append(s, "t"+strconv.Itoa(int(next())))
		}
		return s
	default:
		m := map[string]string{}
		for i := 0; i < int(next()%3); i++ {
			m["p"+strconv.Itoa(i)] = "v" + strconv.Itoa(int(next()))
		}
		return m
	}
}

// vandalize mutates every mutable container reachable from v in place. If the
// clone shared any structure with its source, this corrupts the source too.
func vandalize(v any) {
	switch typed := v.(type) {
	case map[string]any:
		for k, item := range typed {
			vandalize(item)
			typed[k] = "MUT"
		}
		typed["__vandal__"] = "MUT"
	case map[string]string:
		for k := range typed {
			typed[k] = "MUT"
		}
		typed["__vandal__"] = "MUT"
	case []any:
		for i, item := range typed {
			vandalize(item)
			typed[i] = "MUT"
		}
	case []string:
		for i := range typed {
			typed[i] = "MUT"
		}
	}
}

// FuzzLmEstimateMessageInputParts drives estimateMessageInputParts and, through
// it, imageDimensions over a full multimodal message: every content kind plus
// real inline image bytes decoded for their dimensions. The existing token
// fuzzers only ever fed text messages and a non-decodable fallback image, so
// the content-kind switch and the image-decode path were unit-tested only.
//
// Oracles (beyond never-panic):
//   - non-negative: both accumulators (chars, tokens) stay >= 0.
//   - determinism: the same message yields identical (chars, tokens), and the
//     whole-list estimate is stable.
//   - monotonic: appending another content part never lowers either accumulator
//     (the estimator must not lose content).
//   - imageDimensions determinism on the raw bytes, and a decodable seed image
//     reports positive dimensions.
func FuzzLmEstimateMessageInputParts(f *testing.F) {
	f.Add("openai", "gpt-5.2", "hello", pngBytes(4, 7), "image/png", "high", true)
	f.Add("anthropic", "claude-opus-4-5", "ctx", jpegBytes(9, 3), "image/jpeg", "auto", false)
	f.Add("google", "gemini-2.5-pro", "", gifBytes(2, 5), "image/gif", "low", true)
	f.Add("", "", "x", []byte("not an image"), "image/png", "", false)
	f.Add("openai", "o3", "t", []byte{}, "", "low", true)

	f.Fuzz(func(t *testing.T, provider, model, text string, imgData []byte, mediaType, detail string, useLocalPath bool) {
		// Bound inline bytes so decode stays cheap; content is still adversarial.
		if len(imgData) > 1<<16 {
			imgData = imgData[:1<<16]
		}

		img := &ImageData{Data: imgData, MediaType: mediaType, Detail: detail}
		if useLocalPath && len(imgData) > 0 {
			// Cover the local-path decode arm safely: our own temp file, never a
			// fuzzer-chosen path.
			p := filepath.Join(t.TempDir(), "img.bin")
			if err := os.WriteFile(p, imgData, 0o600); err == nil {
				img = &ImageData{URL: p, MediaType: mediaType, Detail: detail}
			}
		}

		// imageDimensions determinism.
		w1, h1, ok1 := imageDimensions(img)
		w2, h2, ok2 := imageDimensions(img)
		if w1 != w2 || h1 != h2 || ok1 != ok2 {
			t.Fatalf("imageDimensions nondeterministic: (%d,%d,%v) vs (%d,%d,%v)", w1, h1, ok1, w2, h2, ok2)
		}
		if ok1 && (w1 <= 0 || h1 <= 0) {
			t.Fatalf("imageDimensions ok but non-positive dims: %dx%d", w1, h1)
		}

		msg := Message{
			Role:       RoleUser,
			Name:       "n",
			ToolCallID: "tc",
			Content: []ContentPart{
				{Kind: ContentText, Text: text},
				{Kind: ContentImage, Image: img},
				{Kind: ContentAudio, Audio: &AudioData{MediaType: mediaType}},
				{Kind: ContentDocument, Document: &DocumentData{URL: "http://x/y", MediaType: mediaType, FileName: "f.pdf"}},
				{Kind: ContentToolCall, ToolCall: &ToolCallData{ID: "id", Name: "shell", Arguments: json.RawMessage(`{"a":1}`)}},
				{Kind: ContentToolResult, ToolResult: &ToolResultData{ToolCallID: "id", Name: "shell", Content: text, ImageData: imgData, ImageMediaType: mediaType}},
				{Kind: ContentThinking, Thinking: &ThinkingData{Text: text, Signature: "sig"}},
				{Kind: ContentWebSearch, WebSearch: &WebSearchData{Query: text, Raw: json.RawMessage(`{"r":1}`)}},
				{Kind: ContentKind("unknown-kind"), Text: text},
			},
		}

		chars, tokens := estimateMessageInputParts(provider, model, msg)
		if chars < 0 || tokens < 0 {
			t.Fatalf("negative accumulators: chars=%d tokens=%d", chars, tokens)
		}
		if c2, tk2 := estimateMessageInputParts(provider, model, msg); c2 != chars || tk2 != tokens {
			t.Fatalf("estimateMessageInputParts nondeterministic: (%d,%d) vs (%d,%d)", chars, tokens, c2, tk2)
		}

		// Monotonic: an extra text part never lowers either accumulator.
		more := msg
		more.Content = append(append([]ContentPart(nil), msg.Content...), ContentPart{Kind: ContentText, Text: "extra"})
		c3, tk3 := estimateMessageInputParts(provider, model, more)
		if c3 < chars || tk3 < tokens {
			t.Fatalf("appending a part lowered an accumulator: (%d,%d) -> (%d,%d)", chars, tokens, c3, tk3)
		}

		// Whole-list estimate is non-negative and stable.
		got := estimateMessagesInputTokens(provider, model, []Message{msg})
		if got < 0 {
			t.Fatalf("negative message estimate: %d", got)
		}
		if again := estimateMessagesInputTokens(provider, model, []Message{msg}); again != got {
			t.Fatalf("estimateMessagesInputTokens nondeterministic: %d vs %d", got, again)
		}
	})
}

func pngBytes(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 1, G: 2, B: 3, A: 255})
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func jpegBytes(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, nil)
	return buf.Bytes()
}

func gifBytes(w, h int) []byte {
	img := image.NewPaletted(image.Rect(0, 0, w, h), color.Palette{color.Black, color.White})
	var buf bytes.Buffer
	_ = gif.Encode(&buf, img, nil)
	return buf.Bytes()
}
