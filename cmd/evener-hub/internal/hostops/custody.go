package hostops

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"time"

	"github.com/spf13/afero"
)

// This file owns spec §4's quarantine-custody snapshot: the schema the custody
// file is written with, the all-or-nothing completeness check that decides
// whether a corrupt store file may be quarantined at all, and the import set the
// replacement store opens with. That check is the slice's safety core: custody
// is written only when the corrupt file parses whole and its record set is
// accounted for with no gap or residue, so no fence the file carried can be
// lost. Any shortfall — an unparseable record, a per-host boundary record that
// fails its schema, ownership missing for a fenced name, an id below the file's
// own allocator high-water mark with no retained evidence — refuses startup
// rather than serving past a fence nobody can prove.
//
// The retired BoundaryEntry vocabulary and the later slices that once owned the
// verifier, the kill/wait, and `orphan-resolve` were withdrawn with the
// crash-fencing program (comp08). The imported records this file produces are
// the retained seam: every fence entry becomes an `orphan-unverified` record
// under its original id, and every ownership-only entry becomes one of the
// carried kind under its stable `quarantineRecordId`, so the `operations` detail
// filter can address every closed name by record id (no resolver runs).

// ErrQuarantineIncomplete reports a corrupt store file whose custody snapshot
// cannot be shown complete. Spec §4: "When the corrupt file cannot yield a
// complete custody snapshot, boot fails startup rather than serving hosts past
// an unprovable fence." It always accompanies ErrStoreCorrupt, so a caller that
// classifies store failures sees one class either way.
var ErrQuarantineIncomplete = errors.New("hostops: quarantine custody is incomplete")

// custodyFile is §4's custody schema, pinned field-for-field:
//
//	{quarantineEpoch, quarantinedFile, custodiedAt, recordIds[],
//	 allocatorHighWaterMark, fences[], ownership[]}
type custodyFile struct {
	// QuarantineEpoch is the durable epoch this quarantine advances the store
	// to, persisted outside the quarantined file.
	QuarantineEpoch uint64 `json:"quarantineEpoch"`
	// QuarantinedFile is the corrupt file's path — the file renamed aside, never
	// deleted.
	QuarantinedFile string `json:"quarantinedFile"`
	// CustodiedAt is the boot timestamp the quarantine landed at (RFC3339).
	CustodiedAt time.Time `json:"custodiedAt"`
	// RecordIDs carries every imported record's controller-assigned id verbatim
	// plus its host, so the replacement store imports by id.
	RecordIDs []custodyRecordID `json:"recordIds"`
	// AllocatorHighWaterMark is the pre-quarantine maximum: the replacement
	// allocator starts above it, so no fresh operation reuses an imported id.
	AllocatorHighWaterMark uint64 `json:"allocatorHighWaterMark"`
	// Fences carries one entry per open `orphan-unverified` record, with the
	// record's persisted boundary verbatim.
	Fences []custodyFence `json:"fences"`
	// Ownership carries one entry per name the corrupt file yielded.
	Ownership []custodyOwnership `json:"ownership"`
}

// custodyRecordID is one imported record's identity for the replacement import.
type custodyRecordID struct {
	RecordID string `json:"recordId"`
	Host     string `json:"host"`
}

// custodyFence is one open fence's full record identity plus its persisted
// boundary, so a fence import builds its record from the entry itself.
type custodyFence struct {
	RecordID          string `json:"recordId"`
	Host              string `json:"host"`
	Kind              Kind   `json:"kind"`
	ClientOperationID string `json:"clientOperationId"`
	Generation        uint64 `json:"generation"`
	IncarnationID     string `json:"incarnationId"`
	// Quarantine and Boundary are retired (comp08 2b): prior custody files carry
	// the per-host fencing-quarantine flag and the persisted boundary payload.
	// They stay decodable so a prior file loads, and are never written
	// (omitempty) — the import keeps the record's identity alone.
	Quarantine bool            `json:"quarantine,omitempty"`
	Boundary   json.RawMessage `json:"boundary,omitempty"`
}

// custodyOwnership is one name's ownership: the identity a replacement
// `OperationRecord` requires plus the name's generation high-water mark.
type custodyOwnership struct {
	QuarantineRecordID string `json:"quarantineRecordId"`
	Host               string `json:"host"`
	Kind               Kind   `json:"kind"`
	ClientOperationID  string `json:"clientOperationId"`
	Generation         uint64 `json:"generation"`
	HighWaterMark      uint64 `json:"highWaterMark"`
	IncarnationID      string `json:"incarnationId"`
}

