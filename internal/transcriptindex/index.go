package transcriptindex

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"

	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apptranscript"
)

const (
	// formatVersion is the sidecar's layout. projectionID names the projection
	// its records reproduce; either changing rebuilds every index.
	formatVersion = 5
	projectionID  = "transcript-read-model-v6"

	// tailBytes is how much of the covered prefix's end validation compares,
	// the check the attention fold cursor uses (agent/session_attention.go).
	tailBytes = 4096

	// maxLineBytes bounds one transcript line, as the server's readers do.
	maxLineBytes = 128 << 20
)

// updateLogRecords is how many of the newest update log records the index
// keeps: once the log holds more than twice as many, an extension cuts it
// back to them. A reader whose snapshot predates the kept log gets
// ErrUpdateLogTruncated from ChangedSince and replaces its window instead.
// About one record is logged per entry, so this covers the last ten
// thousand entries or so. A variable only so tests can shrink it.
var updateLogRecords = 10_000

// SetUpdateLogRecordsForTest shrinks the kept update log for the duration of
// a test, so a caller in another package can force ErrUpdateLogTruncated
// without writing thousands of entries. Restore undoes it.
func SetUpdateLogRecordsForTest(n int) (restore func()) {
	previous := updateLogRecords
	updateLogRecords = n
	return func() { updateLogRecords = previous }
}

// ErrUpdateLogTruncated reports a ChangedSince for a length older than the
// kept update log: the changes since then are not all known any more.
// LatestSince propagates it rather than answering with the window alone,
// since held items outside the window may have changed with no way for the
// caller to tell; the hub maps it to appwire.TranscriptItemCursorStale() so
// the client re-reads without its held snapshot. A daemon's own ChangedSince
// call (project, in server/thread_history.go) treats it as a failure and
// rebuilds.
var ErrUpdateLogTruncated = errors.New("transcript index update log no longer reaches that length")

// testKillAfterRecordWrites, when set, runs right after an extension's or a
// rebuild's scan has written its records and before the meta commit that
// would make them readable: it stands for the process being killed at
// exactly that point, leaving the records on disk uncounted. An error it
// returns propagates as scan's own would, so writeMeta never runs. Test seam
// only; nil in production.
var testKillAfterRecordWrites func() error

// testKillAfterItemUpdateLog, when set, runs in addContributor right after
// its update-log row is written and before the item record's own in-place
// overwrite: it stands for the process being killed at exactly that point,
// leaving the log row on disk (uncommitted, past the committed count) with
// the item slot it names still unmodified. Test seam only; nil in
// production.
var testKillAfterItemUpdateLog func() error

// The sidecar directory holds a lock file, a pointer to the live build, and one
// directory per build. A rebuild writes a new build and renames the pointer
// over, so a reader never sees a half-built index.
const (
	lockFileName    = "lock"
	currentFileName = "CURRENT"
	metaFile        = "meta.json"
	itemsFile       = "items"
	turnsFile       = "turns"
	updatesFile     = "updates"
	stringsFile     = "strings"
)

