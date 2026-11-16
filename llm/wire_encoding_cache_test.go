package llm

import (
	"bytes"
	"encoding/base64"
	"testing"
)

// cacheTestData is a payload distinctive enough that a wrong reuse is visible
// in the returned encoding rather than only in a counter.
func cacheTestData(seed byte) []byte {
	data := make([]byte, 4096)
	for i := range data {
		data[i] = seed ^ byte(i*7)
	}
	return data
}

func newTestWireEncodingCache() *wireEncodingCache {
	return newWireEncodingCache(wireEncodingCacheBudgetBytes, wireEncodingCacheMaxEntries)
}

func TestCachedDataURIMatchesDataURI(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name     string
		mimeType string
		data     []byte
	}{
		{"empty mime", "", cacheTestData(1)},
		{"whitespace mime", "   ", cacheTestData(2)},
		{"explicit mime", "image/png", cacheTestData(3)},
		{"empty data", "text/plain", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			want := DataURI(tt.mimeType, tt.data)
			if got := CachedDataURI(tt.mimeType, tt.data); got != want {
				t.Fatalf("CachedDataURI = %q, want DataURI's %q", got, want)
			}
			// The second call is the cached path; it must return the same URI,
			// not merely an equal-looking one produced from a stale entry.
			if got := CachedDataURI(tt.mimeType, tt.data); got != want {
				t.Fatalf("second CachedDataURI = %q, want %q", got, want)
			}
		})
	}
}

func TestCachedBase64MatchesStdlib(t *testing.T) {
	t.Parallel()
	for _, data := range [][]byte{nil, []byte("hi"), cacheTestData(4)} {
		want := base64.StdEncoding.EncodeToString(data)
		if got := CachedBase64(data); got != want {
			t.Fatalf("CachedBase64 = %q, want %q", got, want)
		}
		if got := CachedBase64(data); got != want {
			t.Fatalf("second CachedBase64 = %q, want %q", got, want)
		}
	}
}

// TestWireEncodingCacheHitReusesWithoutReencoding proves a hit returns the
// retained string rather than rebuilding it: the cached encoding must be
// byte-identical to the encode's while the encode itself is distinguishable.
// The cache's own hit counter is the honest witness here, because a correct
// rebuild produces identical output.
func TestWireEncodingCacheHitReusesWithoutReencoding(t *testing.T) {
	t.Parallel()
	cache := newTestWireEncodingCache()
	data := cacheTestData(11)
	want := DataURI("image/png", data)

	if got := cache.encoding(newDataURIKey("image/png", len(data)), data, func() string { return DataURI("image/png", data) }); got != want {
		t.Fatalf("first encoding = %q, want %q", got, want)
	}
	if cache.hits != 0 || cache.misses != 1 {
		t.Fatalf("after first call hits=%d misses=%d, want 0 and 1", cache.hits, cache.misses)
	}
	if got := cache.encoding(newDataURIKey("image/png", len(data)), data, func() string { return DataURI("image/png", data) }); got != want {
		t.Fatalf("second encoding = %q, want %q", got, want)
	}
	if cache.hits != 1 || cache.misses != 1 {
		t.Fatalf("after second call hits=%d misses=%d, want 1 and 1", cache.hits, cache.misses)
	}
}

// TestWireEncodingCacheHitSurvivesCallerBufferReuse pins that a retained entry
// holds its own copy of the bytes: a caller that rewrites its buffer in place
// must not turn a later lookup into a hit on the wrong image.
func TestWireEncodingCacheHitSurvivesCallerBufferReuse(t *testing.T) {
	t.Parallel()
	cache := newTestWireEncodingCache()
	buffer := cacheTestData(12)
	encode := func(b []byte) string { return DataURI("image/png", b) }
	want := encode(buffer)
	cache.encoding(newDataURIKey("image/png", len(buffer)), buffer, func() string { return encode(buffer) })

	// Same slice, different contents: a re-encoded attachment reusing a scratch
	// buffer looks exactly like this.
	for i := range buffer {
		buffer[i] ^= 0xFF
	}
	if got, wantChanged := cache.encoding(newDataURIKey("image/png", len(buffer)), buffer, func() string { return encode(buffer) }), encode(buffer); got != wantChanged {
		t.Fatalf("rewritten buffer returned %q, want %q", got, wantChanged)
	}
	if cache.misses != 2 {
		t.Fatalf("misses=%d after a caller rewrote its buffer, want 2", cache.misses)
	}
	// The original bytes still read their own entry, from the retained copy.
	original := cacheTestData(12)
	if got := cache.encoding(newDataURIKey("image/png", len(original)), original, func() string { return encode(original) }); got != want {
		t.Fatalf("original bytes = %q, want %q", got, want)
	}
}