// custodyImports returns the replacement store's record set: each fence entry
// imported as an `orphan-unverified` record under its original id, and each
// ownership-only entry imported as the same closed-name record above the
// pre-quarantine high-water mark. The result is sorted ascending by id, which is
// the store file's own order, and every record is validated before it is
// returned.
func custodyImports(custody custodyFile) ([]Record, error) {
	fenced := map[string]bool{}
	for _, fence := range custody.Fences {
		fenced[fence.Host] = true
	}
	at := custody.CustodiedAt.UTC()
	records := make([]Record, 0, len(custody.Fences)+len(custody.Ownership))
	for _, fence := range custody.Fences {
		records = append(records, Record{
			ID:                fence.RecordID,
			ClientOperationID: fence.ClientOperationID,
			Host:              fence.Host,
			Kind:              fence.Kind,
			State:             StateOrphanUnverified,
			Generation:        fence.Generation,
			IncarnationID:     fence.IncarnationID,
			CreatedAt:         at,
			UpdatedAt:         at,
		})
	}
	for _, ownership := range custody.Ownership {
		if fenced[ownership.Host] {
			// The name is closed by its fence import; the ownership entry is the
			// name's identity record, not a second import.
			continue
		}
		records = append(records, Record{
			ID:                ownership.QuarantineRecordID,
			ClientOperationID: ownership.ClientOperationID,
			Host:              ownership.Host,
			Kind:              ownership.Kind,
			State:             StateOrphanUnverified,
			Generation:        ownership.Generation,
			IncarnationID:     ownership.IncarnationID,
			CreatedAt:         at,
			UpdatedAt:         at,
		})
	}
	sort.Slice(records, func(i, j int) bool { return records[i].ID < records[j].ID })
	for _, record := range records {
		if err := validateRecord(record); err != nil {
			return nil, err
		}
	}
	return records, nil
}

// quarantineClientOperationID is the server-minted `quarantine-<name>` client
// operation id for a name the corrupt file yielded none for. It is bounded to
// MaxClientOperationIDBytes: a name long enough to overflow keeps a
// deterministic UTF-8-safe prefix plus a digest of the full name, so two long
// names never collide on one id and the id always satisfies the record schema
// the import must pass. (Truncating at a byte boundary could split a rune and
// produce an id the store refuses, turning a complete custody into an aborted
// boot.)
func quarantineClientOperationID(host string) string {
	const prefix = "quarantine-"
	if id := prefix + host; len(id) <= MaxClientOperationIDBytes {
		return id
	}
	sum := sha256.Sum256([]byte(host))
	suffix := "-" + hex.EncodeToString(sum[:8])
	room := MaxClientOperationIDBytes - len(prefix) - len(suffix)
	cut := 0
	for i := range host {
		if i > room {
			break
		}
		cut = i
	}
	return prefix + host[:cut] + suffix
}

