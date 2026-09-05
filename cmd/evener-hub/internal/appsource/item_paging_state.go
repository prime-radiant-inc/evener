package appsource

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"sync"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
)

const defaultItemSnapshotStateEntries = 32
const itemSnapshotFingerprintTailCapacity = appwire.TranscriptItemPageLimit

type itemSnapshotFingerprint struct {
	Position appwire.ThreadItemPosition
	Digest   [sha256.Size]byte
}

type itemSnapshotState struct {
	ThreadRef        string
	Incarnation      string
	SourceIdentity   string
	NativeCursor     string
	ItemCount        int
	FirstPosition    appwire.ThreadItemPosition
	LastPosition     appwire.ThreadItemPosition
	Prefix           bool
	TranscriptDigest [sha256.Size]byte
	FingerprintCount uint8
	FingerprintTail  [itemSnapshotFingerprintTailCapacity]itemSnapshotFingerprint
}

type itemSnapshotStateEntry struct {
	key   string
	state itemSnapshotState
}

type itemSnapshotStateCache struct {
	mu       sync.Mutex
	entries  map[string]*list.Element
	order    *list.List
	capacity int
}

func newItemSnapshotStateCache(capacity int) *itemSnapshotStateCache {
	if capacity < 0 {
		capacity = 0
	}
	return &itemSnapshotStateCache{
		entries:  make(map[string]*list.Element),
		order:    list.New(),
		capacity: capacity,
	}
}