// meta is a build's small header. The extender writes it after the
// records, so it never counts a record the tables lack. Records past its
// counts belong to an extension that did not finish; nothing reads them, and
// the next extension writes over them.
type meta struct {
	Format int `json:"format"`
	// Projection is projectionID with schemaID folded in (see
	// currentProjection), not just the base projection name: a build whose
	// entries a different schema.Turn/llm.Message shape validated must not
	// be adopted by a reader with a different one, since window.go
	// re-decodes raw transcript bytes leniently
	// (agent/transcript.DecodeValidatedEntry) and would otherwise drop an
	// unknown field silently. Folding it into the field every reader
	// already checks byte-for-byte — rather than a new field only a
	// schema-aware reader would look at — means a binary that predates
	// this check at all still rejects the mismatch: it fails the exact
	// same "transcript index format or projection changed" comparison it
	// always ran, just against a longer string, and rebuilds.
	Projection string `json:"projection"`
	// Incarnation names what the records describe. It changes only when the
	// transcript stops being an extension of the covered prefix; a rebuild
	// the index needs for itself (errRebuild) keeps it.
	Incarnation  string `json:"incarnation"`
	FileIdentity string `json:"file_identity"`
	// Length is the transcript bytes covered: the header and every complete
	// entry line. Entries counts those entries; the next one's ordinal. Both
	// are published together, with the table counts, in one meta write.
	Length       int64  `json:"length"`
	Entries      uint64 `json:"entries"`
	TailSHA256   string `json:"tail_sha256"`
	HeaderOffset int64  `json:"header_offset"`
	HeaderLength int64  `json:"header_length"` // 0: no header yet
	Items        uint64 `json:"items"`
	Turns        uint64 `json:"turns"`
	Updates      uint64 `json:"updates"`
	// UpdatesFile names the update log's file in the build ("" is the
	// first, updatesFile); a cut writes the kept records to a new file.
	// UpdatesFrom is the offset from which the log holds every update: those
	// caused by entries before it may have been cut (0: nothing was).
	UpdatesFile string `json:"updates_file,omitempty"`
	UpdatesFrom int64  `json:"updates_from,omitempty"`
	// The legacy grouping state, for grouping the next legacy entry: whether
	// a legacy group is open, and its id and summary slot.
	Open       bool   `json:"open"`
	OpenTurnID string `json:"open_turn_id"`
	TurnSlot   uint64 `json:"turn_slot"`
	// PendingCommunicate reports whether the builder's commCalls held any
	// deferred communicate call at the end of the last build/extend: the
	// transcript's tail is a communicate call with no result yet. CommCalls
	// and LastAssistant* below are what restoreBuilder needs to reconstruct
	// that state without a rebuild.
	PendingCommunicate bool `json:"pending_communicate,omitempty"`
	// CommCalls persists the builder's open communicate calls (see
	// builder.commCalls), so an adopting handle's restoreBuilder can rebuild
	// them instead of needing a full rebuild every time it adopts a build
	// with a pending call — the records alone (item/turn tables) don't carry
	// this, since a deferred communicate call projects no item of its own
	// until it is paired or flushed.
	CommCalls []persistedCommCall `json:"comm_calls,omitempty"`
	// LastAssistant* persists the builder's sticky last-assistant-text state
	// (builder.lastAssistantText/TurnID/Pos/Known), for the same reason: the
	// healed-communicate echo check needs it, and only a full build or a
	// text-bearing assistant entry scanned since would otherwise supply it.
	LastAssistantText   string       `json:"last_assistant_text,omitempty"`
	LastAssistantTurnID string       `json:"last_assistant_turn_id,omitempty"`
	LastAssistantPos    persistedPos `json:"last_assistant_pos"`
	LastAssistantKnown  bool         `json:"last_assistant_known,omitempty"`
}

// persistedPos is a transcript entry's position: the contributor fields
// restoreBuilder needs from meta, minus Name (always absent for an
// assistant entry, the only kind commState/lastAssistantPos ever hold).
type persistedPos struct {
	Offset  int64  `json:"offset"`
	Ordinal uint64 `json:"ordinal"`
	Length  uint32 `json:"length"`
}

func posOf(c contributor) persistedPos {
	return persistedPos{Offset: c.Offset, Ordinal: c.Ordinal, Length: c.Length}
}

func (p persistedPos) contributor() contributor {
	return contributor{Offset: p.Offset, Ordinal: p.Ordinal, Length: p.Length}
}

// validatePersistedPos bounds a persisted position against the covered
// transcript, the same way adopt bounds meta.HeaderOffset/HeaderLength: a
// self-consistent but fabricated meta.json must not reach reader.entry's
// make([]byte, length) with an enormous or negative length. Length 0 is the
// sentinel for "no position" (see pendingRegistry's add), never read, so it
// always passes.
func validatePersistedPos(m meta, p persistedPos) error {
	if p.Length == 0 {
		return nil
	}
	if p.Offset < 0 || int64(p.Length) > maxLineBytes || p.Offset > m.Length || int64(p.Length) > m.Length-p.Offset {
		return fmt.Errorf("%w: persisted position outside the covered transcript", errCorrupt)
	}
	return nil
}

// persistedCommCall is one builder.commCalls entry (see commState), persisted
// so restoreBuilder can reconstruct it without a rebuild.
type persistedCommCall struct {
	ID           string `json:"id"`
	RawArgs      string `json:"raw_args"`
	persistedPos        // flattened: offset, ordinal, length
	TurnID       string `json:"turn_id"`
}

// EntryError reports the entry the index could not apply: a decode failure or
// a builder error, at scan or extension time.
type EntryError struct {
	Ordinal uint64
	Err     error
}

func (e *EntryError) Error() string { return fmt.Sprintf("entry %d: %v", e.Ordinal, e.Err) }
func (e *EntryError) Unwrap() error { return e.Err }