// assembleCustody completes and validates one custody snapshot: the cross-entry
// rules §4's schema rests on. Every fence names an ownership entry (a fence with
// no ownership cannot be imported as a record identity), every quarantine
// record id is unique in the file, every imported record builds and validates,
// and the id split is exact — fence imports at or below the pre-quarantine
// high-water mark, ownership-only imports above it.
func assembleCustody(custody custodyFile) (custodyFile, error) {
	if custody.QuarantineEpoch == 0 {
		return custodyFile{}, fmt.Errorf("%w: custody carries no quarantine epoch", ErrQuarantineIncomplete)
	}
	if custody.QuarantinedFile == "" {
		return custodyFile{}, fmt.Errorf("%w: custody names no quarantined file", ErrQuarantineIncomplete)
	}
	if custody.CustodiedAt.IsZero() {
		return custodyFile{}, fmt.Errorf("%w: custody carries no timestamp", ErrQuarantineIncomplete)
	}
	ownership := make(map[string]custodyOwnership, len(custody.Ownership))
	ids := make(map[string]string, len(custody.Ownership)+len(custody.Fences))
	for _, entry := range custody.Ownership {
		if entry.QuarantineRecordID == "" {
			return custodyFile{}, fmt.Errorf("%w: ownership for %q carries no quarantine record id", ErrQuarantineIncomplete, entry.Host)
		}
		if _, duplicate := ids[entry.QuarantineRecordID]; duplicate {
			return custodyFile{}, fmt.Errorf("%w: quarantine record id %q is not unique in the custody file",
				ErrQuarantineIncomplete, entry.QuarantineRecordID)
		}
		ids[entry.QuarantineRecordID] = entry.Host
		if _, duplicate := ownership[entry.Host]; duplicate {
			return custodyFile{}, fmt.Errorf("%w: host %q carries more than one ownership entry", ErrQuarantineIncomplete, entry.Host)
		}
		if entry.Generation == 0 || entry.HighWaterMark < entry.Generation {
			return custodyFile{}, fmt.Errorf("%w: ownership for %q carries generation %d with high-water mark %d",
				ErrQuarantineIncomplete, entry.Host, entry.Generation, entry.HighWaterMark)
		}
		ownership[entry.Host] = entry
	}
	for _, entry := range custody.RecordIDs {
		if entry.RecordID == "" || entry.Host == "" {
			return custodyFile{}, fmt.Errorf("%w: recordIds carries an empty row", ErrQuarantineIncomplete)
		}
	}
	fenceIDs := make(map[string]bool, len(custody.Fences))
	for _, fence := range custody.Fences {
		if !fence.Kind.Valid() {
			return custodyFile{}, fmt.Errorf("%w: fence %q carries kind %q", ErrQuarantineIncomplete, fence.RecordID, fence.Kind)
		}
		if fence.RecordID == "" {
			return custodyFile{}, fmt.Errorf("%w: fence for %q carries no record id", ErrQuarantineIncomplete, fence.Host)
		}
		if fenceIDs[fence.RecordID] {
			return custodyFile{}, fmt.Errorf("%w: fence record id %q is not unique", ErrQuarantineIncomplete, fence.RecordID)
		}
		fenceIDs[fence.RecordID] = true
		if _, ok := ownership[fence.Host]; !ok {
			// Spec §4: "ownership missing for a fenced name" is incomplete.
			return custodyFile{}, fmt.Errorf("%w: fence %q names host %q with no ownership entry",
				ErrQuarantineIncomplete, fence.RecordID, fence.Host)
		}
	}
	// Every entry must be resolvable: the import set is built and validated here
	// so a custody file the operator reads is one the replacement store can hold.
	records, err := custodyImports(custody)
	if err != nil {
		return custodyFile{}, fmt.Errorf("%w: %w", ErrQuarantineIncomplete, err)
	}
	// recordIds and the import set are one-to-one: every imported record appears
	// exactly once as a row (same id and host), and every row names an import.
	// Existence plus length equality is not enough — two rows for one import
	// would leave another import unnamed while the counts agree.
	imported := make(map[string]string, len(records))
	for _, record := range records {
		if _, duplicate := imported[record.ID]; duplicate {
			return custodyFile{}, fmt.Errorf("%w: the import set names record id %q twice", ErrQuarantineIncomplete, record.ID)
		}
		imported[record.ID] = record.Host
	}
	rows := make(map[string]string, len(custody.RecordIDs))
	for _, row := range custody.RecordIDs {
		if _, duplicate := rows[row.RecordID]; duplicate {
			return custodyFile{}, fmt.Errorf("%w: recordIds names %q twice", ErrQuarantineIncomplete, row.RecordID)
		}
		rows[row.RecordID] = row.Host
		if host, ok := imported[row.RecordID]; !ok || host != row.Host {
			return custodyFile{}, fmt.Errorf("%w: recordIds row %q/%q is not an imported record",
				ErrQuarantineIncomplete, row.RecordID, row.Host)
		}
	}
	for _, record := range records {
		if _, ok := rows[record.ID]; !ok {
			return custodyFile{}, fmt.Errorf("%w: imported record %q/%q is missing from recordIds",
				ErrQuarantineIncomplete, record.ID, record.Host)
		}
	}
	// The id split: fence imports keep their original ids, which the corrupt file
	// allocated at or below its high-water mark; ownership-only imports were
	// allocated above it so no fresh operation can reuse a fence's id.
	for _, record := range records {
		allocated, err := parseAllocatorID(record.ID)
		if err != nil {
			return custodyFile{}, fmt.Errorf("%w: imported record id %q is not a controller-assigned id", ErrQuarantineIncomplete, record.ID)
		}
		if fenceIDs[record.ID] && allocated > custody.AllocatorHighWaterMark {
			return custodyFile{}, fmt.Errorf("%w: fence import %q sits above the pre-quarantine high-water mark %d",
				ErrQuarantineIncomplete, record.ID, custody.AllocatorHighWaterMark)
		}
		if !fenceIDs[record.ID] && allocated <= custody.AllocatorHighWaterMark {
			return custodyFile{}, fmt.Errorf("%w: ownership import %q does not sit above the pre-quarantine high-water mark %d",
				ErrQuarantineIncomplete, record.ID, custody.AllocatorHighWaterMark)
		}
	}
	return custody, nil
}

// custodyDocument is the decode shape the custody snapshot is read through: the
// families a fence, an ownership pair or the record-set accounting can stand on
// are typed and strictly decoded, and the families the replacement store
// replaces wholesale — tokens, probe epochs, the guard epoch, the wall clock,
// the compaction ledger, and §9's `pendingCompensation` set (whose preimage
// rows the token family shares) — are consumed as raw regions only. §4 starts
// the replacement with zero outstanding tokens and no history, so a row that
// fails its own schema in one of those families is not a lost fence; refusing
// startup over it would brick the hosts for bit-rot of data the quarantine
// discards by design. Every key the writer emits is declared here, so an unknown
// top-level key is still refused: a file carrying one is not a file this store
// wrote.
type custodyDocument struct {
	Version                *uint64                    `json:"version"`
	Sequence               *uint64                    `json:"sequence"`
	AllocatorHighWaterMark *uint64                    `json:"allocatorHighWaterMark"`
	Records                *[]recordFile              `json:"records"`
	CompactSeq             uint64                     `json:"compactSeq"`
	Tombstones             *[]tombstoneFile           `json:"tombstones"`
	CompactionMarks        *[]CompactionMark          `json:"compactionMarks"`
	CompactionFloor        uint64                     `json:"compactionFloor"`
	Boundaries             map[string]json.RawMessage `json:"boundaries"`
	RemovedHosts           map[string]json.RawMessage `json:"removedHosts"`
	Tokens                 json.RawMessage            `json:"tokens"`
	ProbeEpochs            json.RawMessage            `json:"probeEpochs"`
	ProbeEpochSeq          json.RawMessage            `json:"probeEpochSeq"`
	GuardEpoch             json.RawMessage            `json:"guardEpoch"`
	WallClockHighWaterMark json.RawMessage            `json:"wallClockHighWaterMark"`
	PendingCompensation    json.RawMessage            `json:"pendingCompensation"`
	// FencingQuarantines is retired (comp08 2b): a prior-build corrupt store
	// carries the per-host fencing-quarantine marker set. It is consumed as a
	// raw region — a prior file loads, and the replacement store never carries a
	// marker again.
	FencingQuarantines json.RawMessage `json:"fencingQuarantines"`
}

