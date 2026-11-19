package agent

import (
	"iter"
	"maps"
	"slices"
	"strings"
	"sync/atomic"

	"primeradiant.com/evener/agent/internal/jobstore"
)

// activityJobDurableVisits counts the fold records a durable-membership walk
// examined. It exists so a test can assert that a refresh which publishes
// nothing does not walk the history, which is a cost claim a duration cannot
// state reliably. Production only increments it.
var activityJobDurableVisits atomic.Int64

// activityJobChunkRecords is how many records one activityJobList chunk
// holds. A page skips whole chunks by their counts and scans within at most
// one, and extending a list copies the chunk list plus each chunk the delta
// changes, so both stay near the page or delta size at any history length.
const activityJobChunkRecords = 512

// activityJobList is a session's job records in durable order: each job at
// the position of its first appearance, a later record for the same job
// replacing it in place. A list never changes once built, so a fold-cache
// entry hands the same list to every concurrent page, and extending it
// shares every chunk the extension does not touch.
type activityJobList struct {
	chunks []*activityJobChunk
	// live overlays the in-memory records of running jobs on the entries
	// above, keyed by job ID. A live record replaces the durable record with
	// the same ID as the list is read, so an overlay costs the live set: the
	// folded history stays shared, unread and uncopied.
	//
	// Sharing the chunks instead of replacing the records is only equivalent
	// to substituting them when the substitution moves nothing a page filters
	// or counts by: a chunk's owner counts and each record's classification
	// are the DURABLE ones, so a page skips and filters by them.
	//
	// On the ordinary path a live entry exists only once its job_started event
	// is durable, so the fold the overlay sits on carries a row for the same
	// job ID and that row's projection class (Type) and owner are the ones the
	// event set — the same ones the live record carries. That is what lets
	// withLive share every chunk.
	//
	// A rebuild can break the premise, and when it does the shared counts are
	// wrong rather than merely stale: truncating a journal past a running job's
	// start leaves its completion as the only durable row, which classifies as
	// an unsupported record owned by nobody while the live record is a shell job
	// owned by its session. withLiveRebuilt substitutes exactly those rows; the
	// caller decides which ones by comparing the two, so nothing here has to
	// scan for them.
	live map[string]*jobstore.JobRecord
}

// withLive returns the list with live overlaid on it. The receiver is
// unchanged, so a fold-cache list can keep being handed to other readers. See
// the live field's doc comment for the invariant that makes sharing the chunks
// equivalent to substituting the records.
//
// The overlay replaces a durable row; it does not add one. That is exact while
// the fold still holds every start record the live set names, which is the
// normal case: liveJobRecords only contains jobs whose start is durable, so the
// fold it overlays carries a row for each. A rebuild can break that premise —
// truncating the journal past a running job's start removes the row while the
// job keeps running — and only the caller knows that happened. It says so by
// calling withLiveStranded instead.
func (l *activityJobList) withLive(live map[string]*jobstore.JobRecord) *activityJobList {
	if l == nil || len(live) == 0 {
		return l
	}
	return &activityJobList{chunks: l.chunks, live: live}
}

