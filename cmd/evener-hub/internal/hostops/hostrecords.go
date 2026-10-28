package hostops

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"time"
	"unicode/utf8"
)

// This file owns the machine-record family the hub keeps for its host set: the
// per-host identity records, the generation high-water marks, the mutation
// receipts, the removal tombstones, the pruned-receipt markers, and the staged,
// remnant and attempt records the commit and teardown protocols write. The hub
// decides what a record means; the store decides how it lasts. The family
// lives in the same file as the operation records, so one atomic store write
// carries any subset of the eight maps together with the boundary mirror the
// operation records are validated against, and a corrupt store file is
// quarantined with its family in custody.
//
// Each value is the record's JSON object, held verbatim: the store bounds,
// canonicalizes and persists it, and the hub's codec owns its schema.

const (
	// MaxHostRecordKeyBytes bounds a key in any family map. The longest key is a
	// scoped receipt key: five parts of at most 128 bytes each, percent-encoded.
	MaxHostRecordKeyBytes = 2048
	// MaxHostRecordBytes bounds one record. A removal tombstone carrying its
	// retained thread rows is the largest record the hub writes.
	MaxHostRecordBytes = 4 << 20
	// MaxHostRecordsBytes bounds the family as a whole, so no write can push the
	// store file past what its next load will read.
	MaxHostRecordsBytes = 64 << 20
)

// ErrInvalidHostRecords reports a family outside the schema every writer of
// this store emits: an empty, oversized or non-UTF-8 key, a value that is not a
// bounded JSON object, or a family over its total bound.
var ErrInvalidHostRecords = errors.New("hostops: invalid host records")

// ErrHostRecordsNotImported refuses a family commit before the import that
// makes the store the records' home.
var ErrHostRecordsNotImported = errors.New("hostops: host records have not been imported")

// ErrHostRecordsImported refuses a second import: the store already holds the
// family.
var ErrHostRecordsImported = errors.New("hostops: host records were already imported")

// HostRecords is the family's eight maps. A nil map and an empty one mean the
// same thing; every write emits all eight as objects.
type HostRecords struct {
	// Hosts holds each live host's identity record, keyed by host name.
	Hosts map[string]json.RawMessage `json:"hosts"`
	// Generations holds each name's generation high-water mark, keyed by host
	// name. It outlives the live entry.
	Generations map[string]json.RawMessage `json:"generations"`
	// Receipts holds the finalized mutation receipts, keyed by scoped receipt key.
	Receipts map[string]json.RawMessage `json:"receipts"`
	// Tombstones holds the removed-host records, keyed by host name.
	Tombstones map[string]json.RawMessage `json:"tombstones"`
	// PrunedReceipts holds the compacted-receipt markers, keyed by scoped
	// receipt key.
	PrunedReceipts map[string]json.RawMessage `json:"prunedReceipts"`
	// StagedReceipts holds the in-flight commit markers, keyed by host name.
	StagedReceipts map[string]json.RawMessage `json:"stagedReceipts"`
	// Remnants holds the open and resolved teardown remnants, keyed by remnant id.
	Remnants map[string]json.RawMessage `json:"remnants"`
	// Attempts holds the teardown attempts, keyed by attempt id.
	Attempts map[string]json.RawMessage `json:"attempts"`
}

// hostRecordsState is the family as the store file holds it: the eight maps and
// the instant the import made the store their home. Its presence is the import
// marker.
type hostRecordsState struct {
	ImportedAt time.Time `json:"importedAt"`
	HostRecords
}

// hostRecordsFile is the decode shape of hostRecordsState. Every field is a
// pointer so a file that omits one — or carries null — is refused instead of
// read as an empty map, which the next write would turn into lost records.
type hostRecordsFile struct {
	ImportedAt     *time.Time                  `json:"importedAt"`
	Hosts          *map[string]json.RawMessage `json:"hosts"`
	Generations    *map[string]json.RawMessage `json:"generations"`
	Receipts       *map[string]json.RawMessage `json:"receipts"`
	Tombstones     *map[string]json.RawMessage `json:"tombstones"`
	PrunedReceipts *map[string]json.RawMessage `json:"prunedReceipts"`
	StagedReceipts *map[string]json.RawMessage `json:"stagedReceipts"`
	Remnants       *map[string]json.RawMessage `json:"remnants"`
	Attempts       *map[string]json.RawMessage `json:"attempts"`
}