// readStoreForCustody decodes a corrupt store file for the custody snapshot. It
// keeps the byte-level rules (valid UTF-8, no lone surrogate escape, no key
// named twice) and the strict decode of everything custody stands on — the
// version, the allocator high-water mark, the record list, the tombstones that
// are removal evidence, compactSeq, and the boundary mirror and removal markers
// ownership is derived from — while the families the replacement store
// discards are consumed without a schema judgment (see custodyDocument). Any
// failure on the strict half means a region of the file could not be read, so
// the snapshot cannot be complete.
func readStoreForCustody(fs afero.Fs, path string) (snapshot, error) {
	// The kind rule runs before the read, on the path itself: a symlinked or
	// otherwise non-regular store path is refused here, never followed into a
	// snapshot the rename step would then refuse — which would leave intent and
	// custody artifacts that wedge every later boot.
	info, err := lstat(fs, path)
	if err != nil {
		return snapshot{}, fmt.Errorf("stat corrupt store %s: %w", path, err)
	}
	if err := rejectNonStoreFileKind(path, info); err != nil {
		return snapshot{}, err
	}
	raw, err := afero.ReadFile(fs, path)
	if err != nil {
		return snapshot{}, fmt.Errorf("read corrupt store %s: %w", path, err)
	}
	if err := checkQuarantineBytes(raw); err != nil {
		return snapshot{}, fmt.Errorf("%s: %w", path, err)
	}
	var file custodyDocument
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return snapshot{}, fmt.Errorf("decode %s: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return snapshot{}, fmt.Errorf("decode %s trailing data", path)
	}
	switch {
	case file.Version == nil:
		return snapshot{}, fmt.Errorf("%s carries no version", path)
	case file.Sequence == nil:
		return snapshot{}, fmt.Errorf("%s carries no state-transition sequence", path)
	case file.AllocatorHighWaterMark == nil:
		return snapshot{}, fmt.Errorf("%s carries no allocator high-water mark", path)
	case file.Records == nil:
		return snapshot{}, fmt.Errorf("%s carries no record list", path)
	}
	if *file.Version != storeVersion {
		return snapshot{}, fmt.Errorf("%s carries unsupported store version %d", path, *file.Version)
	}
	state := snapshot{
		Version:                *file.Version,
		Sequence:               *file.Sequence,
		AllocatorHighWaterMark: *file.AllocatorHighWaterMark,
		CompactSeq:             file.CompactSeq,
	}
	state.Records = make([]Record, len(*file.Records))
	for i, encoded := range *file.Records {
		record, err := encoded.record()
		if err != nil {
			return snapshot{}, fmt.Errorf("%s: %w", path, err)
		}
		state.Records[i] = record
	}
	if file.Tombstones != nil {
		state.Tombstones = make([]Tombstone, len(*file.Tombstones))
		for i, encoded := range *file.Tombstones {
			tombstone, err := encoded.tombstone()
			if err != nil {
				return snapshot{}, fmt.Errorf("%s: %w", path, err)
			}
			state.Tombstones[i] = tombstone
		}
	}
	if file.CompactionMarks != nil {
		state.CompactionMarks = append([]CompactionMark(nil), (*file.CompactionMarks)...)
	}
	state.CompactionFloor = file.CompactionFloor
	state.Boundaries = make(map[string]Boundary, len(file.Boundaries))
	state.RemovedHosts = make(map[string]RemovedHost, len(file.RemovedHosts))
	for name, value := range file.Boundaries {
		var boundary Boundary
		item := json.NewDecoder(bytes.NewReader(value))
		item.DisallowUnknownFields()
		if err := item.Decode(&boundary); err != nil {
			return snapshot{}, fmt.Errorf("%s: boundary mirror %q: %w", path, name, err)
		}
		state.Boundaries[name] = boundary
	}
	for name, value := range file.RemovedHosts {
		var removed RemovedHost
		item := json.NewDecoder(bytes.NewReader(value))
		item.DisallowUnknownFields()
		if err := item.Decode(&removed); err != nil {
			return snapshot{}, fmt.Errorf("%s: removal marker %q: %w", path, name, err)
		}
		state.RemovedHosts[name] = removed
	}
	return state, nil
}

