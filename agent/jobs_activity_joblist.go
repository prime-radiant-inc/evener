package agent

import (
	"iter"
	"maps"

	"primeradiant.com/evener/agent/internal/jobstore"
)

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

// newActivityJobList orders records the way a fold does: by first
// appearance of each job ID, a later record for an ID replacing the earlier
// one in place. Records without a job ID are dropped. The list holds the
// records themselves, so the caller hands over records it will not mutate.
func newActivityJobList(records []*jobstore.JobRecord) *activityJobList {
	b := newActivityJobListBuilder(nil, make(map[string]int, len(records)))
	for _, rec := range records {
		if rec == nil || rec.JobID == "" {
			continue
		}
		if at, seen := b.positions[rec.JobID]; seen {
			b.replace(at, func(*jobstore.JobRecord) *jobstore.JobRecord { return rec })
			continue
		}
		b.push(rec)
	}
	return b.list()
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
				if !yield(rec) {
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
				if !yield(rec) {
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
// time it writes to it. positions maps each job ID to its index and is
// updated in place, so it must describe the base list and belong to this
// builder alone.
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
	b.positions[rec.JobID] = b.length
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
