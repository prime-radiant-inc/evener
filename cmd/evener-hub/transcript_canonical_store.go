package hub

import (
	"container/list"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"sync"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
)

const transcriptCanonicalStoreMaxBytes = 256 << 20

type transcriptCanonicalStoreEntry struct {
	key    string
	triple string
	data   []byte
}

// transcriptCanonicalStore retains the exact item encoding that the window
// packer measured and labeled with a fetch cursor. Entries are immutable and
// bounded by a least-recently-used byte budget.
type transcriptCanonicalStore struct {
	mu       sync.Mutex
	maxBytes int
	bytes    int
	entries  map[string]*list.Element
	order    list.List
	// live maps an item's identity triple to the token of the entry retained
	// for it. An eviction removes the mapping, so the next retention of the same
	// triple receives a new token and an older cursor cannot be revived.
	live    map[string]string
	triples map[string]string
	prefix  string
	next    uint64
}

func newTranscriptCanonicalStore(maxBytes int) *transcriptCanonicalStore {
	return &transcriptCanonicalStore{
		maxBytes: maxBytes,
		entries:  make(map[string]*list.Element),
		live:     make(map[string]string),
		triples:  make(map[string]string),
		prefix:   fmt.Sprintf("%016x", rand.Uint64()),
	}
}

// tokenFor returns the token of the entry currently retained for an item
// identity triple, assigning a never-reused token when none is live.
func (s *transcriptCanonicalStore) tokenFor(triple string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if token := s.live[triple]; token != "" {
		return token
	}
	s.next++
	token := fmt.Sprintf("%s-%x", s.prefix, s.next)
	s.live[triple] = token
	s.triples[token] = triple
	return token
}

func (s *transcriptCanonicalStore) putItem(key string, item appwire.ThreadItem) error {
	if _, ok := s.borrow(key); ok {
		return nil
	}
	data, err := json.Marshal(item)
	if err != nil {
		return err
	}
	if len(data) > s.maxBytes {
		return fmt.Errorf("canonical transcript item is %d bytes, store limit is %d", len(data), s.maxBytes)
	}
	s.mu.Lock()
	triple := s.triples[key]
	s.mu.Unlock()
	s.putOwnedTriple(key, triple, data)
	return nil
}

func (s *transcriptCanonicalStore) put(key string, data []byte) {
	s.putOwned(key, append([]byte(nil), data...))
}

// putOwned retains data without copying it. The caller must not mutate data
// after transferring ownership to the store.
func (s *transcriptCanonicalStore) putOwned(key string, data []byte) {
	s.putOwnedTriple(key, "", data)
}

// putOwnedTriple retains data without copying it under key, remembering the
// item identity triple so an eviction can retire the token mapping.
func (s *transcriptCanonicalStore) putOwnedTriple(key, triple string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if current := s.entries[key]; current != nil {
		s.order.MoveToFront(current)
	} else {
		entry := &transcriptCanonicalStoreEntry{key: key, triple: triple, data: data}
		s.entries[key] = s.order.PushFront(entry)
		s.bytes += len(entry.data)
	}
	for s.bytes > s.maxBytes {
		oldest := s.order.Back()
		entry := oldest.Value.(*transcriptCanonicalStoreEntry)
		delete(s.entries, entry.key)
		if entry.triple != "" && s.live[entry.triple] == entry.key {
			delete(s.live, entry.triple)
		}
		delete(s.triples, entry.key)
		s.bytes -= len(entry.data)
		s.order.Remove(oldest)
	}
}

func (s *transcriptCanonicalStore) get(key string) ([]byte, bool) {
	data, ok := s.borrow(key)
	if !ok {
		return nil, false
	}
	return append([]byte(nil), data...), true
}

// borrow returns immutable store-owned bytes. Callers may retain the slice but
// must not mutate it; replacement and eviction never reuse its backing array.
func (s *transcriptCanonicalStore) borrow(key string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	element := s.entries[key]
	if element == nil {
		return nil, false
	}
	s.order.MoveToFront(element)
	entry := element.Value.(*transcriptCanonicalStoreEntry)
	return entry.data, true
}

// fetchEntryTriple names one item identity inside a view for the retained entry
// table, so repeated reads of the same item reuse a live token.
func fetchEntryTriple(identity appitempaging.CursorIdentity, candidate appitempaging.TranscriptItemCandidate) string {
	return fmt.Sprintf("%s\x00%s\x00%d\x00%d\x00%d", identity.ThreadRef, identity.Incarnation,
		candidate.Position.Entry, candidate.Position.Item, candidate.Item.Version)
}