// Index is an open transcript index. It is safe for concurrent use, and other
// handles, in this process or another, may share its sidecar directory.
type Index struct {
	mu         sync.Mutex
	path, dir  string
	lock       *os.File
	transcript *os.File
	build      string // the live build's directory name
	items      table
	turns      table
	updates    table
	strings    blob
	meta       meta
	prelude    *appwire.Turn
	builder    builder
	// updatesName is the update log file updates has open.
	updatesName string
	// builderStale reports records another handle extended, which the
	// builder's open-turn state does not reflect yet.
	builderStale bool
	// stale marks an index a read found inconsistent with its transcript; the
	// next extension rebuilds it.
	stale bool
	// rebuilds counts full builds by this handle, for tests.
	rebuilds int
}

// Open returns the index for the transcript at path, kept in dir. A missing,
// stale or corrupt index is rebuilt from the transcript. Open catches up to
// the last complete line.
func Open(path, dir string) (*Index, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create transcript index dir: %w", err)
	}
	lock, err := os.OpenFile(filepath.Join(dir, lockFileName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open transcript index lock: %w", err)
	}
	x := &Index{path: path, dir: dir, lock: lock}
	if err := x.CatchUp(); err != nil {
		_ = x.Close()
		return nil, err
	}
	return x, nil
}

// Close releases the index's files.
func (x *Index) Close() error {
	x.mu.Lock()
	defer x.mu.Unlock()
	err := x.closeBuild()
	if x.lock != nil {
		err = errors.Join(err, x.lock.Close())
		x.lock = nil
	}
	return err
}

// CatchUp indexes every complete line in the transcript.
func (x *Index) CatchUp() error {
	info, err := os.Stat(x.path)
	if err != nil {
		return fmt.Errorf("stat transcript: %w", err)
	}
	return x.CatchUpTo(info.Size())
}

// CatchUpTo indexes the complete lines that end at or before length: the
// recorded length, for a caller that knows it. A transcript that is no longer
// the covered file grown by appends is rebuilt, under a new incarnation.
func (x *Index) CatchUpTo(length int64) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.locked(true, func() error { return x.extend(length) })
}

// Rebuild discards the current build and indexes the transcript again, up to
// length, under a new incarnation. Unlike the internal rebuild an extension
// falls back to when it cannot apply an entry incrementally (errRebuild),
// which keeps the incarnation because the transcript still extends the
// covered prefix, Rebuild always mints a new one: a reader holding an older
// snapshot must not mistake its items and turns for still-valid history.
func (x *Index) Rebuild(length int64) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.locked(true, func() error { return x.rebuild(length, "") })
}

// locked runs fn holding the sidecar lock, shared or exclusive, after taking
// up whatever other handles wrote.
func (x *Index) locked(exclusive bool, fn func() error) error {
	if x.lock == nil {
		return errors.New("transcript index is closed")
	}
	if err := lockFile(x.lock, exclusive); err != nil {
		return fmt.Errorf("lock transcript index: %w", err)
	}
	defer func() { _ = unlockFile(x.lock) }()
	if err := x.refresh(); err != nil && !exclusive {
		// Only an extender may rebuild; a reader with no usable index has
		// nothing to read.
		return err
	}
	return fn()
}

// refresh takes up the sidecar as it is on disk: another build reopens,
// and a changed covered state (another handle extended) is adopted.
func (x *Index) refresh() error {
	current, err := os.ReadFile(filepath.Join(x.dir, currentFileName))
	if err != nil {
		_ = x.closeBuild()
		return errors.New("transcript index has no live build")
	}
	name := strings.TrimSpace(string(current))
	if name != x.build {
		_ = x.closeBuild()
		if err := x.load(name); err != nil {
			_ = x.closeBuild()
			return err
		}
		return nil
	}
	m, err := x.readMeta()
	if err != nil {
		_ = x.closeBuild()
		return err
	}
	if !reflect.DeepEqual(m, x.meta) {
		if err := x.adopt(m); err != nil {
			_ = x.closeBuild()
			return err
		}
	}
	return nil
}

func (x *Index) readMeta() (meta, error) {
	var m meta
	data, err := os.ReadFile(filepath.Join(x.dir, x.build, metaFile))
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, err
	}
	if m.Format != formatVersion || m.Projection != currentProjection() {
		return m, errors.New("transcript index format, projection, or schema identity changed")
	}
	return m, nil
}

