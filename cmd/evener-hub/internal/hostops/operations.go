package hostops

// Operations reads (deploy pipeline 08b §4, §8, §10).
//
// This file owns the read half of the operation store: `evener/host/operations`
// answers from here. The read is a pure read — no gate, no dial, no write — and
// every record it returns comes from the store's own snapshot under the store
// mutex. Records sort by the controller-assigned id ascending; `createdAt` and
// `updatedAt` stay display-only and never decide the order (§8 "Ordering").
//
// A call answers one bounded listing: at most `limit` records (default 50,
// capped at 200), returned directly. There is no continuation token, so no
// compaction or boundary move can invalidate a later page: each read is its own
// fresh window over the store's current state.
//
// A host-pinned read narrows the window to one host's effective
// (generation, incarnationId) pair (§10: "generation ... omitted: the current
// generation") and echoes that pair on the response. An unfiltered cross-host
// read lists each host's current pair. History stays reachable through the
// host-pinned pair filter and the `id`/`operationId` detail filters; a detail
// filter resolves a compacted record out of its dedup tombstone with
// `compacted: true`, because the record itself no longer sits in the record set.

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"unicode/utf8"
)

const (
	// DefaultOperationsLimit is §8's default page size when the request names
	// no limit.
	DefaultOperationsLimit = 50
	// MaxOperationsLimit is §8's cap on a page: responses never exceed it.
	MaxOperationsLimit = 200
)

// ErrInvalidOperationsQuery reports an operations read whose caller-supplied
// inputs fall outside §10's params.
var ErrInvalidOperationsQuery = errors.New("hostops: invalid operations query")

// OperationsQuery is one `evener/host/operations` read (§10's params, mapped
// onto the store): the host filter, the query filters and the page size.
type OperationsQuery struct {
	// Host pins the read to one host. Empty is the unfiltered cross-host read;
	// a generation filter requires it (§8: "A generation-pinned page requires
	// `name`").
	Host string
	// ClientOperationID matches the client-supplied clientOperationId, never
	// the controller-assigned id.
	ClientOperationID string
	// State, when set, selects records in exactly that durable state.
	State State
	// Generation, when set, selects the incarnation after client operation-ID
	// reuse; IncarnationID narrows it to the exact incarnation.
	Generation    *uint64
	IncarnationID string
	// ID is the detail filter for the controller-assigned record id.
	ID string
	// Limit is the page size: 0 reads the default, values above the cap are
	// capped, and a negative limit is refused.
	Limit int
}

// OperationsPage is one bounded listing of records plus the identity the
// response must carry: the effective (generation, incarnationId) pair on
// host-pinned pages.
type OperationsPage struct {
	// Records are ascending by controller-assigned id.
	Records []Record
	// Generation and IncarnationID are set exactly on host-pinned pages: the
	// effective pair actually listed (§8, §10).
	Generation    *uint64
	IncarnationID string
}