// TestWireEncodingCacheMissesOnChangedMediaType pins the wire media type as part
// of the key: the same bytes under a different media type describe a different
// URI, and serving the retained one would put the wrong type on the wire.
func TestWireEncodingCacheMissesOnChangedMediaType(t *testing.T) {
	t.Parallel()
	cache := newTestWireEncodingCache()
	data := cacheTestData(21)
	encode := func(mimeType string) string { return DataURI(mimeType, data) }

	pngURI := cache.encoding(newDataURIKey("image/png", len(data)), data, func() string { return encode("image/png") })
	jpegURI := cache.encoding(newDataURIKey("image/jpeg", len(data)), data, func() string { return encode("image/jpeg") })

	if pngURI == jpegURI {
		t.Fatalf("image/png and image/jpeg produced the same URI %q", pngURI)
	}
	if want := encode("image/jpeg"); jpegURI != want {
		t.Fatalf("changed media type returned %q, want %q", jpegURI, want)
	}
	if cache.misses != 2 {
		t.Fatalf("misses=%d after two media types for one payload, want 2", cache.misses)
	}
	// Each media type keeps its own entry, so the first is still served.
	if got := cache.encoding(newDataURIKey("image/png", len(data)), data, func() string { return encode("image/png") }); got != pngURI {
		t.Fatalf("image/png re-read = %q, want its earlier %q", got, pngURI)
	}
	if cache.hits != 1 {
		t.Fatalf("hits=%d after re-reading image/png, want 1", cache.hits)
	}
}

// TestWireEncodingCacheSeparatesEncodings pins that a data URI and a bare base64
// payload of the same bytes never share an entry: they are different strings for
// different wire shapes.
func TestWireEncodingCacheSeparatesEncodings(t *testing.T) {
	t.Parallel()
	cache := newTestWireEncodingCache()
	data := cacheTestData(22)

	uri := cache.encoding(newDataURIKey("image/png", len(data)), data, func() string { return DataURI("image/png", data) })
	plain := cache.encoding(newBase64Key(len(data)), data, func() string { return base64.StdEncoding.EncodeToString(data) })

	if uri == plain {
		t.Fatalf("data URI and base64 payload produced the same string %q", uri)
	}
	if want := base64.StdEncoding.EncodeToString(data); plain != want {
		t.Fatalf("base64 encoding = %q, want %q", plain, want)
	}
	// Both entries survive, so a replay of either wire shape reads its own
	// retention rather than the other's.
	if cache.order.Len() != 2 {
		t.Fatalf("two encodings of one payload retained %d entries, want 2", cache.order.Len())
	}
	if got := cache.encoding(newDataURIKey("image/png", len(data)), data, func() string { return DataURI("image/png", data) }); got != uri {
		t.Fatalf("data URI re-read as %q, want %q", got, uri)
	}
	if got := cache.encoding(newBase64Key(len(data)), data, func() string { return base64.StdEncoding.EncodeToString(data) }); got != plain {
		t.Fatalf("base64 re-read as %q, want %q", got, plain)
	}
}

// TestWireEncodingCacheMissesOnReencodedSource pins the source bytes as part of
// the key: a re-encoded attachment under the same media type must not inherit
// the earlier URI, even though it encodes to the same length.
func TestWireEncodingCacheMissesOnReencodedSource(t *testing.T) {
	t.Parallel()
	cache := newTestWireEncodingCache()
	before := cacheTestData(31)
	after := cacheTestData(31)
	after[len(after)-1] ^= 0xFF // one bit differs: a re-encode of the same image

	beforeURI := cache.encoding(newDataURIKey("image/png", len(before)), before, func() string { return DataURI("image/png", before) })
	afterURI := cache.encoding(newDataURIKey("image/png", len(after)), after, func() string { return DataURI("image/png", after) })

	if beforeURI == afterURI {
		t.Fatalf("re-encoded source returned the earlier URI %q", afterURI)
	}
	if want := DataURI("image/png", after); afterURI != want {
		t.Fatalf("re-encoded source returned %q, want %q", afterURI, want)
	}
	if cache.misses != 2 {
		t.Fatalf("misses=%d after a re-encoded source, want 2", cache.misses)
	}
}