// adopt takes m as the covered state. The builder's open-turn state is
// restored from records before the next extension.
func (x *Index) adopt(m meta) error {
	if err := x.openUpdates(m.updatesFileName()); err != nil {
		return err
	}
	items, err := x.items.available()
	if err != nil {
		return err
	}
	turns, err := x.turns.available()
	if err != nil {
		return err
	}
	updates, err := x.updates.available()
	if err != nil {
		return err
	}
	if m.Items > items || m.Turns > turns || m.Updates > updates {
		return fmt.Errorf("%w: meta counts records the tables lack", errCorrupt)
	}
	transcriptInfo, err := x.transcript.Stat()
	if err != nil {
		return err
	}
	// Bound every persisted offset/length against the real transcript before
	// readHeader allocates from them: a self-consistent but fabricated or
	// stale-build meta.json (Length itself unchecked) would otherwise pass
	// the header_offset+header_length<=Length inequality and still panic
	// make([]byte, ...) with an enormous or negative HeaderLength.
	if m.Length < 0 || m.Length > transcriptInfo.Size() {
		return fmt.Errorf("%w: length exceeds the transcript", errCorrupt)
	}
	if m.HeaderLength < 0 || m.HeaderOffset < 0 || m.HeaderLength > maxLineBytes ||
		m.HeaderOffset > m.Length || m.HeaderLength > m.Length-m.HeaderOffset {
		return fmt.Errorf("%w: header_offset/header_length outside the covered transcript", errCorrupt)
	}
	// Bound CommCalls[].persistedPos and LastAssistantPos the same way, before
	// restoreBuilder's reader.entry allocates from them (see writeMeta):
	// otherwise a self-consistent but fabricated meta.json could name a
	// multi-gigabyte length and OOM the process on read.
	if m.PendingCommunicate != (len(m.CommCalls) > 0) {
		return fmt.Errorf("%w: pending_communicate disagrees with comm_calls", errCorrupt)
	}
	for _, c := range m.CommCalls {
		if err := validatePersistedPos(m, c.persistedPos); err != nil {
			return err
		}
	}
	if err := validatePersistedPos(m, m.LastAssistantPos); err != nil {
		return err
	}
	info, err := x.strings.file.Stat()
	if err != nil {
		return err
	}
	x.meta, x.items.n, x.turns.n, x.updates.n, x.strings.n = m, m.Items, m.Turns, m.Updates, uint64(info.Size())
	x.builderStale = true
	return x.readHeader()
}

// load opens build name, which must describe the transcript at path.
func (x *Index) load(name string) error {
	x.build = name
	if err := x.openFiles(false); err != nil {
		return err
	}
	m, err := x.readMeta()
	if err != nil {
		return err
	}
	info, err := x.transcript.Stat()
	if err != nil {
		return err
	}
	if apptranscript.FileIdentity(info) != m.FileIdentity {
		return errors.New("transcript index describes another file")
	}
	return x.adopt(m)
}

// extend covers complete lines up to length, rebuilding when the covered
// prefix is no longer the transcript's.
func (x *Index) extend(length int64) error {
	info, err := os.Stat(x.path)
	if err != nil {
		return fmt.Errorf("stat transcript: %w", err)
	}
	length = min(length, info.Size())
	if x.build == "" || x.stale || !x.grownByAppends(info) {
		return x.rebuild(length, "")
	}
	if err := x.repairBuilder(length); err != nil {
		return err
	}
	if length <= x.meta.Length {
		return nil
	}
	// An extension interrupted after writing its records but before
	// committing meta can leave update-log rows past the committed count:
	// truncating them away and re-scanning only up to this call's length
	// assumes the scan will re-derive everything they held. That holds for
	// a row logging a new record's own update (the scan recreates the
	// record itself, so its later update comes back too, or the record
	// stays new and uncommitted either way), but not for a row logging an
	// in-place update to an already-committed record: its slot precedes the
	// committed counts, so truncate never touches it, and the update may
	// already be on disk from an entry this scan does not reach. Collect
	// such rows' offsets before truncating them away, so they can be
	// checked against where the scan below actually lands.
	unsafeOffsets, err := x.leftoverUpdatesToCommittedSlots()
	if err != nil {
		return x.fail(err)
	}
	// Whatever an extension that did not finish left past the counts goes
	// before this one appends.
	for _, t := range []*table{&x.items, &x.turns, &x.updates} {
		if err := t.truncate(t.n); err != nil {
			return err
		}
	}
	if err := x.scan(length); err != nil {
		// The records may hold part of this scan; only a full build is
		// known to be consistent. The transcript still extends the covered
		// prefix when the index only needed the whole file's tool names.
		incarnation := ""
		if errors.Is(err, errRebuild) {
			incarnation = x.meta.Incarnation
		}
		return x.rebuild(length, incarnation)
	}
	// A scan only advances x.meta.Length past a line once that whole line is
	// read and applied, in order, so x.meta.Length landing past one of the
	// offsets collected above proves the entry there was a complete line
	// within this scan's bound and so was fully redone; landing at or before
	// it proves the scan stopped no later than the entry's own start,
	// meaning it never reprocessed it, leaving whatever the truncated row
	// announced already on disk from before this call with nothing left to
	// say so. Rebuild instead of trusting that: it recomputes every record
	// from scratch up to length, so it cannot leave such a leftover behind.
	// The transcript still extends the covered prefix, so the incarnation
	// is kept, as errRebuild's rebuild above does.
	for _, offset := range unsafeOffsets {
		if x.meta.Length <= offset {
			return x.rebuild(length, x.meta.Incarnation)
		}
	}
	x.meta.PendingCommunicate = len(x.builder.commCalls) > 0
	if x.meta.PendingCommunicate && !x.builder.lastAssistantKnown {
		// A communicate call just became pending with lastAssistantText
		// still unknown. restoreBuilder now restores it from meta (see its
		// doc comment), so this should not happen once any build has run;
		// kept as a defensive fallback for an index built before that field
		// existed (a lower formatVersion rebuilds instead, but a defense in
		// depth costs nothing here). The echo check pendingFlush relies on
		// would be missing the true prior text otherwise.
		return x.rebuild(length, x.meta.Incarnation)
	}
	if testKillAfterRecordWrites != nil {
		if err := testKillAfterRecordWrites(); err != nil {
			return err
		}
	}
	return x.writeMeta()
}