// state maps the decode shape to a validated state, refusing every omitted
// field.
func (f hostRecordsFile) state() (*hostRecordsState, error) {
	if f.ImportedAt == nil {
		return nil, fmt.Errorf("%w: the family carries no import instant", ErrInvalidHostRecords)
	}
	fields := []struct {
		name  string
		value *map[string]json.RawMessage
	}{
		{"hosts", f.Hosts}, {"generations", f.Generations}, {"receipts", f.Receipts},
		{"tombstones", f.Tombstones}, {"prunedReceipts", f.PrunedReceipts},
		{"stagedReceipts", f.StagedReceipts}, {"remnants", f.Remnants}, {"attempts", f.Attempts},
	}
	for _, field := range fields {
		if field.value == nil {
			return nil, fmt.Errorf("%w: the family carries no %s map", ErrInvalidHostRecords, field.name)
		}
	}
	state := &hostRecordsState{ImportedAt: f.ImportedAt.UTC()}
	state.Hosts, state.Generations, state.Receipts = *f.Hosts, *f.Generations, *f.Receipts
	state.Tombstones, state.PrunedReceipts = *f.Tombstones, *f.PrunedReceipts
	state.StagedReceipts, state.Remnants, state.Attempts = *f.StagedReceipts, *f.Remnants, *f.Attempts
	if err := state.validate(); err != nil {
		return nil, err
	}
	return state, nil
}

// decodeHostRecordsRegion reads one family region — the object a store file or
// a custody file holds — with the strictness the store load applies: no unknown
// key, every field present, every bound held.
func decodeHostRecordsRegion(raw json.RawMessage) (*hostRecordsState, error) {
	var file hostRecordsFile
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return nil, fmt.Errorf("%w: decode the family: %w", ErrInvalidHostRecords, err)
	}
	return file.state()
}

// maps lists the family's maps with the key bound each is held to: the maps
// keyed by host name hold names to the host-name bound.
func (r *HostRecords) maps() []struct {
	name    string
	records *map[string]json.RawMessage
	keyMax  int
} {
	return []struct {
		name    string
		records *map[string]json.RawMessage
		keyMax  int
	}{
		{"hosts", &r.Hosts, MaxHostNameBytes},
		{"generations", &r.Generations, MaxHostNameBytes},
		{"receipts", &r.Receipts, MaxHostRecordKeyBytes},
		{"tombstones", &r.Tombstones, MaxHostNameBytes},
		{"prunedReceipts", &r.PrunedReceipts, MaxHostRecordKeyBytes},
		{"stagedReceipts", &r.StagedReceipts, MaxHostNameBytes},
		{"remnants", &r.Remnants, MaxHostRecordKeyBytes},
		{"attempts", &r.Attempts, MaxHostRecordKeyBytes},
	}
}

// validate checks every key and value against the family's bounds: keys are
// non-empty valid UTF-8 within their map's bound, values are JSON objects within
// the record bound, and the family as a whole fits its bound.
func (r HostRecords) validate() error {
	total := 0
	for _, family := range r.maps() {
		for key, value := range *family.records {
			if key == "" {
				return fmt.Errorf("%w: %s carries an empty key", ErrInvalidHostRecords, family.name)
			}
			if len(key) > family.keyMax {
				return fmt.Errorf("%w: %s carries a %d-byte key, over the %d-byte bound",
					ErrInvalidHostRecords, family.name, len(key), family.keyMax)
			}
			if !utf8.ValidString(key) {
				return fmt.Errorf("%w: %s carries a key that is not valid UTF-8", ErrInvalidHostRecords, family.name)
			}
			if len(value) > MaxHostRecordBytes {
				return fmt.Errorf("%w: %s record %q is %d bytes, over the %d-byte bound",
					ErrInvalidHostRecords, family.name, key, len(value), MaxHostRecordBytes)
			}
			if !json.Valid(value) || len(bytes.TrimSpace(value)) == 0 || bytes.TrimSpace(value)[0] != '{' {
				return fmt.Errorf("%w: %s record %q is not a JSON object", ErrInvalidHostRecords, family.name, key)
			}
			total += len(key) + len(value)
		}
	}
	if total > MaxHostRecordsBytes {
		return fmt.Errorf("%w: the family is %d bytes, over the %d-byte bound", ErrInvalidHostRecords, total, MaxHostRecordsBytes)
	}
	return nil
}

