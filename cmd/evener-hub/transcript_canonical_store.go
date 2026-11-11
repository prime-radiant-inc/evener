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
	// remoteTokens maps a controller-retained entry to the owning hub's token
	// for the same item, so a fetch can forward that token without letting it
	// double as the controller's cache key.
	remoteTokens map[string]string
	prefix       string
	next         uint64
}

func newTranscriptCanonicalStore(maxBytes int) *transcriptCanonicalStore {
	return &transcriptCanonicalStore{
		maxBytes:     maxBytes,
		entries:      make(map[string]*list.Element),
		live:         make(map[string]string),
		triples:      make(map[string]string),
		remoteTokens: make(map[string]string),
		prefix:       fmt.Sprintf("%016x", rand.Uint64()),
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

// retainedToken returns the token of the entry currently live for an item
// identity triple without assigning one. Canonicalization uses it so a
// candidate that is filtered or truncated away registers no entry.
func (s *transcriptCanonicalStore) retainedToken(triple string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	token := s.live[triple]
	return token, token != ""
}

// retainRemoteToken records the owning hub's token for a controller-retained
// entry. The mapping lives exactly as long as that entry.
func (s *transcriptCanonicalStore) retainRemoteToken(key, remoteToken string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.remoteTokens[key] = remoteToken
}

// remoteToken returns the owning hub's token recorded for a controller-retained
// entry.
func (s *transcriptCanonicalStore) remoteToken(key string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	token, ok := s.remoteTokens[key]
	return token, ok
}

// putItem retains the canonical encoding of the item a token was minted for.
// The token must still be the live assignment for the item's identity triple:
// an eviction between the mint and this call retires the token, and the bytes
// are dropped rather than repopulating it. A cursor naming a retired token then
// fails typed stale, as an eviction promises, instead of resuming a different
// encoding.
func (s *transcriptCanonicalStore) putItem(token, triple string, item appwire.ThreadItem) error {
	data, err := json.Marshal(item)
	if err != nil {
		return err
	}
	if len(data) > s.maxBytes {
		return fmt.Errorf("canonical transcript item is %d bytes, store limit is %d", len(data), s.maxBytes)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.live[triple] != token {
		return nil
	}
	s.putLocked(token, triple, data)
	return nil
}

// retainOrReuseItem atomically chooses the canonical encoding retained for an
// item identity and returns that encoding. Two concurrent first reads can
// canonicalize the same item version with different file-backed descriptors;
// the first to arrive retains its bytes and every other caller reuses them, so
// the preview a response packs and the fetch cursor minted beside it always
// describe one canonical item. A token assigned but not yet retained (a mint
// without bytes) receives the first caller's bytes under that same token.
func (s *transcriptCanonicalStore) retainOrReuseItem(triple string, item appwire.ThreadItem) (appwire.ThreadItem, error) {
	data, err := json.Marshal(item)
	if err != nil {
		return appwire.ThreadItem{}, err
	}
	if len(data) > s.maxBytes {
		return appwire.ThreadItem{}, fmt.Errorf("canonical transcript item is %d bytes, store limit is %d", len(data), s.maxBytes)
	}
	s.mu.Lock()
	token := s.live[triple]
	if token != "" {
		if element := s.entries[token]; element != nil {
			s.order.MoveToFront(element)
			live := element.Value.(*transcriptCanonicalStoreEntry).data
			s.mu.Unlock()
			var retained appwire.ThreadItem
			if err := json.Unmarshal(live, &retained); err != nil {
				return appwire.ThreadItem{}, err
			}
			return retained, nil
		}
	}
	if token == "" {
		s.next++
		token = fmt.Sprintf("%s-%x", s.prefix, s.next)
		s.live[triple] = token
		s.triples[token] = triple
	}
	s.putLocked(token, triple, data)
	s.mu.Unlock()
	return item, nil
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
	s.putLocked(key, triple, data)
}

// putLocked retains data under key and evicts least-recently-used entries until
// the byte budget holds. The caller holds the lock.
func (s *transcriptCanonicalStore) putLocked(key, triple string, data []byte) {
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
		delete(s.remoteTokens, entry.key)
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