// repairBuilder brings x.builder up to date with the covered records when
// another handle's extension left it stale: adopt updates the covered
// counts/meta but never touches x.builder, so a caller that consults it
// directly (pendingFlush's commCalls/lastAssistant* state) would otherwise
// see the state from before that extension. restoreBuilder recovers it,
// including a pending communicate call, from the meta fields persisted for
// that purpose (CommCalls/LastAssistant*). A builder that is not stale is
// left untouched, so a caller re-checking the same length repeatedly (a
// pending communicate call outliving several reads, or another handle
// adopting it) costs nothing once repaired: it never mints a new build, so
// it never gives another handle something new to adopt in turn.
func (x *Index) repairBuilder(length int64) error {
	if !x.builderStale {
		return nil
	}
	if err := x.restoreBuilder(); err != nil {
		return x.rebuild(length, "")
	}
	return nil
}

// leftoverUpdatesToCommittedSlots returns the causing Offset of every
// leftover update-log row (past its committed count, the signature of an
// extension that wrote its records but was interrupted before the meta
// commit that would count them) that logs an in-place update to an
// already-committed item or turn record: one whose slot precedes
// items.n/turns.n. Only rows of a kind real code appends (updatedItem,
// updatedTurn) count: garbage bytes written straight to the file (as a test
// fault-injects to simulate leftovers, bypassing logUpdate) decode to
// neither and name no slot at all.
func (x *Index) leftoverUpdatesToCommittedSlots() ([]int64, error) {
	available, err := x.updates.available()
	if err != nil {
		return nil, err
	}
	var offsets []int64
	for slot := x.updates.n; slot < available; slot++ {
		buf := make([]byte, x.updates.size)
		if _, err := x.updates.file.ReadAt(buf, int64(slot)*x.updates.size); err != nil {
			return nil, fmt.Errorf("%w: read leftover update record: %w", errCorrupt, err)
		}
		row := decodeUpdate(buf)
		var committed uint64
		switch row.Kind {
		case updatedItem:
			committed = x.items.n
		case updatedTurn:
			committed = x.turns.n
		default:
			continue
		}
		if row.Slot < committed {
			offsets = append(offsets, row.Offset)
		}
	}
	return offsets, nil
}

// grownByAppends reports whether the transcript at path is still the file the
// index covers: the same file, at least as long, with the same bytes before
// the covered end.
func (x *Index) grownByAppends(info os.FileInfo) bool {
	if x.transcript == nil || x.meta.FileIdentity == "" || apptranscript.FileIdentity(info) != x.meta.FileIdentity || info.Size() < x.meta.Length {
		return false
	}
	sum, err := x.tailSum(x.meta.Length)
	return err == nil && sum == x.meta.TailSHA256
}

func (x *Index) tailSum(end int64) (string, error) {
	start := max(0, end-tailBytes)
	buf := make([]byte, end-start)
	if _, err := x.transcript.ReadAt(buf, start); err != nil {
		return "", err
	}
	sum := sha256.Sum256(buf)
	return hex.EncodeToString(sum[:]), nil
}