// custodyFromStore builds the custody snapshot for the corrupt store file at
// path. state is the custody-scoped decode of the corrupt bytes: the caller
// only calls this when that decode — every region of the file consumed, no key
// named twice, every record mapped — succeeded. This function adds the
// record-set accounting §4 requires:
//
//   - every record parses and validates;
//   - every retained tombstone is valid, and its id is removal evidence;
//   - the parsed ids, against the file's own allocator high-water mark, account
//     for the file's whole record set with no gap or residue: every id in
//     [1..highWaterMark] appears as a retained record or a retained tombstone.
//     A truncation that merely omits a record is a gap below the high-water mark
//     (not a parse error) and is refused here. A file whose compaction evicted
//     the tombstones that were its removal evidence refuses too: it can no
//     longer show its record set whole, and §4 fails startup over that rather
//     than serving past a fence it cannot prove;
//   - every name the file yielded has an ownership entry whose pair and
//     generation high-water mark are valid.
//
// floor is the highest id any existing custody file for this store already
// handed out. Ownership-only ids are allocated above it as well as above the
// corrupt file's own high-water mark, so a later quarantine can never mint an id
// an earlier custody file still references: an id-only resolve (§5) must never
// alias an unrelated record.
// priorImports maps every id an existing custody file already handed out to the
// entry that carries it. A fence import reusing such an id is refused unless it
// is the same record identity — the same lineage's repeated fence keeps its
// original id, while a different record under an earlier custody's id could
// otherwise be cleared by resolving the older custody's row.
func custodyFromStore(state snapshot, path string, epoch uint64, now time.Time, floor uint64, priorImports map[string]custodyFence) (custodyFile, error) {
	if state.Version != storeVersion {
		return custodyFile{}, fmt.Errorf("%w: unsupported store version %d", ErrQuarantineIncomplete, state.Version)
	}
	ids := make(map[uint64]bool, len(state.Records)+len(state.Tombstones))
	previous := ""
	for _, record := range state.Records {
		if err := validateRecord(record); err != nil {
			return custodyFile{}, fmt.Errorf("%w: %w", ErrQuarantineIncomplete, err)
		}
		if previous != "" && record.ID <= previous {
			return custodyFile{}, fmt.Errorf("%w: record %q is out of ascending id order after %q",
				ErrQuarantineIncomplete, record.ID, previous)
		}
		previous = record.ID
		allocated, err := parseAllocatorID(record.ID)
		if err != nil {
			return custodyFile{}, fmt.Errorf("%w: record id %q is not a controller-assigned id", ErrQuarantineIncomplete, record.ID)
		}
		if allocated > state.AllocatorHighWaterMark {
			// Residue: an id above the file's own high-water mark cannot be
			// accounted for by the file's own record set.
			return custodyFile{}, fmt.Errorf("%w: record %q sits above the allocator high-water mark %d",
				ErrQuarantineIncomplete, record.ID, state.AllocatorHighWaterMark)
		}
		if ids[allocated] {
			return custodyFile{}, fmt.Errorf("%w: record id %q is carried twice", ErrQuarantineIncomplete, record.ID)
		}
		ids[allocated] = true
	}
	for _, tombstone := range state.Tombstones {
		if err := validateTombstone(tombstone, state.CompactSeq); err != nil {
			return custodyFile{}, fmt.Errorf("%w: %w", ErrQuarantineIncomplete, err)
		}
		allocated, err := parseAllocatorID(tombstone.ID)
		if err != nil {
			return custodyFile{}, fmt.Errorf("%w: tombstone id %q is not a controller-assigned id", ErrQuarantineIncomplete, tombstone.ID)
		}
		if allocated > state.AllocatorHighWaterMark {
			return custodyFile{}, fmt.Errorf("%w: tombstone %q sits above the allocator high-water mark %d",
				ErrQuarantineIncomplete, tombstone.ID, state.AllocatorHighWaterMark)
		}
		if ids[allocated] {
			return custodyFile{}, fmt.Errorf("%w: id %q is accounted for twice", ErrQuarantineIncomplete, tombstone.ID)
		}
		ids[allocated] = true
	}
	// Coverage, not density: retention and compaction legitimately leave gaps, so
	// completeness is the weaker — and still bounded work — question of whether
	// every id the file no longer carries is accounted for by its own removal
	// evidence rather than lost. Nothing here iterates the file-controlled
	// allocator high-water mark: the accounting is O(records+tombstones), so a
	// file claiming 10^12 records refuses promptly instead of spinning the boot.
	//
	//   - A retained tombstone accounts for its compacted id directly.
	//   - A retained compaction mark accounts for ids at or above the smallest id
	//     its write removed, up to the highest id the file still shows: the marks
	//     record each write's smallest removed id and compaction consumes
	//     terminals oldest-first, so a missing id inside that span is removal
	//     evidence, not loss.
	//   - The dropped-marks floor accounts for ids below every retained minimum:
	//     those are the oldest ids, which only the evicted writes ever removed.
	//     With the ledger whole (floor zero) an id below every minimum was never
	//     compacted, so a gap there is a lost record; and any id above the
	//     highest visible id — where §4's "truncation that merely omits a record"
	//     lands — has no evidence at all. Both refuse.
	missing := state.AllocatorHighWaterMark - uint64(len(ids))
	if missing > 0 {
		smallest, haveSmallest := uint64(0), false
		var maxVisible uint64
		for allocated := range ids {
			if allocated > maxVisible {
				maxVisible = allocated
			}
		}
		markSeqs := make(map[uint64]bool, len(state.CompactionMarks))
		for _, mark := range state.CompactionMarks {
			if err := validateCompactionMark(mark, state.CompactSeq); err != nil {
				return custodyFile{}, fmt.Errorf("%w: %w", ErrQuarantineIncomplete, err)
			}
			if markSeqs[mark.Seq] {
				return custodyFile{}, fmt.Errorf("%w: compaction seq %d is carried by more than one mark",
					ErrQuarantineIncomplete, mark.Seq)
			}
			markSeqs[mark.Seq] = true
			for _, removed := range mark.Hosts {
				allocated, err := parseAllocatorID(removed)
				if err != nil {
					return custodyFile{}, fmt.Errorf("%w: compaction mark seq %d names removed id %q, which is not a controller-assigned id",
						ErrQuarantineIncomplete, mark.Seq, removed)
				}
				if !haveSmallest || allocated < smallest {
					smallest, haveSmallest = allocated, true
				}
			}
		}
		if state.CompactionFloor > state.CompactSeq {
			return custodyFile{}, fmt.Errorf("%w: compaction floor %d is above the store's compactSeq %d",
				ErrQuarantineIncomplete, state.CompactionFloor, state.CompactSeq)
		}
		if !haveSmallest {
			return custodyFile{}, fmt.Errorf("%w: %d ids below the allocator high-water mark %d carry no removal evidence",
				ErrQuarantineIncomplete, missing, state.AllocatorHighWaterMark)
		}
		var visibleBelow, visibleInSpan uint64
		for allocated := range ids {
			switch {
			case allocated < smallest:
				visibleBelow++
			case allocated <= maxVisible:
				visibleInSpan++
			}
		}
		missingBelow := smallest - 1 - visibleBelow
		missingInSpan := uint64(0)
		if maxVisible >= smallest {
			missingInSpan = maxVisible - smallest + 1 - visibleInSpan
		}
		missingAbove := missing - missingBelow - missingInSpan
		if missingAbove > 0 {
			return custodyFile{}, fmt.Errorf("%w: %d ids above the highest id the file still shows carry no removal evidence",
				ErrQuarantineIncomplete, missingAbove)
		}
		if missingBelow > 0 && state.CompactionFloor == 0 {
			return custodyFile{}, fmt.Errorf("%w: %d ids below the smallest id any retained compaction mark removed carry no removal evidence",
				ErrQuarantineIncomplete, missingBelow)
		}
	}

	fences := make([]custodyFence, 0, len(state.Records))
	for _, record := range state.Records {
		if record.State != StateOrphanUnverified {
			continue
		}
		fences = append(fences, custodyFence{
			RecordID:          record.ID,
			Host:              record.Host,
			Kind:              record.Kind,
			ClientOperationID: record.ClientOperationID,
			Generation:        record.Generation,
			IncarnationID:     record.IncarnationID,
		})
	}
	for _, fence := range fences {
		prior, known := priorImports[fence.RecordID]
		if !known {
			continue
		}
		if prior.Kind != "" && prior.Host == fence.Host && prior.Kind == fence.Kind &&
			prior.Generation == fence.Generation && prior.IncarnationID == fence.IncarnationID &&
			prior.ClientOperationID == fence.ClientOperationID {
			// The same lineage's fence, kept under its original id.
			continue
		}
		return custodyFile{}, fmt.Errorf("%w: fence %q for host %q reuses an id an earlier custody file carries",
			ErrQuarantineIncomplete, fence.RecordID, fence.Host)
	}

	ownership, err := custodyOwnershipFrom(state)
	if err != nil {
		return custodyFile{}, err
	}
	// Ownership-only names are allocated fresh controller-assigned ids above the
	// pre-quarantine high-water mark, in name order so the snapshot is
	// deterministic.
	fenced := map[string]bool{}
	for _, fence := range fences {
		fenced[fence.Host] = true
	}
	base := max(state.AllocatorHighWaterMark, floor)
	ownershipOnly := 0
	for i := range ownership {
		if !fenced[ownership[i].Host] {
			ownershipOnly++
		}
	}
	freshIDs, err := nextControllerIDs(base, ownershipOnly)
	if err != nil {
		return custodyFile{}, fmt.Errorf("%w: %w", ErrQuarantineIncomplete, err)
	}
	next := 0
	for i := range ownership {
		if fenced[ownership[i].Host] {
			ownership[i].QuarantineRecordID = fenceIDFor(fences, ownership[i].Host)
			continue
		}
		ownership[i].QuarantineRecordID = freshIDs[next]
		next++
	}
	sort.Slice(ownership, func(i, j int) bool { return ownership[i].Host < ownership[j].Host })

	recordIDs := make([]custodyRecordID, 0, len(fences)+len(ownership))
	for _, fence := range fences {
		recordIDs = append(recordIDs, custodyRecordID{RecordID: fence.RecordID, Host: fence.Host})
	}
	for _, entry := range ownership {
		if fenced[entry.Host] {
			continue
		}
		recordIDs = append(recordIDs, custodyRecordID{RecordID: entry.QuarantineRecordID, Host: entry.Host})
	}
	sort.Slice(recordIDs, func(i, j int) bool { return recordIDs[i].RecordID < recordIDs[j].RecordID })

	return assembleCustody(custodyFile{
		QuarantineEpoch:        epoch,
		QuarantinedFile:        path,
		CustodiedAt:            now.UTC(),
		RecordIDs:              recordIDs,
		AllocatorHighWaterMark: state.AllocatorHighWaterMark,
		Fences:                 fences,
		Ownership:              ownership,
	})
}