// withLiveRebuilt is withLive for a list a rebuild produced, which may have lost
// rows and may hold rows that are no longer the ones the live set describes.
//
// Two questions, both answered by the caller against the fold's own bytes and
// passed in, because establishing either one reads the whole history and a
// refresh that publishes nothing must not pay that twice (see
// liveJobFold.overlayLive):
//
//   - stranded holds the live IDs the list does not name at all. Those records
//     are kept — the job is still running, its process is alive, and the
//     manager's own listing still shows it, so dropping it would report a
//     running job as gone — in their own chunk after the durable ones. The row
//     that placed them earlier is exactly what the rebuild took, so there is no
//     position left to put them back at.
//   - changed holds the rows whose projection the live record alters: a class or
//     an owner that differs from the durable row's. A truncation past a running
//     job's start can leave its completion as the only durable row, and that row
//     classifies as an unsupported record under no owner while the live record
//     is a shell job under one. Sharing that chunk would count and filter the
//     wrong row, so the row is substituted in place and the chunk's counts are
//     corrected with it.
//
// Chunks neither list touches stay shared, so the cost is the live set plus the
// chunks it actually changes.
func (l *activityJobList) withLiveRebuilt(live map[string]*jobstore.JobRecord, stranded map[string]struct{}, changed map[string]activityJobDurableRow) *activityJobList {
	if l == nil || len(live) == 0 {
		return l
	}
	next := &activityJobList{chunks: l.chunks, live: live}
	if len(changed) > 0 {
		// Copy the chunk list and each chunk a substitution writes, and nothing
		// else: the rest keep being shared with the fold's own list.
		//
		// The copy is a real one, not slices.Clip: Clip only caps the length and
		// keeps the same backing array, so writing a chunk pointer through it
		// would write INTO the published fold's own slice. That is two defects at
		// once — the fold every other reader is holding silently changes under
		// them, and two overlays writing at the same index race. A caller that
		// replaces one entry must not be able to change the list it was given.
		chunks := make([]*activityJobChunk, len(l.chunks))
		copy(chunks, l.chunks)
		written := make(map[int]bool, len(changed))
		for jobID, row := range changed {
			if !row.row || jobID == "" {
				continue
			}
			rec := live[jobID]
			if rec == nil {
				continue
			}
			// A row describes a slot in ONE list. A caller that resolved it
			// against a different publication can name a slot this list does not
			// have — including a whole chunk it does not have. Callers scope their
			// lookups to the list they carry (see liveJobFoldResult.durableRowAt),
			// so this is a second lock on a door that is already shut; but the
			// failure it prevents is an out-of-range panic in the middle of
			// rendering a page, which is not a cost worth paying to discover a
			// caller bug. A row that does not fit is skipped, and the live record
			// still overlays through the stranded path.
			if row.chunk < 0 || row.chunk >= len(chunks) {
				continue
			}
			if row.slot < 0 || row.slot >= len(chunks[row.chunk].records) {
				continue
			}
			if !written[row.chunk] {
				base := chunks[row.chunk]
				records := make([]*jobstore.JobRecord, len(base.records))
				copy(records, base.records)
				chunks[row.chunk] = &activityJobChunk{records: records, owners: maps.Clone(base.owners)}
				written[row.chunk] = true
			}
			chunk := chunks[row.chunk]
			chunk.count(chunk.records[row.slot], -1)
			chunk.records[row.slot] = rec
			chunk.count(rec, 1)
		}
		next.chunks = chunks
	}
	if len(stranded) == 0 {
		return next
	}
	kept := make([]*jobstore.JobRecord, 0, len(stranded))
	for jobID, rec := range live {
		if rec == nil || jobID == "" {
			continue
		}
		if _, missing := stranded[jobID]; !missing {
			continue
		}
		kept = append(kept, rec)
	}
	if len(kept) == 0 {
		return next
	}
	// Map iteration is randomized, so order by start time then ID: a page that
	// renders these must render the same order twice, and a continuation's
	// index is only meaningful over a stable sequence.
	slices.SortFunc(kept, func(a, b *jobstore.JobRecord) int {
		if byStart := a.StartedAt.Compare(b.StartedAt); byStart != 0 {
			return byStart
		}
		return strings.Compare(a.JobID, b.JobID)
	})
	chunk := &activityJobChunk{records: kept, owners: make(map[string]activityJobCounts, len(kept))}
	for _, rec := range kept {
		chunk.count(rec, 1)
	}
	// Capped at its length before appending, for the same reason the chunk
	// pointers above are copied rather than Clipped: a slice with spare
	// capacity shares its backing array with the list it came from, so an
	// append writes a stranded chunk INTO the published fold's own slice.
	//
	// Nothing is visible until a slot past the fold's length is written, and
	// then two things go wrong at once: the fold every other reader holds grows
	// a chunk it never built, and a second request over the same fold with a
	// different live snapshot overwrites that slot with its own stranded set.
	// A page built from either list then renders the other request's jobs.
	// Clip caps the capacity at the length, which makes the append below
	// allocate rather than write into the array this slice still shares.
	next.chunks = append(slices.Clip(next.chunks), chunk)
	return next
}

// activityJobDurableRow is what the fold itself says about one live job ID: no
// row at all, or the row's own projection class and owner plus where it sits.
//
// The class and owner are here because a live overlay is only equivalent to
// substituting a record when it moves nothing a page filters or counts by. A
// rebuild can leave a row that is NOT the start record — truncating the journal
// past a running job's start leaves its completion behind — and that row
// classifies as unsupported under an empty owner while the live record is a
// shell job under a real one. The caller compares the two and substitutes the
// rows that disagree.
type activityJobDurableRow struct {
	row   bool
	chunk int
	slot  int
	class activityJobClass
	owner string
}