// restoreBuilder recovers the builder's state from meta and records: the open
// legacy group's calls, and the new-format turns that can still take entries.
func (x *Index) restoreBuilder() error {
	b := newBuilder(x)
	b.grouper = apptranscript.TurnGrouper{Open: x.meta.Open, TurnID: x.meta.OpenTurnID}
	for _, c := range x.meta.CommCalls {
		b.commCalls[c.ID] = commState{rawArgs: c.RawArgs, pos: c.contributor(), turnID: c.TurnID}
	}
	b.lastAssistantText, b.lastAssistantTurnID, b.lastAssistantKnown = x.meta.LastAssistantText, x.meta.LastAssistantTurnID, x.meta.LastAssistantKnown
	b.lastAssistantPos = x.meta.LastAssistantPos.contributor()
	if x.meta.Open {
		b.turnSlot = x.meta.TurnSlot
		// The open legacy group's items are the newest records: any
		// new-format entry would have closed it.
		for slot := x.items.n; slot > 0; slot-- {
			buf, err := x.items.read(slot-1, 1)
			if err != nil {
				return err
			}
			record := decodeItem(buf)
			if uint64(record.Turn) != b.turnSlot {
				break
			}
			if record.Call.Len > 0 {
				call, err := x.strings.get(record.Call)
				if err != nil {
					return err
				}
				b.calls[string(call)] = slot - 1
			}
		}
	}
	if err := b.restoreOpenTurns(); err != nil {
		return err
	}
	x.builder = b
	x.builderStale = false
	return nil
}

// restoreOpenTurns fills the open map from the summaries: every open
// execution, the latest gap turn and the prelude.
func (b *builder) restoreOpenTurns() error {
	const chunk = 256
	var open []uint64
	gap, prelude := -1, -1
	for start := uint64(0); start < b.x.turns.n; start += chunk {
		count := min(chunk, b.x.turns.n-start)
		buf, err := b.x.turns.read(start, int(count))
		if err != nil {
			return err
		}
		for i := range count {
			record := decodeTurn(buf[i*turnRecordSize:])
			switch {
			case record.Kind == turnKindExecution && record.Status == statusOpen:
				open = append(open, start+i)
			case record.Kind == turnKindGap:
				gap = int(start + i)
			case record.Kind == turnKindPrelude:
				prelude = int(start + i)
			}
		}
	}
	for _, slot := range []int{gap, prelude} {
		if slot >= 0 {
			open = append(open, uint64(slot))
		}
	}
	for _, slot := range open {
		buf, err := b.x.turns.read(slot, 1)
		if err != nil {
			return err
		}
		id, err := b.x.strings.get(decodeTurn(buf).ID)
		if err != nil {
			return err
		}
		b.open[string(id)] = slot
		if gap >= 0 && slot == uint64(gap) {
			b.gap = string(id)
		}
	}
	return nil
}

func (x *Index) readHeader() error {
	x.prelude = nil
	if x.meta.HeaderLength == 0 {
		return nil
	}
	line := make([]byte, x.meta.HeaderLength)
	if _, err := x.transcript.ReadAt(line, x.meta.HeaderOffset); err != nil {
		return err
	}
	header, err := transcript.DecodeHeader(bytes.TrimSpace(line))
	if err != nil {
		return err
	}
	x.prelude = apptranscript.PreludeTurn(header)
	return nil
}

// rebuild indexes the transcript from its start into a new build, then makes
// it the live one. incarnation carries the covered records' incarnation over
// when the transcript still extends them; "" mints a new one. A build that
// fails is removed, and the live build stays as it was.
func (x *Index) rebuild(length int64, incarnation string) error {
	err := x.buildNew(length, incarnation)
	if err != nil && x.build != "" {
		failed := filepath.Join(x.dir, x.build)
		err = errors.Join(err, x.closeBuild(), os.RemoveAll(failed))
	}
	return err
}