// fenceIDFor names the record id an ownership entry reports for a fenced host:
// the first (lowest) open fence's original id, which is stable and unique
// within the custody file.
func fenceIDFor(fences []custodyFence, host string) string {
	id := ""
	for _, fence := range fences {
		if fence.Host != host {
			continue
		}
		if id == "" || fence.RecordID < id {
			id = fence.RecordID
		}
	}
	return id
}

// custodyOwnershipFrom derives one ownership entry per name the corrupt file
// yielded: every name a retained record, a tombstone, the boundary mirror or a
// removal marker names. A tombstone is the last evidence a compacted name has —
// dropping it would reopen the name past the record the quarantine could no
// longer prove — so it counts like any other. The pair is the name's generation
// high-water mark with the incarnation id that mark belongs to; the client
// operation id is the establishing evidence's own when it carried one, and a
// bounded server-minted id otherwise. Every name is held to the record schema's
// host bound here, explicitly, so an over-long name in the mirror or a removal
// marker refuses custody with a message naming the bound rather than failing
// later in the import build. A name that reaches here without a usable pair is
// incomplete: an ownership entry no record can be built from would leave the
// name unaddressed and unclosable.
func custodyOwnershipFrom(state snapshot) ([]custodyOwnership, error) {
	type pair struct {
		generation    uint64
		incarnationID string
		clientOp      string
		recordID      string
	}
	names := map[string]pair{}
	note := func(host string, generation uint64, incarnationID, clientOp, recordID string) {
		current, ok := names[host]
		if !ok || generation > current.generation ||
			(generation == current.generation && recordID != "" && (current.recordID == "" || recordID > current.recordID)) {
			names[host] = pair{generation: generation, incarnationID: incarnationID, clientOp: clientOp, recordID: recordID}
		}
	}
	for _, record := range state.Records {
		if err := validateBoundaryName(record.Host); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrQuarantineIncomplete, err)
		}
		note(record.Host, record.Generation, record.IncarnationID, record.ClientOperationID, record.ID)
	}
	for _, tombstone := range state.Tombstones {
		if err := custodyHostName(tombstone.Host); err != nil {
			return nil, err
		}
		note(tombstone.Host, tombstone.Generation, tombstone.IncarnationID, tombstone.ClientOperationID, tombstone.ID)
	}
	for name, boundary := range state.Boundaries {
		if err := validateBoundary(name, boundary); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrQuarantineIncomplete, err)
		}
		if err := custodyHostName(name); err != nil {
			return nil, err
		}
		note(name, boundary.Generation, boundary.IncarnationID, "", "")
	}
	for name, removed := range state.RemovedHosts {
		if err := validateRemovedHost(name, removed); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrQuarantineIncomplete, err)
		}
		if err := custodyHostName(name); err != nil {
			return nil, err
		}
		note(name, removed.Generation, removed.IncarnationID, "", "")
	}
	hosts := make([]string, 0, len(names))
	for host := range names {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	ownership := make([]custodyOwnership, 0, len(hosts))
	for _, host := range hosts {
		pair := names[host]
		if pair.generation == 0 || pair.incarnationID == "" {
			return nil, fmt.Errorf("%w: name %q yielded no usable ownership pair", ErrQuarantineIncomplete, host)
		}
		clientOp := pair.clientOp
		if clientOp == "" {
			clientOp = quarantineClientOperationID(host)
		}
		ownership = append(ownership, custodyOwnership{
			Host:              host,
			Kind:              KindRestart,
			ClientOperationID: clientOp,
			Generation:        pair.generation,
			HighWaterMark:     pair.generation,
			IncarnationID:     pair.incarnationID,
		})
	}
	return ownership, nil
}