// TestWireEncodingCacheMissesOnDroppedIdentityComponent is the mutation witness:
// a key that named only the media type would serve one payload for another, and
// this case is what fails when the key stops distinguishing the bytes.
func TestWireEncodingCacheMissesOnDroppedIdentityComponent(t *testing.T) {
	t.Parallel()
	first, second := cacheTestData(41), cacheTestData(42)
	if bytes.Equal(first, second) {
		t.Fatal("test payloads must differ")
	}
	cache := newTestWireEncodingCache()
	cache.encoding(newDataURIKey("image/png", len(first)), first, func() string { return DataURI("image/png", first) })
	got := cache.encoding(newDataURIKey("image/png", len(second)), second, func() string { return DataURI("image/png", second) })
	if want := DataURI("image/png", second); got != want {
		t.Fatalf("second payload returned %q, want %q", got, want)
	}
}

// TestWireEncodingCacheComparesSameLengthPayloads pins the length-keyed candidate
// list: payloads that share a media type and a length are the case where the key
// alone cannot distinguish them, and each must still read its own URI.
func TestWireEncodingCacheComparesSameLengthPayloads(t *testing.T) {
	t.Parallel()
	cache := newTestWireEncodingCache()
	data := make([][]byte, wireEncodingCacheMaxCandidates+1)
	for i := range data {
		data[i] = cacheTestData(byte(90 + i))
		if len(data[i]) != len(data[0]) {
			t.Fatalf("test payloads must share a length")
		}
	}
	want := make([]string, len(data))
	for i, payload := range data {
		want[i] = cache.encoding(newDataURIKey("image/png", len(payload)), payload, func() string { return DataURI("image/png", payload) })
		if got := DataURI("image/png", payload); want[i] != got {
			t.Fatalf("payload %d encoded as %q, want %q", i, want[i], got)
		}
	}
	for i, payload := range data {
		if got := cache.encoding(newDataURIKey("image/png", len(payload)), payload, func() string { return DataURI("image/png", payload) }); got != want[i] {
			t.Fatalf("payload %d re-read as %q, want %q", i, got, want[i])
		}
	}
	if cache.order.Len() != 1 {
		t.Fatalf("one media type and length retained %d keys, want 1", cache.order.Len())
	}
}

// TestWireEncodingCacheSeparatesLengths pins the length component of the key:
// payloads of different lengths under one media type are separate entries, so a
// lookup never walks every payload of that media type.
func TestWireEncodingCacheSeparatesLengths(t *testing.T) {
	t.Parallel()
	cache := newTestWireEncodingCache()
	long := cacheTestData(96)
	short := long[:len(long)-1]
	for _, payload := range [][]byte{short, long} {
		got := cache.encoding(newDataURIKey("image/png", len(payload)), payload, func() string { return DataURI("image/png", payload) })
		if want := DataURI("image/png", payload); got != want {
			t.Fatalf("payload of %d bytes = %q, want %q", len(payload), got, want)
		}
	}
	if cache.order.Len() != 2 {
		t.Fatalf("two lengths retained %d keys, want 2", cache.order.Len())
	}
}

// TestWireEncodingCacheHitDoesNotReencode pins that a hit returns the retained
// string instead of rebuilding it. A rebuild produces an equal string, so the
// allocation count is the witness: encoding a 1 MiB payload allocates its 1.4 MB
// result, and a hit allocates nothing.
func TestWireEncodingCacheHitDoesNotReencode(t *testing.T) {
	// Not parallel: testing.AllocsPerRun runs with GOMAXPROCS pinned to 1
	// and panics when called from a parallel test.
	payload := bytes.Repeat([]byte{0xAB}, 1<<20)
	cache := newTestWireEncodingCache()
	key := newDataURIKey("image/png", len(payload))
	want := cache.encoding(key, payload, func() string { return DataURI("image/png", payload) })
	if allocs := testing.AllocsPerRun(20, func() {
		if cache.encoding(key, payload, func() string { return DataURI("image/png", payload) }) != want {
			t.Fatal("cached encoding changed")
		}
	}); allocs > 0 {
		t.Fatalf("cache hit allocates %.0f times, want 0", allocs)
	}
}