func (x *Index) buildNew(length int64, incarnation string) error {
	if err := x.closeBuild(); err != nil {
		return err
	}
	name, err := randomName()
	if err != nil {
		return err
	}
	if incarnation == "" {
		if incarnation, err = randomName(); err != nil {
			return err
		}
	}
	if err := os.Mkdir(filepath.Join(x.dir, name), 0o755); err != nil {
		return fmt.Errorf("create transcript index build: %w", err)
	}
	x.build = name
	if err := x.openFiles(true); err != nil {
		return err
	}
	info, err := x.transcript.Stat()
	if err != nil {
		return err
	}
	length = min(length, info.Size())
	x.meta = meta{Format: formatVersion, Projection: currentProjection(), Incarnation: incarnation, FileIdentity: apptranscript.FileIdentity(info)}
	x.prelude = nil
	x.stale, x.builderStale = false, false
	x.builder = newBuilder(x)
	x.builder.global = map[string]string{}
	x.rebuilds++
	if err := x.scan(length); err != nil {
		return err
	}
	// Only the open turn's names are kept past a build: memory stays bounded
	// by the open turn, and errRebuild covers the rest.
	x.builder.global = nil
	x.meta.PendingCommunicate = len(x.builder.commCalls) > 0
	if testKillAfterRecordWrites != nil {
		if err := testKillAfterRecordWrites(); err != nil {
			return err
		}
	}
	if err := x.writeMeta(); err != nil {
		return err
	}
	if err := writeFileAtomically(filepath.Join(x.dir, currentFileName), []byte(name+"\n")); err != nil {
		return err
	}
	x.removeOtherBuilds()
	return nil
}

// removeOtherBuilds deletes every build but the live one. A reader in another
// process may still hold an old one's files open; where the platform refuses
// the removal, the next rebuild tries again.
func (x *Index) removeOtherBuilds() {
	entries, err := os.ReadDir(x.dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() && entry.Name() != x.build {
			_ = os.RemoveAll(filepath.Join(x.dir, entry.Name()))
		}
	}
}

// scan covers complete lines from the covered end up to length.
func (x *Index) scan(length int64) error {
	reader := bufio.NewReaderSize(io.NewSectionReader(x.transcript, x.meta.Length, length-x.meta.Length), 64<<10)
	offset := x.meta.Length
	for {
		line, complete, n, err := transcript.ReadLine(reader, maxLineBytes)
		if err != nil {
			return err
		}
		if !complete {
			break
		}
		start := offset
		offset += n
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			x.meta.Length = offset
			continue
		}
		if x.meta.HeaderLength == 0 {
			if _, err := transcript.DecodeHeader(trimmed); err != nil {
				return fmt.Errorf("parse transcript header: %w", err)
			}
			x.meta.HeaderOffset, x.meta.HeaderLength = start, int64(len(line))
			if err := x.readHeader(); err != nil {
				return err
			}
		} else {
			entry, err := transcript.DecodeEntry(trimmed)
			if err != nil {
				// A line that does not decode never will: it is quarantined
				// as one visible item, and the history goes on past it.
				err = x.builder.quarantine(x.meta.Entries, start, uint32(len(line)))
			} else {
				err = x.builder.apply(x.meta.Entries, start, uint32(len(line)), &entry.Turn)
			}
			if err != nil {
				return &EntryError{Ordinal: x.meta.Entries, Err: err}
			}
			x.meta.Entries++
		}
		x.meta.Length = offset
	}
	return nil
}

// updatesFileName is the update log file m names.
func (m meta) updatesFileName() string {
	if m.UpdatesFile == "" {
		return updatesFile
	}
	return m.UpdatesFile
}

// openUpdates makes name the open update log file, if it is not already.
func (x *Index) openUpdates(name string) error {
	if x.updates.file != nil && x.updatesName == name {
		return nil
	}
	f, err := os.OpenFile(filepath.Join(x.dir, x.build, name), os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	if x.updates.file != nil {
		_ = x.updates.file.Close()
	}
	x.updates.file, x.updates.size, x.updatesName = f, updateRecordSize, name
	return nil
}

// cutUpdateLog keeps only the newest updateLogRecords of an update log that
// has grown past twice as many: it writes them to a new file, which the next
// meta names. The old file stays until that meta is written (writeMeta), so
// a crash before it leaves the old log in force; a file no meta names is
// removed at the next cut.
func (x *Index) cutUpdateLog() (old string, err error) {
	keep := uint64(updateLogRecords)
	if x.updates.n <= 2*keep {
		return "", nil
	}
	drop := x.updates.n - keep
	last, err := x.updates.read(drop-1, 1)
	if err != nil {
		return "", err
	}
	kept, err := x.updates.read(drop, int(keep))
	if err != nil {
		return "", err
	}
	name, err := randomName()
	if err != nil {
		return "", err
	}
	name = updatesFile + "." + name
	f, err := os.OpenFile(filepath.Join(x.dir, x.build, name), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("create transcript index update log: %w", err)
	}
	if _, err := f.Write(kept); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("write transcript index update log: %w", err)
	}
	old = x.updatesName
	_ = x.updates.file.Close()
	x.updates.file, x.updates.n, x.updatesName = f, keep, name
	x.meta.UpdatesFile = name
	x.meta.UpdatesFrom = max(x.meta.UpdatesFrom, decodeUpdate(last).Offset+1)
	return old, nil
}