// custodyHostName holds a name the custody derivation uses to the record
// schema's host bound, so an over-long mirrored or removal-marker name is an
// incomplete-custody refusal that names the bound.
func custodyHostName(name string) error {
	if err := validateBoundaryName(name); err != nil {
		return fmt.Errorf("%w: %w", ErrQuarantineIncomplete, err)
	}
	if len(name) > MaxHostNameBytes {
		return fmt.Errorf("%w: name %q is %d bytes, over the %d-byte host bound",
			ErrQuarantineIncomplete, name, len(name), MaxHostNameBytes)
	}
	return nil
}

// readCustodyFile reads and validates one custody file at path for the store at
// storePath. It is the recovery reader: a boot that depends on a custody file —
// a pending intent, or an aside file with no replacement — refuses when the file
// is missing, schema-invalid, readable beyond its owner, or fails the same
// completeness rules the writing boot applied.
func readCustodyFile(fs afero.Fs, path, storePath string) (custodyFile, error) {
	info, err := lstat(fs, path)
	if err != nil {
		return custodyFile{}, fmt.Errorf("%w: stat custody file %s: %w", ErrQuarantineIncomplete, path, err)
	}
	if err := rejectNonStoreFileKind(path, info); err != nil {
		return custodyFile{}, fmt.Errorf("%w: %w", ErrQuarantineIncomplete, err)
	}
	if perm := info.Mode().Perm(); !ownerOnly(perm) {
		return custodyFile{}, fmt.Errorf("%w: %w: %s has mode %04o", ErrQuarantineIncomplete, ErrStoreReadableBeyondOwner, path, perm)
	}
	raw, err := afero.ReadFile(fs, path)
	if err != nil {
		return custodyFile{}, fmt.Errorf("%w: read custody file %s: %w", ErrQuarantineIncomplete, path, err)
	}
	if err := checkQuarantineBytes(raw); err != nil {
		return custodyFile{}, fmt.Errorf("%w: %s: %w", ErrQuarantineIncomplete, path, err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return custodyFile{}, fmt.Errorf("%w: decode %s: %w", ErrQuarantineIncomplete, path, err)
	}
	sameKeySet := map[string]bool{
		"quarantineEpoch": true, "quarantinedFile": true, "custodiedAt": true, "recordIds": true,
		"allocatorHighWaterMark": true, "fences": true, "ownership": true,
	}
	if len(top) != len(sameKeySet) {
		return custodyFile{}, fmt.Errorf("%w: %s is not the custody schema", ErrQuarantineIncomplete, path)
	}
	for name := range top {
		if !sameKeySet[name] {
			return custodyFile{}, fmt.Errorf("%w: %s carries unknown key %q", ErrQuarantineIncomplete, path, name)
		}
	}
	var custody custodyFile
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&custody); err != nil {
		return custodyFile{}, fmt.Errorf("%w: decode %s: %w", ErrQuarantineIncomplete, path, err)
	}
	if custody.QuarantinedFile != storePath {
		return custodyFile{}, fmt.Errorf("%w: %s names the quarantined file %q, not %q",
			ErrQuarantineIncomplete, path, custody.QuarantinedFile, storePath)
	}
	complete, err := assembleCustody(custody)
	if err != nil {
		return custodyFile{}, err
	}
	return complete, nil
}