// durableRows reports what the list's own bytes say, per id in want. It reads
// the fold, never the overlay, so it answers for the journal. Every asked id is
// present in the result, including the ones with no row: "this fold does not
// name it" is an answer a caller memoizes, so it must not be indistinguishable
// from "not asked".
func (l *activityJobList) durableRows(want map[string]struct{}) map[string]activityJobDurableRow {
	found := make(map[string]activityJobDurableRow, len(want))
	if l == nil || len(want) == 0 {
		return found
	}
	for jobID := range want {
		found[jobID] = activityJobDurableRow{}
	}
	remaining := len(want)
	for chunkIndex, chunk := range l.chunks {
		for slot, rec := range chunk.records {
			activityJobDurableVisits.Add(1)
			if rec == nil || rec.JobID == "" {
				continue
			}
			if _, asked := want[rec.JobID]; !asked {
				continue
			}
			if found[rec.JobID].row {
				continue
			}
			found[rec.JobID] = activityJobDurableRow{
				row: true, chunk: chunkIndex, slot: slot,
				class: classifyActivityJob(rec), owner: rec.OwnerSessionID,
			}
			remaining--
			if remaining == 0 {
				return found
			}
		}
	}
	return found
}

// record returns the record to report for a durable record: the live one when
// a running job with the same ID overlays it.
func (l *activityJobList) record(rec *jobstore.JobRecord) *jobstore.JobRecord {
	if len(l.live) == 0 || rec == nil {
		return rec
	}
	if live := l.live[rec.JobID]; live != nil {
		return live
	}
	return rec
}

type activityJobChunk struct {
	records []*jobstore.JobRecord
	// owners counts the chunk's shell and unsupported records by
	// OwnerSessionID, so a page finds where its entries start, and how many
	// unsupported records it reports, without visiting any record.
	owners map[string]activityJobCounts
}

type activityJobCounts struct {
	shell       int
	unsupported int
}

// activityJobClass is how a session's projection treats a job record:
// rendered as a shell entry, reported as unsupported, or ignored.
type activityJobClass int

const (
	activityIgnoredJob activityJobClass = iota
	activityShellJob
	activityUnsupportedJob
)

// classifyActivityJob sorts a record for projection. Retired delegate rows
// are ignored state kept in the journal, not unsupported records.
func classifyActivityJob(rec *jobstore.JobRecord) activityJobClass {
	switch {
	case rec == nil || string(rec.Type) == delegateResourceType:
		return activityIgnoredJob
	case rec.Type == jobstore.JobShell:
		return activityShellJob
	default:
		return activityUnsupportedJob
	}
}

func (c activityJobCounts) of(class activityJobClass) int {
	switch class {
	case activityShellJob:
		return c.shell
	case activityUnsupportedJob:
		return c.unsupported
	default:
		return 0
	}
}

// activityJobOwnedBy reports whether sessionID's projection shows a record
// owned by owner: every record when sessionID is empty, and otherwise the
// session's own records plus records naming no owner.
func activityJobOwnedBy(sessionID, owner string) bool {
	return sessionID == "" || owner == "" || owner == sessionID
}

// counts totals sessionID's shell and unsupported records.
func (l *activityJobList) counts(sessionID string) activityJobCounts {
	var total activityJobCounts
	if l == nil {
		return total
	}
	for _, chunk := range l.chunks {
		owned := chunk.ownedCounts(sessionID)
		total.shell += owned.shell
		total.unsupported += owned.unsupported
	}
	return total
}

// records yields sessionID's records of class in list order, after skipping
// the first skip of them. Whole chunks are skipped by their counts, so a
// late page visits only the records it renders and the rest of one chunk.
func (l *activityJobList) records(sessionID string, class activityJobClass, skip int) iter.Seq[*jobstore.JobRecord] {
	return func(yield func(*jobstore.JobRecord) bool) {
		if l == nil {
			return
		}
		for _, chunk := range l.chunks {
			if inChunk := chunk.ownedCounts(sessionID).of(class); skip >= inChunk {
				skip -= inChunk
				continue
			}
			for _, rec := range chunk.records {
				if classifyActivityJob(rec) != class || !activityJobOwnedBy(sessionID, rec.OwnerSessionID) {
					continue
				}
				if skip > 0 {
					skip--
					continue
				}
				if !yield(l.record(rec)) {
					return
				}
			}
		}
	}
}

