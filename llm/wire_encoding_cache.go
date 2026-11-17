package llm

import (
	"bytes"
	"container/list"
	"encoding/base64"
	"strings"
	"sync"
)

// A replayed image content part carries no identity of its own: ImageData holds
// the bytes, a media type, and nothing that names the attachment. Every request
// that replays a session's history therefore rebuilds each image's wire payload
// from scratch, and that cost grows with the number of images in history rather
// than with the response.
//
// wireEncodingCache memoizes those payloads. A key names the encoding kind, the
// media type where the encoding depends on it, and the payload length;
// candidates are then confirmed by comparing the bytes themselves against a
// private copy retained with the encoded string. The comparison is what makes a
// hit correct, so the key only has to find candidates, never to establish
// identity: a changed media type reads a different key, and a re-encoded source
// of the same length fails the comparison. Both miss.
//
// No digest is computed. Hashing the payload on the request path would cost
// about a quarter of the encode it is meant to save, and nothing upstream
// retains one, so the cheap discriminator is the length plus the comparison.

// wireEncodingCacheBudgetBytes bounds the memory the cache retains, counting
// each encoded string and the private copy of the bytes it was built from. A
// session replays the media in its live history on every request, so the working
// set is that media; 32 MiB holds several multi-MB attachments and cannot grow
// with session length.
const wireEncodingCacheBudgetBytes = 32 << 20

// wireEncodingCacheMaxEntries bounds the number of distinct keys independently
// of the byte budget: a stream of small payloads would otherwise keep thousands
// of list and map nodes alive for negligible payload bytes.
const wireEncodingCacheMaxEntries = 64

// wireEncodingCacheMaxCandidates bounds how many distinct payloads one key
// retains. Payloads that share a media type and a length are otherwise compared
// one after another, so the bound keeps the worst-case comparison work small;
// past it the candidate that length has retained longest gives way.
const wireEncodingCacheMaxCandidates = 4

// wireEncodingKind distinguishes encodings that are not interchangeable under
// one key: a data URI embeds its media type while a bare base64 payload does
// not.
type wireEncodingKind uint8

const (
	wireEncodingDataURI wireEncodingKind = iota + 1
	wireEncodingBase64
)

// wireEncodingKey names a payload's encoding, media type where relevant, and
// length. It deliberately does not claim to identify the bytes —
// wireEncodingCandidate.data does that.
type wireEncodingKey struct {
	kind      wireEncodingKind
	mediaType string
	length    int
}

// newDataURIKey and newBase64Key build the two keys callers ask for, so a
// caller never has to know that a base64 payload carries no media type.
func newDataURIKey(mimeType string, length int) wireEncodingKey {
	return wireEncodingKey{kind: wireEncodingDataURI, mediaType: canonicalDataURIMediaType(mimeType), length: length}
}

func newBase64Key(length int) wireEncodingKey {
	return wireEncodingKey{kind: wireEncodingBase64, length: length}
}

// wireEncodingCandidate is one retained encoding together with the bytes it was
// built from. The bytes are a private copy: retaining the caller's slice instead
// would let a caller that rewrites its buffer in place turn a stale encoding
// into a false hit.
type wireEncodingCandidate struct {
	data     []byte
	encoding string
}

func (c wireEncodingCandidate) size() int { return len(c.data) + len(c.encoding) }

// wireEncodingEntry is every candidate one key retains, and its LRU element.
type wireEncodingEntry struct {
	key        wireEncodingKey
	candidates []wireEncodingCandidate
	bytes      int
}

// wireEncodingCache is a bounded LRU of encoded wire payloads. Eviction costs a
// re-encode and never a wrong answer.
type wireEncodingCache struct {
	mu         sync.Mutex
	budget     int
	maxEntries int
	bytes      int
	order      *list.List // front is the most recently used entry
	entries    map[wireEncodingKey]*list.Element
	hits       int
	misses     int
}

func newWireEncodingCache(budget, maxEntries int) *wireEncodingCache {
	return &wireEncodingCache{
		budget:     budget,
		maxEntries: maxEntries,
		order:      list.New(),
		entries:    make(map[wireEncodingKey]*list.Element),
	}
}