func (c *itemSnapshotStateCache) get(key string) (itemSnapshotState, bool) {
	if c == nil {
		return itemSnapshotState{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.entries[key]
	if !ok {
		return itemSnapshotState{}, false
	}
	c.order.MoveToFront(element)
	return element.Value.(itemSnapshotStateEntry).state, true
}

// peek leaves recency untouched until the operation commits successfully.
func (c *itemSnapshotStateCache) peek(key string) (itemSnapshotState, bool) {
	if c == nil {
		return itemSnapshotState{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.entries[key]
	if !ok {
		return itemSnapshotState{}, false
	}
	return element.Value.(itemSnapshotStateEntry).state, true
}

func (c *itemSnapshotStateCache) put(key string, state itemSnapshotState) {
	_ = c.putContext(context.Background(), key, state)
}

func (c *itemSnapshotStateCache) putContext(ctx context.Context, key string, state itemSnapshotState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c == nil || c.capacity == 0 {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if existing, ok := c.entries[key]; ok {
		existing.Value = itemSnapshotStateEntry{key: key, state: state}
		c.order.MoveToFront(existing)
		return nil
	}
	element := c.order.PushFront(itemSnapshotStateEntry{key: key, state: state})
	c.entries[key] = element
	for c.order.Len() > c.capacity {
		oldest := c.order.Back()
		entry := oldest.Value.(itemSnapshotStateEntry)
		delete(c.entries, entry.key)
		c.order.Remove(oldest)
	}
	return nil
}

var itemSnapshotChainAnchor = sha256.Sum256([]byte("evener:item-snapshot-chain:v1\x00"))

func transcriptItemFingerprint(candidate appitempaging.TranscriptItemCandidate) itemSnapshotFingerprint {
	hash := sha256.New()
	_, _ = hash.Write([]byte("evener:item-snapshot-fingerprint:v1\x00"))
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], uint64(len(candidate.Item.TranscriptKey)))
	_, _ = hash.Write(encoded[:])
	_, _ = hash.Write([]byte(candidate.Item.TranscriptKey))
	binary.BigEndian.PutUint64(encoded[:], candidate.Position.Entry)
	_, _ = hash.Write(encoded[:])
	var encodedItem [4]byte
	binary.BigEndian.PutUint32(encodedItem[:], candidate.Position.Item)
	_, _ = hash.Write(encodedItem[:])
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	return itemSnapshotFingerprint{Position: candidate.Position, Digest: digest}
}

func extendTranscriptItemDigest(previous [sha256.Size]byte, fingerprint itemSnapshotFingerprint) [sha256.Size]byte {
	hash := sha256.New()
	_, _ = hash.Write([]byte("evener:item-snapshot-chain-step:v1\x00"))
	_, _ = hash.Write(previous[:])
	_, _ = hash.Write(fingerprint.Digest[:])
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	return digest
}

func transcriptItemDigest(candidates []appitempaging.TranscriptItemCandidate, count int) ([sha256.Size]byte, bool) {
	if count < 0 || count > len(candidates) {
		return [sha256.Size]byte{}, false
	}
	digest := itemSnapshotChainAnchor
	for _, candidate := range candidates[:count] {
		digest = extendTranscriptItemDigest(digest, transcriptItemFingerprint(candidate))
	}
	return digest, true
}

// itemSnapshotStateAdvance derives the next summary and checks observed
// continuity. Full materializations recompute the anchored prefix; bounded
// windows extend the digest only through an exact tail-to-head overlap.
// Disjoint windows return a fresh bounded summary; the caller must authenticate
// and retain their native cursor before preserving transcript identity.
func itemSnapshotStateAdvance(previous itemSnapshotState, candidates []appitempaging.TranscriptItemCandidate, prefix bool) (itemSnapshotState, bool) {
	current := itemSnapshotStateForCandidates(previous.ThreadRef, previous.Incarnation, previous.SourceIdentity, candidates, prefix)
	if prefix && !previous.Prefix {
		if previous.FingerprintCount == 0 || len(candidates) < int(previous.FingerprintCount) {
			return current, false
		}
		currentTail := candidates[len(candidates)-int(previous.FingerprintCount):]
		for index, candidate := range currentTail {
			if transcriptItemFingerprint(candidate) != previous.FingerprintTail[index] {
				return current, false
			}
		}
		return current, true
	}
	if previous.ItemCount == 0 {
		return current, prefix || len(candidates) == 0
	}
	if prefix {
		start := 0
		end := start + previous.ItemCount
		if end > len(candidates) || candidates[end-1].Position != previous.LastPosition {
			return current, false
		}
		digest, ok := transcriptItemDigest(candidates[start:end], previous.ItemCount)
		return current, ok && digest == previous.TranscriptDigest
	}
	if len(candidates) == 0 || previous.FingerprintCount == 0 {
		return current, false
	}

	first := candidates[0].Position
	if first.Entry > previous.LastPosition.Entry ||
		(first.Entry == previous.LastPosition.Entry && first.Item > previous.LastPosition.Item) {
		// A disjoint native window can retain identity through its native token,
		// but cannot extend an anchored digest across unobserved positions.
		return current, true
	}
	fingerprints := make([]itemSnapshotFingerprint, len(candidates))
	for index, candidate := range candidates {
		fingerprints[index] = transcriptItemFingerprint(candidate)
	}
	previousTail := previous.FingerprintTail[:int(previous.FingerprintCount)]
	maxOverlap := min(len(previousTail), len(fingerprints))
	overlap := 0
	matches := 0
	for count := 1; count <= maxOverlap; count++ {
		if equalItemSnapshotFingerprints(previousTail[len(previousTail)-count:], fingerprints[:count]) {
			overlap = count
			matches++
		}
	}
	if matches != 1 {
		return current, false
	}
	if overlap == len(fingerprints) {
		return previous, true
	}
	last := fingerprints[len(fingerprints)-1].Position
	if last.Entry < previous.LastPosition.Entry || (last.Entry == previous.LastPosition.Entry && last.Item <= previous.LastPosition.Item) {
		return current, false
	}

	advanced := previous
	advanced.ItemCount += len(fingerprints) - overlap
	advanced.LastPosition = last
	for _, fingerprint := range fingerprints[overlap:] {
		advanced.TranscriptDigest = extendTranscriptItemDigest(advanced.TranscriptDigest, fingerprint)
	}
	combined := make([]itemSnapshotFingerprint, 0, len(previousTail)+len(fingerprints)-overlap)
	combined = append(combined, previousTail...)
	combined = append(combined, fingerprints[overlap:]...)
	if len(combined) > itemSnapshotFingerprintTailCapacity {
		combined = combined[len(combined)-itemSnapshotFingerprintTailCapacity:]
	}
	advanced.FingerprintCount = uint8(len(combined))
	clear(advanced.FingerprintTail[:])
	copy(advanced.FingerprintTail[:], combined)
	return advanced, true
}

func equalItemSnapshotFingerprints(left, right []itemSnapshotFingerprint) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func itemSnapshotStateForCandidates(
	threadRef, incarnation, sourceIdentity string,
	candidates []appitempaging.TranscriptItemCandidate,
	prefix bool,
) itemSnapshotState {
	digest, _ := transcriptItemDigest(candidates, len(candidates))
	state := itemSnapshotState{
		ThreadRef:        threadRef,
		Incarnation:      incarnation,
		SourceIdentity:   sourceIdentity,
		ItemCount:        len(candidates),
		Prefix:           prefix,
		TranscriptDigest: digest,
	}
	if len(candidates) > 0 {
		state.FirstPosition = candidates[0].Position
		state.LastPosition = candidates[len(candidates)-1].Position
	}
	start := max(0, len(candidates)-itemSnapshotFingerprintTailCapacity)
	state.FingerprintCount = uint8(len(candidates) - start)
	for index, candidate := range candidates[start:] {
		state.FingerprintTail[index] = transcriptItemFingerprint(candidate)
	}
	return state
}

func itemSnapshotStateMatchesCompleteCandidates(previous itemSnapshotState, candidates []appitempaging.TranscriptItemCandidate) bool {
	if !previous.Prefix || previous.ItemCount != len(candidates) {
		return false
	}
	digest, ok := transcriptItemDigest(candidates, len(candidates))
	if !ok || digest != previous.TranscriptDigest {
		return false
	}
	if len(candidates) > 0 && (candidates[0].Position != previous.FirstPosition || candidates[len(candidates)-1].Position != previous.LastPosition) {
		return false
	}
	fingerprintCount := int(previous.FingerprintCount)
	if fingerprintCount > len(candidates) {
		return false
	}
	fingerprintStart := len(candidates) - fingerprintCount
	for index, candidate := range candidates[fingerprintStart:] {
		if transcriptItemFingerprint(candidate) != previous.FingerprintTail[index] {
			return false
		}
	}
	return true
}

type keyedMutexEntry struct {
	mu   sync.Mutex
	refs int
}

type keyedMutexRegistry struct {
	mu      sync.Mutex
	entries map[string]*keyedMutexEntry
}

func (r *keyedMutexRegistry) lock(key string) func() {
	r.mu.Lock()
	if r.entries == nil {
		r.entries = make(map[string]*keyedMutexEntry)
	}
	entry := r.entries[key]
	if entry == nil {
		entry = &keyedMutexEntry{}
		r.entries[key] = entry
	}
	entry.refs++
	r.mu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		r.mu.Lock()
		entry.refs--
		if entry.refs == 0 && r.entries[key] == entry {
			delete(r.entries, key)
		}
		r.mu.Unlock()
	}
}