// all yields every record in list order.
func (l *activityJobList) all() iter.Seq[*jobstore.JobRecord] {
	return func(yield func(*jobstore.JobRecord) bool) {
		if l == nil {
			return
		}
		for _, chunk := range l.chunks {
			for _, rec := range chunk.records {
				if !yield(l.record(rec)) {
					return
				}
			}
		}
	}
}

func (c *activityJobChunk) ownedCounts(sessionID string) activityJobCounts {
	if sessionID == "" {
		var total activityJobCounts
		for _, counts := range c.owners {
			total.shell += counts.shell
			total.unsupported += counts.unsupported
		}
		return total
	}
	own, unowned := c.owners[sessionID], c.owners[""]
	return activityJobCounts{shell: own.shell + unowned.shell, unsupported: own.unsupported + unowned.unsupported}
}

// count adds delta to rec's tally in the chunk's owner counts.
func (c *activityJobChunk) count(rec *jobstore.JobRecord, delta int) {
	class := classifyActivityJob(rec)
	if class == activityIgnoredJob {
		return
	}
	counts := c.owners[rec.OwnerSessionID]
	switch class {
	case activityShellJob:
		counts.shell += delta
	case activityUnsupportedJob:
		counts.unsupported += delta
	}
	if counts == (activityJobCounts{}) {
		delete(c.owners, rec.OwnerSessionID)
		return
	}
	c.owners[rec.OwnerSessionID] = counts
}

// activityJobListBuilder derives a new list from a base list without
// changing the base: it copies the chunk list once and a chunk the first
// time it writes to it. positions, when not nil, maps each job ID to its
// index and is updated in place, so it must describe the base list and
// belong to this builder alone.
type activityJobListBuilder struct {
	chunks    []*activityJobChunk
	written   map[int]bool // chunks this builder copied or created
	positions map[string]int
	length    int
}

func newActivityJobListBuilder(base *activityJobList, positions map[string]int) *activityJobListBuilder {
	b := &activityJobListBuilder{written: make(map[int]bool), positions: positions}
	if base != nil {
		b.chunks = append([]*activityJobChunk(nil), base.chunks...)
		b.length = base.len()
	}
	return b
}

func (l *activityJobList) len() int {
	if l == nil || len(l.chunks) == 0 {
		return 0
	}
	last := len(l.chunks) - 1
	return last*activityJobChunkRecords + len(l.chunks[last].records)
}

func (b *activityJobListBuilder) list() *activityJobList {
	return &activityJobList{chunks: b.chunks}
}

func (b *activityJobListBuilder) writable(chunkIndex int) *activityJobChunk {
	if !b.written[chunkIndex] {
		base := b.chunks[chunkIndex]
		records := make([]*jobstore.JobRecord, len(base.records), activityJobChunkRecords)
		copy(records, base.records)
		b.chunks[chunkIndex] = &activityJobChunk{records: records, owners: maps.Clone(base.owners)}
		b.written[chunkIndex] = true
	}
	return b.chunks[chunkIndex]
}

// push appends rec, a job not yet in the list.
func (b *activityJobListBuilder) push(rec *jobstore.JobRecord) {
	if b.length%activityJobChunkRecords == 0 {
		b.chunks = append(b.chunks, &activityJobChunk{
			records: make([]*jobstore.JobRecord, 0, activityJobChunkRecords),
			owners:  make(map[string]activityJobCounts),
		})
		b.written[len(b.chunks)-1] = true
	}
	chunk := b.writable(len(b.chunks) - 1)
	chunk.records = append(chunk.records, rec)
	chunk.count(rec, 1)
	if b.positions != nil {
		b.positions[rec.JobID] = b.length
	}
	b.length++
}

// replace stores update(current) at position at. update may change the
// record it is given only when this builder created that record, since the
// base list may still be serving it.
func (b *activityJobListBuilder) replace(at int, update func(*jobstore.JobRecord) *jobstore.JobRecord) {
	chunk := b.writable(at / activityJobChunkRecords)
	slot := at % activityJobChunkRecords
	chunk.count(chunk.records[slot], -1)
	chunk.records[slot] = update(chunk.records[slot])
	chunk.count(chunk.records[slot], 1)
}