// ReadOperations is the store's `evener/host/operations` read (§8, §10): a pure
// read of the records in ascending id order under the store mutex, one bounded
// listing per call.
func (s *Store) ReadOperations(q OperationsQuery) (OperationsPage, error) {
	if s == nil {
		return OperationsPage{}, errors.New("hostops: store is not configured")
	}
	if err := validateOperationsQuery(q); err != nil {
		return OperationsPage{}, err
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()
	return readOperationsLocked(&s.cell.state, q)
}

// validateOperationsQuery checks the caller-supplied half of a read against
// §10's params: a closed state set, a negative limit, an incarnation filter
// without its generation, and a pair filter without the host name it pins.
func validateOperationsQuery(q OperationsQuery) error {
	if q.Limit < 0 {
		return fmt.Errorf("%w: limit %d is negative", ErrInvalidOperationsQuery, q.Limit)
	}
	if q.State != "" && !q.State.Valid() {
		return fmt.Errorf("%w: state %q is outside the record state set", ErrInvalidOperationsQuery, q.State)
	}
	if q.Generation != nil && *q.Generation == 0 {
		return fmt.Errorf("%w: generation 0 names no incarnation", ErrInvalidOperationsQuery)
	}
	if q.IncarnationID != "" && q.Generation == nil {
		return fmt.Errorf("%w: incarnationId needs generation", ErrInvalidOperationsQuery)
	}
	if q.Host == "" && (q.Generation != nil || q.IncarnationID != "") {
		return fmt.Errorf("%w: a generation filter needs a host name", ErrInvalidOperationsQuery)
	}
	if q.Host != "" && !utf8.ValidString(q.Host) {
		return fmt.Errorf("%w: host name is not valid UTF-8", ErrInvalidOperationsQuery)
	}
	return nil
}

// readOperationsLocked is ReadOperations' body; the caller holds the store
// mutex across the whole read, so the listing describes one record set.
func readOperationsLocked(state *snapshot, q OperationsQuery) (OperationsPage, error) {
	limit := q.Limit
	if limit == 0 {
		limit = DefaultOperationsLimit
	}
	if limit > MaxOperationsLimit {
		limit = MaxOperationsLimit
	}

	page := OperationsPage{}
	// newestByHost grounds a mirror-less host's pair; one pass serves both the
	// known-host set and every per-host pair lookup below.
	newestByHost := newestRecordByHostLocked(state)
	var (
		pinned         = q.Host != ""
		pageHosts      = map[string]bool{}
		windowPairs    = map[string]OperationPair{}
		pinnedPair     OperationPair
		havePinnedPair bool
	)
	// The `id` detail filter addresses one record directly (§10's "detail
	// filter ... resolves through it directly"): it is not a window, so the
	// pair window stands down for it. A named pair is an explicit window and
	// keeps its filter.
	detailOnly := q.ID != "" && q.Generation == nil

	if pinned {
		pageHosts[q.Host] = true
		if !detailOnly {
			// A detail lookup resolves no window pair: the row it returns is
			// the pair that page lists under, and the response echoes that row.
			resolved, ok, err := resolvePinnedPair(state, newestByHost, q)
			if err != nil {
				return OperationsPage{}, err
			}
			pinnedPair, havePinnedPair = resolved, ok
			if havePinnedPair {
				windowPairs[q.Host] = pinnedPair
			}
		}
	} else {
		// Every host the store knows — a mirrored boundary name, or a
		// record-holding name whose boundary was never mirrored (the custody
		// import) — contributes its current pair's records. A boundary-only
		// name contributes nothing because it holds no records.
		for host := range state.Boundaries {
			pageHosts[host] = true
		}
		for host := range newestByHost {
			pageHosts[host] = true
		}
		if !detailOnly {
			for host := range pageHosts {
				if pair, ok := currentHostPairLocked(state, newestByHost, host); ok {
					windowPairs[host] = pair
				}
			}
		}
	}

	// The listing itself: stored order is ascending id order (store.go's
	// Records contract, §8's "one ordering for both"), so a linear scan of that
	// order is the whole read. The fixed allocator width makes the id-string
	// comparison the numeric one (formatAllocatorID); createdAt never
	// participates. The filters are applied exactly as the request names them.
	records := make([]Record, 0, limit)
	for i := range state.Records {
		record := state.Records[i]
		if !pageHosts[record.Host] {
			continue
		}
		if !matchesOperationsQuery(record, q, windowPairs, detailOnly) {
			continue
		}
		records = append(records, cloneRecord(record))
		if len(records) == limit {
			break
		}
	}
	page.Records = records

	// §4's replay is retrievable: a detail lookup — the `id` filter for the
	// controller-assigned id, or `operationId` for the client operation id —
	// resolves a compacted record out of its dedup tombstone with
	// `compacted: true`, because the record itself no longer sits in the
	// record set and without this the contract has no retrieval path. Listings
	// stay record-only: a tombstone is a replay source, not a listed row.
	if q.ID != "" || q.ClientOperationID != "" {
		for i := range state.Tombstones {
			tombstone := state.Tombstones[i]
			if q.ID != "" {
				// A direct id lookup addresses the row itself: a retained
				// tombstone is retrievable even when its host no longer appears
				// in the current host set — a removed host's boundary drops
				// with its last record while its tombstones stay — so the
				// unfiltered and host-pinned spellings of the same lookup agree.
				if q.Host != "" && tombstone.Host != q.Host {
					continue
				}
			} else if !pageHosts[tombstone.Host] {
				// The operationId filter stays a listing filter over the
				// window: retrieval of a compacted record is by its
				// controller-assigned id, which deploy/restart return.
				continue
			}
			record := tombstone.record()
			if !matchesOperationsQuery(record, q, windowPairs, detailOnly) {
				continue
			}
			records = append(records, record)
		}
		slices.SortStableFunc(records, func(a, b Record) int { return cmp.Compare(a.ID, b.ID) })
		if len(records) > limit {
			records = records[:limit]
		}
		page.Records = records
	}

	if pinned {
		// §8: the host-pinned response carries "the effective generation and
		// incarnationId actually listed". The window pair is that; a detail
		// lookup that named no pair echoes the row it listed instead.
		generation, incarnation := uint64(0), ""
		switch {
		case havePinnedPair:
			generation, incarnation = pinnedPair.Generation, pinnedPair.IncarnationID
		case detailOnly && len(records) == 1:
			generation, incarnation = records[0].Generation, records[0].IncarnationID
		}
		page.Generation = &generation
		page.IncarnationID = incarnation
	}
	return page, nil
}

// matchesOperationsQuery reports whether a stored identity satisfies the
// query's non-host filters and the pair window the read lists under. The
// caller owns the host-membership rule, which differs between a listed record
// (its host must be in the page) and a tombstone reached by the `id` detail
// lookup (a removed host's tombstone stays retrievable).
func matchesOperationsQuery(record Record, q OperationsQuery, windowPairs map[string]OperationPair, detailOnly bool) bool {
	if q.ClientOperationID != "" && record.ClientOperationID != q.ClientOperationID {
		return false
	}
	if q.State != "" && record.State != q.State {
		return false
	}
	if q.ID != "" && record.ID != q.ID {
		return false
	}
	pair, ok := windowPairs[record.Host]
	if !ok {
		// A host with no pinned pair has no records to list, except a detail
		// lookup, which is not a window.
		return detailOnly
	}
	return record.Generation == pair.Generation && record.IncarnationID == pair.IncarnationID
}

// resolvePinnedPair answers the effective pair a host-pinned read lists under
// (§8: "a host-pinned response carries the effective generation and
// incarnationId actually listed"; §10: "generation selects the incarnation
// after client operation-ID reuse (omitted: the current generation)").
//
// The named pair is taken exactly. A named generation resolves to the current
// incarnation when that generation is the host's current one; a generation the
// host has moved past is a superseded selection, and §10 requires the
// incarnationId alongside it ("incarnationId narrows that selection to the
// exact incarnation — required alongside generation whenever the caller names a
// superseded pair") — a generation-only superseded read would silently hide
// every other incarnation of that generation, so it is refused as an invalid
// query. An omitted pair is the host's current pair, the mirrored boundary. ok
// is false only when the host has no pair at all, which is also a host with no
// records to list.
func resolvePinnedPair(state *snapshot, newest map[string]Record, q OperationsQuery) (OperationPair, bool, error) {
	boundary, hasBoundary := state.Boundaries[q.Host]
	switch {
	case q.Generation != nil && q.IncarnationID != "":
		return OperationPair{Generation: *q.Generation, IncarnationID: q.IncarnationID}, true, nil
	case q.Generation != nil:
		if hasBoundary && boundary.Generation == *q.Generation {
			return OperationPair{Generation: *q.Generation, IncarnationID: boundary.IncarnationID}, true, nil
		}
		if !hasBoundary {
			// A mirror-less host — §4's custody import — is pinned by its own
			// newest record, and a generation-only read naming that pair resolves
			// like any other current pair instead of refusing.
			if pair, ok := currentHostPairLocked(state, newest, q.Host); ok && pair.Generation == *q.Generation {
				return pair, true, nil
			}
		}
		return OperationPair{}, false, fmt.Errorf(
			"%w: host %q has moved past generation %d, so the read needs its incarnationId",
			ErrInvalidOperationsQuery, q.Host, *q.Generation)
	case hasBoundary:
		return OperationPair{Generation: boundary.Generation, IncarnationID: boundary.IncarnationID}, true, nil
	}
	// An omitted pair is the host's current one; a mirror-less host's is its own
	// newest record's, so a host-pinned read of a closed name is never a
	// silently empty page.
	pair, ok := currentHostPairLocked(state, newest, q.Host)
	return pair, ok, nil
}

// newestRecordByHostLocked returns, per host, the record whose identity is the
// host's current one: the highest generation, ties broken by the newest record
// id. It is the ground truth a mirror-less host's pair is minted from (§4's
// custody import), and mirrors do not participate.
func newestRecordByHostLocked(state *snapshot) map[string]Record {
	newest := make(map[string]Record, len(state.Records))
	for i := range state.Records {
		record := state.Records[i]
		current, held := newest[record.Host]
		if !held || record.Generation > current.Generation ||
			(record.Generation == current.Generation && record.ID > current.ID) {
			newest[record.Host] = record
		}
	}
	return newest
}

// currentHostPairLocked returns the pair a host is pinned by: its mirrored
// triple when one exists, and otherwise the pair its own newest record carries —
// the state §4's custody import produces, which a host-and-generation read must
// resolve like any other current pair.
func currentHostPairLocked(state *snapshot, newest map[string]Record, host string) (OperationPair, bool) {
	if boundary, ok := state.Boundaries[host]; ok {
		return OperationPair{Generation: boundary.Generation, IncarnationID: boundary.IncarnationID}, true
	}
	record, ok := newest[host]
	if !ok {
		return OperationPair{}, false
	}
	return OperationPair{Generation: record.Generation, IncarnationID: record.IncarnationID}, true
}