// TestWireEncodingCacheEvictionIsBounded pins both bounds and proves an evicted
// entry re-encodes to the same string, so eviction costs work but can never
// change an answer.
func TestWireEncodingCacheEvictionIsBounded(t *testing.T) {
	t.Parallel()
	const maxEntries = 4
	sample := cacheTestData(51)
	entryBytes := len(sample) + len(DataURI("image/png", sample))
	cache := newWireEncodingCache(entryBytes*maxEntries/2, maxEntries)

	want := make(map[string]string, maxEntries+2)
	for i := range maxEntries + 2 {
		data := cacheTestData(byte(60 + i))
		encoded := cache.encoding(newDataURIKey("image/png", len(data)), data, func() string { return DataURI("image/png", data) })
		want[string(data)] = encoded
		if cache.order.Len() > maxEntries {
			t.Fatalf("retained %d entries, bound is %d", cache.order.Len(), maxEntries)
		}
		if cache.bytes > cache.budget {
			t.Fatalf("retained %d bytes, budget is %d", cache.bytes, cache.budget)
		}
	}
	for data, encoded := range want {
		payload := []byte(data)
		if got := cache.encoding(newDataURIKey("image/png", len(payload)), payload, func() string { return DataURI("image/png", payload) }); got != encoded {
			t.Fatalf("re-read after eviction = %q, want %q", got, encoded)
		}
	}
	for element := cache.order.Front(); element != nil; element = element.Next() {
		entry := element.Value.(*wireEncodingEntry)
		retained := 0
		for _, candidate := range entry.candidates {
			retained += candidate.size()
		}
		if entry.bytes != retained {
			t.Fatalf("entry %v accounts %d bytes, its candidates hold %d", entry.key, entry.bytes, retained)
		}
	}
}

// TestWireEncodingCacheDoesNotRetainOversizedEntry keeps one payload larger than
// the whole budget from evicting every other entry to hold it.
func TestWireEncodingCacheDoesNotRetainOversizedEntry(t *testing.T) {
	t.Parallel()
	small := cacheTestData(71)
	smallURI := DataURI("image/png", small)
	cache := newWireEncodingCache(len(small)+len(smallURI), wireEncodingCacheMaxEntries)

	cache.encoding(newDataURIKey("image/png", len(small)), small, func() string { return smallURI }) // fills the budget exactly
	oversized := make([]byte, len(small)*4)
	copy(oversized, small)
	got := cache.encoding(newDataURIKey("image/png", len(oversized)), oversized, func() string { return DataURI("image/png", oversized) })
	if want := DataURI("image/png", oversized); got != want {
		t.Fatalf("oversized entry returned %q, want %q", got, want)
	}
	if cache.order.Len() != 1 {
		t.Fatalf("retained %d entries after an oversized payload, want the original 1", cache.order.Len())
	}
	if got := cache.encoding(newDataURIKey("image/png", len(small)), small, func() string { return smallURI }); got != smallURI {
		t.Fatalf("small entry after oversized payload = %q, want its retained %q", got, smallURI)
	}
}

// TestCachedDataURIConcurrentCallers pins that a hit under concurrency is the
// same string every goroutine would get from the encode.
func TestCachedDataURIConcurrentCallers(t *testing.T) {
	t.Parallel()
	data := cacheTestData(81)
	want := DataURI("image/png", data)
	results := make(chan string, 8)
	for range 8 {
		go func() { results <- CachedDataURI("image/png", data) }()
	}
	for range 8 {
		if got := <-results; got != want {
			t.Errorf("concurrent CachedDataURI = %q, want %q", got, want)
		}
	}
}

// BenchmarkCachedDataURIReplayed measures the replayed-attachment path: the
// retained URI is returned without re-encoding it.
func BenchmarkCachedDataURIReplayed(b *testing.B) {
	payload := bytes.Repeat([]byte{0xAB}, 1<<20)
	want := CachedDataURI("image/png", payload)
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	for b.Loop() {
		if got := CachedDataURI("image/png", payload); got != want {
			b.Fatal("cached URI changed")
		}
	}
}

// BenchmarkDataURIReplayed measures the same payload with no cache, so the two
// numbers describe one replayed attachment before and after the change.
func BenchmarkDataURIReplayed(b *testing.B) {
	payload := bytes.Repeat([]byte{0xAB}, 1<<20)
	want := DataURI("image/png", payload)
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	for b.Loop() {
		if got := DataURI("image/png", payload); got != want {
			b.Fatal("encode changed")
		}
	}
}

// BenchmarkCachedBase64Replayed measures the bare base64 payload the Anthropic
// and Google builders put on the wire.
func BenchmarkCachedBase64Replayed(b *testing.B) {
	payload := bytes.Repeat([]byte{0xAB}, 1<<20)
	want := CachedBase64(payload)
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	for b.Loop() {
		if got := CachedBase64(payload); got != want {
			b.Fatal("cached base64 changed")
		}
	}
}