// removeOtherUpdateLogs deletes every update log file in the build but the
// live one.
func (x *Index) removeOtherUpdateLogs() {
	entries, err := os.ReadDir(filepath.Join(x.dir, x.build))
	if err != nil {
		return
	}
	for _, entry := range entries {
		if name := entry.Name(); (name == updatesFile || strings.HasPrefix(name, updatesFile+".")) && name != x.updatesName {
			_ = os.Remove(filepath.Join(x.dir, x.build, name))
		}
	}
}

// writeMeta publishes the covered state, after the records it counts,
// cutting the update log first when it has grown past its bound. A meta that
// fails to publish a cut leaves the index to rebuild.
func (x *Index) writeMeta() error {
	cut, err := x.cutUpdateLog()
	if err != nil {
		return err
	}
	if err := x.publishMeta(); err != nil {
		if cut != "" {
			x.stale = true
		}
		return err
	}
	if cut != "" {
		x.removeOtherUpdateLogs()
	}
	return nil
}

// publishMeta writes the covered state.
func (x *Index) publishMeta() error {
	sum, err := x.tailSum(x.meta.Length)
	if err != nil {
		return err
	}
	b := &x.builder
	x.meta.TailSHA256 = sum
	x.meta.Items, x.meta.Turns, x.meta.Updates = x.items.n, x.turns.n, x.updates.n
	x.meta.Open, x.meta.OpenTurnID = b.grouper.Open, b.grouper.TurnID
	x.meta.TurnSlot = b.turnSlot
	x.meta.CommCalls = persistCommCalls(b.commCalls)
	x.meta.LastAssistantText, x.meta.LastAssistantTurnID, x.meta.LastAssistantKnown = b.lastAssistantText, b.lastAssistantTurnID, b.lastAssistantKnown
	x.meta.LastAssistantPos = posOf(b.lastAssistantPos)
	data, err := json.Marshal(x.meta)
	if err != nil {
		return err
	}
	return writeFileAtomically(filepath.Join(x.dir, x.build, metaFile), data)
}

// persistCommCalls converts the builder's open communicate calls to their
// persisted form (see meta.CommCalls), sorted by call id for a stable
// meta.json across writes of the same state.
func persistCommCalls(m map[string]commState) []persistedCommCall {
	if len(m) == 0 {
		return nil
	}
	list := make([]persistedCommCall, 0, len(m))
	for id, state := range m {
		list = append(list, persistedCommCall{ID: id, RawArgs: state.rawArgs, persistedPos: posOf(state.pos), TurnID: state.turnID})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return list
}

// writeFileAtomically replaces path by rename. There is no fsync: the index is
// derived, and whatever a crash loses rebuilds or replays.
func writeFileAtomically(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write transcript index: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("publish transcript index: %w", err)
	}
	return nil
}

func (x *Index) openFiles(create bool) error {
	var err error
	if x.transcript, err = os.Open(x.path); err != nil {
		return fmt.Errorf("open transcript: %w", err)
	}
	flags := os.O_RDWR
	if create {
		flags |= os.O_CREATE | os.O_EXCL
	}
	open := func(name string) (*os.File, error) {
		return os.OpenFile(filepath.Join(x.dir, x.build, name), flags, 0o600)
	}
	if x.items.file, err = open(itemsFile); err != nil {
		return err
	}
	x.items.size = itemRecordSize
	if x.turns.file, err = open(turnsFile); err != nil {
		return err
	}
	x.turns.size = turnRecordSize
	if create {
		if x.updates.file, err = open(updatesFile); err != nil {
			return err
		}
		x.updates.size, x.updatesName = updateRecordSize, updatesFile
	}
	x.strings.file, err = open(stringsFile)
	return err
}

// closeBuild drops the open build and its view of the transcript.
func (x *Index) closeBuild() error {
	var errs []error
	for _, f := range []**os.File{&x.transcript, &x.items.file, &x.turns.file, &x.updates.file, &x.strings.file} {
		if *f != nil {
			errs = append(errs, (*f).Close())
			*f = nil
		}
	}
	x.build = ""
	x.updatesName = ""
	x.meta = meta{}
	x.prelude = nil
	x.items.n, x.turns.n, x.updates.n, x.strings.n = 0, 0, 0, 0
	return errors.Join(errs...)
}

func randomName() (string, error) {
	var id [8]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(id[:]), nil
}