// canonical returns the family with every value in the form the store file
// writes it, so a value read back compares equal to the value that was
// written. Maps are never nil.
func (r HostRecords) canonical() (HostRecords, error) {
	out := HostRecords{}
	dest := out.maps()
	for i, family := range r.maps() {
		clean := make(map[string]json.RawMessage, len(*family.records))
		for key, value := range *family.records {
			encoded, err := json.Marshal(value)
			if err != nil {
				return HostRecords{}, fmt.Errorf("%w: %s record %q: %w", ErrInvalidHostRecords, family.name, key, err)
			}
			clean[key] = encoded
		}
		*dest[i].records = clean
	}
	return out, nil
}

// clone copies the family deeply.
func (r HostRecords) clone() HostRecords {
	out := HostRecords{}
	dest := out.maps()
	for i, family := range r.maps() {
		clean := make(map[string]json.RawMessage, len(*family.records))
		for key, value := range *family.records {
			clean[key] = bytes.Clone(value)
		}
		*dest[i].records = clean
	}
	return out
}

// equal reports whether two canonical families hold the same records.
func (r HostRecords) equal(other HostRecords) bool {
	theirs := other.maps()
	for i, family := range r.maps() {
		if !maps.EqualFunc(*family.records, *theirs[i].records, func(a, b json.RawMessage) bool { return bytes.Equal(a, b) }) {
			return false
		}
	}
	return true
}

func cloneHostRecordsState(state *hostRecordsState) *hostRecordsState {
	if state == nil {
		return nil
	}
	return &hostRecordsState{ImportedAt: state.ImportedAt, HostRecords: state.clone()}
}

// HostRecords returns a copy of the family and the instant it was imported.
// imported is false for a store whose records still live elsewhere: nothing has
// been imported, and the family is empty.
func (s *Store) HostRecords() (records HostRecords, importedAt time.Time, imported bool) {
	if s == nil {
		return HostRecords{}, time.Time{}, false
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()
	state := s.cell.state.HostRecords
	if state == nil {
		return HostRecords{}.clone(), time.Time{}, false
	}
	return state.clone(), state.ImportedAt, true
}

// ImportHostRecords makes the store the home of the host records: it persists
// the family and the import marker in one atomic store write together with the
// boundary mirror. A store that already holds the family refuses with
// ErrHostRecordsImported and changes nothing, so a retried import after a
// crash cannot overwrite records the store has moved on from.
func (s *Store) ImportHostRecords(records HostRecords, mirror HostMirror) error {
	return s.writeHostRecords(records, mirror, false)
}

// CommitHostRecords replaces the family and applies the boundary mirror in one
// atomic store write. A fault before the rename lands neither; the store's
// in-memory state follows the file. It refuses with ErrHostRecordsNotImported
// until the import has made the store the records' home. A commit that changes
// nothing writes nothing.
func (s *Store) CommitHostRecords(records HostRecords, mirror HostMirror) error {
	return s.writeHostRecords(records, mirror, true)
}

func (s *Store) writeHostRecords(records HostRecords, mirror HostMirror, imported bool) error {
	if s == nil {
		return errors.New("hostops: store is not configured")
	}
	if err := records.validate(); err != nil {
		return err
	}
	if err := validateMirror(mirror); err != nil {
		return err
	}
	canonical, err := records.canonical()
	if err != nil {
		return err
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()
	current := s.cell.state.HostRecords
	switch {
	case imported && current == nil:
		return ErrHostRecordsNotImported
	case !imported && current != nil:
		return ErrHostRecordsImported
	}
	next := cloneSnapshot(s.cell.state)
	changed := s.applyMirrorLocked(&next, mirror)
	if current == nil || !current.equal(canonical) {
		importedAt := s.now()
		if current != nil {
			importedAt = current.ImportedAt
		}
		next.HostRecords = &hostRecordsState{ImportedAt: importedAt.UTC(), HostRecords: canonical}
		changed = true
	}
	if !changed {
		return nil
	}
	_, err = s.commitLocked(next)
	return err
}
