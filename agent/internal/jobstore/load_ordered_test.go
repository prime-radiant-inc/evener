package jobstore

// loadOrderedForTest is the append-order whole-history read the live activity
// path used before the incremental fold. No production caller remains, so the
// tests that compare against it keep this wrapper over the store's existing
// whole-history read.
func loadOrderedForTest(s *Store) ([]*JobRecord, error) {
	events, err := s.LoadEvents()
	if err != nil {
		return nil, err
	}
	return FoldOrdered(events), nil
}