// encoding returns what encode produces for data under key, reusing an earlier
// call's result when those exact bytes are retained.
func (c *wireEncodingCache) encoding(key wireEncodingKey, data []byte, encode func() string) string {
	// An empty payload's encoding is a constant; retaining it would only evict
	// real payloads.
	if len(data) == 0 {
		return encode()
	}

	c.mu.Lock()
	if element, ok := c.entries[key]; ok {
		entry := element.Value.(*wireEncodingEntry)
		for _, candidate := range entry.candidates {
			if bytes.Equal(candidate.data, data) {
				c.order.MoveToFront(element)
				c.hits++
				c.mu.Unlock()
				return candidate.encoding
			}
		}
	}
	c.misses++
	c.mu.Unlock()

	// Encode outside the lock: it is the expensive part, and a concurrent caller
	// missing the same payload duplicates this work rather than blocking every
	// other lookup behind it.
	encoded := encode()
	c.store(key, data, encoded)
	return encoded
}

// store retains encoded, and a private copy of the data it was built from, under
// key. It evicts least-recently-used entries until both bounds hold. A payload
// larger than the whole budget is not retained: caching it would evict every
// other entry to hold one image.
func (c *wireEncodingCache) store(key wireEncodingKey, data []byte, encoded string) {
	// Check the bounds before copying: a payload that cannot be retained must not
	// cost a retained-sized copy on its way to being dropped.
	if c.budget <= 0 || c.maxEntries <= 0 || len(data)+len(encoded) > c.budget {
		return
	}
	candidate := wireEncodingCandidate{data: append([]byte(nil), data...), encoding: encoded}
	c.mu.Lock()
	defer c.mu.Unlock()
	if element, ok := c.entries[key]; ok {
		entry := element.Value.(*wireEncodingEntry)
		for _, existing := range entry.candidates {
			if bytes.Equal(existing.data, candidate.data) {
				return // a concurrent caller retained the same payload first
			}
		}
		if len(entry.candidates) >= wireEncodingCacheMaxCandidates {
			oldest := entry.candidates[0]
			// Zero the slot before reslicing so the evicted candidate holds no
			// reference to a payload the budget no longer accounts for. Slices do not
			// expose the slot afterwards, but the array is what the collector sees.
			entry.candidates[0] = wireEncodingCandidate{}
			entry.candidates = entry.candidates[1:]
			entry.bytes -= oldest.size()
			c.bytes -= oldest.size()
		}
		entry.candidates = append(entry.candidates, candidate)
		entry.bytes += candidate.size()
		c.bytes += candidate.size()
		c.order.MoveToFront(element)
	} else {
		entry := &wireEncodingEntry{key: key, candidates: []wireEncodingCandidate{candidate}, bytes: candidate.size()}
		c.entries[key] = c.order.PushFront(entry)
		c.bytes += candidate.size()
	}
	for c.bytes > c.budget || c.order.Len() > c.maxEntries {
		oldest := c.order.Back()
		if oldest == nil {
			return
		}
		entry := oldest.Value.(*wireEncodingEntry)
		c.order.Remove(oldest)
		delete(c.entries, entry.key)
		c.bytes -= entry.bytes
	}
}

// canonicalDataURIMediaType is DataURI's media type rule: surrounding whitespace
// is dropped and a blank type becomes application/octet-stream. Both the encode
// and the cache key use this one function, so a key always names the URI DataURI
// would build rather than the caller's spelling of it.
func canonicalDataURIMediaType(mimeType string) string {
	mimeType = strings.TrimSpace(mimeType)
	if mimeType == "" {
		return "application/octet-stream"
	}
	return mimeType
}

// wireEncodings retains the encoded payloads replaying history would otherwise
// rebuild on every request.
var wireEncodings = newWireEncodingCache(wireEncodingCacheBudgetBytes, wireEncodingCacheMaxEntries)

// CachedDataURI returns exactly what DataURI returns for mimeType and data,
// re-encoding only when no earlier encode of those exact bytes under that same
// wire media type is retained. A caller that replays the same attachment should
// use this rather than DataURI: the encode is proportional to the payload and is
// otherwise paid again on every request.
//
// The result is the string DataURI builds for the arguments it is given, so a
// caller that changes the bytes or the media type never reads an earlier
// attachment's URI.
func CachedDataURI(mimeType string, data []byte) string {
	return wireEncodings.encoding(newDataURIKey(mimeType, len(data)), data, func() string {
		return DataURI(mimeType, data)
	})
}

// CachedBase64 returns exactly what base64.StdEncoding.EncodeToString returns
// for data, reusing an earlier encoding of those exact bytes when one is
// retained. It is the cached form of the bare payload the Anthropic and Google
// builders put on the wire, which is rebuilt on every replay just as a data URI
// is.
func CachedBase64(data []byte) string {
	return wireEncodings.encoding(newBase64Key(len(data)), data, func() string {
		return base64.StdEncoding.EncodeToString(data)
	})
}