// replacementState is the state the replacement store opens with: empty except
// for the custody imports, `compactSeq` from zero, zero outstanding tokens, and
// the row-id allocator starting above the custodial high-water mark plus the
// ids the ownership-only imports consumed — so no fresh operation reuses an
// imported record's id. floor carries the highest id any earlier custody file
// for this store handed out, so an id is never reused across quarantine epochs
// either.
func replacementState(custody custodyFile, floor uint64) (snapshot, error) {
	records, err := custodyImports(custody)
	if err != nil {
		return snapshot{}, fmt.Errorf("%w: %w", ErrQuarantineIncomplete, err)
	}
	allocated := max(custody.AllocatorHighWaterMark, floor)
	for _, record := range records {
		id, err := parseAllocatorID(record.ID)
		if err != nil {
			return snapshot{}, fmt.Errorf("%w: %w", ErrQuarantineIncomplete, err)
		}
		if id > allocated {
			allocated = id
		}
	}
	state := snapshot{
		Version:                storeVersion,
		AllocatorHighWaterMark: allocated,
		Records:                records,
		Boundaries:             map[string]Boundary{},
		Tombstones:             []Tombstone{},
		CompactionMarks:        []CompactionMark{},
		RemovedHosts:           map[string]RemovedHost{},
		Tokens:                 []Token{},
		ProbeEpochs:            []ProbeEpoch{},
		ProbeEpochSeq:          map[string]uint64{},
	}
	if err := validateSnapshot(state); err != nil {
		return snapshot{}, fmt.Errorf("%w: the replacement store does not validate: %w", ErrQuarantineIncomplete, err)
	}
	return state, nil
}

// nextControllerIDs mints count controller-assigned ids strictly above base, in
// ascending order. An allocation that would wrap is refused rather than
// wrapped: a wrapped id is a zero or an id an earlier quarantine already handed
// out, and either could alias an unrelated record under an id-only resolve.
func nextControllerIDs(base uint64, count int) ([]string, error) {
	if count < 0 || uint64(count) > math.MaxUint64-base {
		return nil, fmt.Errorf("cannot allocate %d controller-assigned ids above %d", count, base)
	}
	ids := make([]string, 0, count)
	for n := uint64(1); n <= uint64(count); n++ {
		ids = append(ids, formatAllocatorID(base+n))
	}
	return ids, nil
}

// readDirNames lists the entry names of the directory beside a store, for the
// artifact scans. A missing directory is empty — a store that was never beside
// anything has no artifacts — while any other read failure is reported.
func readDirNames(fs afero.Fs, dir string) ([]string, error) {
	entries, err := afero.ReadDir(fs, dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("hostops: read quarantine artifacts in %s: %w", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names, nil
}
