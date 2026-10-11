package appoverlay

// recentSet is a map that remembers only its most recently added keys: past
// its capacity, adding a key forgets the oldest. The overlay keeps its
// per-round and per-call memory in these so a long-lived thread's overlay
// does not grow with its history.
type recentSet[V any] struct {
	capacity int
	values   map[string]recentValue[V]
	// order is the keys in the order they were added, oldest first. An
	// entry whose key was deleted, or deleted and added again later, no
	// longer matches its key's seq and is skipped.
	order   []recentKey
	nextSeq uint64
}

type recentValue[V any] struct {
	value V
	seq   uint64
}

type recentKey struct {
	key string
	seq uint64
}

func newRecentSet[V any](capacity int) recentSet[V] {
	return recentSet[V]{capacity: capacity, values: map[string]recentValue[V]{}}
}

func (s *recentSet[V]) get(key string) (V, bool) {
	v, ok := s.values[key]
	return v.value, ok
}

// put sets key's value. A new key is the newest; a key already present keeps
// its place.
func (s *recentSet[V]) put(key string, v V) {
	if current, ok := s.values[key]; ok {
		s.values[key] = recentValue[V]{value: v, seq: current.seq}
		return
	}
	s.values[key] = recentValue[V]{value: v, seq: s.nextSeq}
	s.order = append(s.order, recentKey{key: key, seq: s.nextSeq})
	s.nextSeq++
	for len(s.values) > s.capacity {
		oldest := s.order[0]
		s.order = s.order[1:]
		if s.live(oldest) {
			delete(s.values, oldest.key)
		}
	}
	if len(s.order) > 2*s.capacity {
		s.order = s.liveOrder()
	}
}

func (s *recentSet[V]) delete(key string) {
	delete(s.values, key)
}

func (s *recentSet[V]) len() int {
	return len(s.values)
}

// deleteFunc deletes every entry drop reports true for.
func (s *recentSet[V]) deleteFunc(drop func(key string, v V) bool) {
	for key, v := range s.values {
		if drop(key, v.value) {
			delete(s.values, key)
		}
	}
}

func (s *recentSet[V]) clear() {
	clear(s.values)
	s.order = nil
}

func (s *recentSet[V]) live(k recentKey) bool {
	v, ok := s.values[k.key]
	return ok && v.seq == k.seq
}

// liveOrder is order without its skipped entries, in a fresh slice so the
// dropped prefix's storage is freed.
func (s *recentSet[V]) liveOrder() []recentKey {
	kept := make([]recentKey, 0, len(s.values))
	for _, k := range s.order {
		if s.live(k) {
			kept = append(kept, k)
		}
	}
	return kept
}
