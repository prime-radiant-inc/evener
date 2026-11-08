package hub

import (
	"container/list"
	"encoding/json"
	"fmt"
	"sync"

	"primeradiant.com/evener/appwire"
)

const transcriptCanonicalStoreMaxBytes = 256 << 20

type transcriptCanonicalStoreEntry struct {
	key  string
	data []byte
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
}

func newTranscriptCanonicalStore(maxBytes int) *transcriptCanonicalStore {
	return &transcriptCanonicalStore{maxBytes: maxBytes, entries: make(map[string]*list.Element)}
}

func (s *transcriptCanonicalStore) putItem(key string, item appwire.ThreadItem) error {
	data, err := json.Marshal(item)
	if err != nil {
		return err
	}
	if len(data) > s.maxBytes {
		return fmt.Errorf("canonical transcript item is %d bytes, store limit is %d", len(data), s.maxBytes)
	}
	s.put(key, data)
	return nil
}

func (s *transcriptCanonicalStore) put(key string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if current := s.entries[key]; current != nil {
		entry := current.Value.(*transcriptCanonicalStoreEntry)
		s.bytes -= len(entry.data)
		entry.data = append(entry.data[:0], data...)
		s.bytes += len(entry.data)
		s.order.MoveToFront(current)
	} else {
		entry := &transcriptCanonicalStoreEntry{key: key, data: append([]byte(nil), data...)}
		s.entries[key] = s.order.PushFront(entry)
		s.bytes += len(entry.data)
	}
	for s.bytes > s.maxBytes {
		oldest := s.order.Back()
		entry := oldest.Value.(*transcriptCanonicalStoreEntry)
		delete(s.entries, entry.key)
		s.bytes -= len(entry.data)
		s.order.Remove(oldest)
	}
}

func (s *transcriptCanonicalStore) get(key string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	element := s.entries[key]
	if element == nil {
		return nil, false
	}
	s.order.MoveToFront(element)
	entry := element.Value.(*transcriptCanonicalStoreEntry)
	return append([]byte(nil), entry.data...), true
}
