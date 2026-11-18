package agent

import (
	"context"
	"errors"
	"maps"
	"sync"

	"primeradiant.com/evener/agent/internal/jobstore"
)

// liveJobFold is one live session's own jobs.jsonl folded incrementally, owned
// by the job manager that appends to that journal.
//
// It exists because the shared historicalJobFoldCache is the wrong owner for a
// journal that is still being written, on three counts, and each one is a
// property this type has to hold instead:
//
//   - A boundary for what is committed. The store's append is not atomic to a
//     reader: it writes, then fsyncs, then rolls the bytes back if the sync
//     fails. A scan that reads to the file's end can fold a start whose append
//     then fails, showing a job that never committed, and — because a failed
//     start emits no revision — a page minted over that row can pass the
//     revision check and skip a delegate on the next page. The fold therefore
//     reads no further than the store's CommittedJournal length, which the
//     store samples under the lock that serializes every append with its
//     rollback. Bytes past that boundary are not this reader's to decode, so a
//     failed append leaves the previous fold and the previous paging positions
//     exactly as they were.
//
//   - A lifetime for who is still waiting. A foldcache flight runs detached
//     from every caller, so abandoning a cold read still pays its whole I/O
//     and allocation cost. Here the scan runs on a context detached from any
//     single caller — one canceled request must not poison a concurrent one —
//     but canceled once the LAST waiter leaves, so a subscription whose only
//     reader is gone stops paying for a result nobody will read.
//
//   - Retention a busy tree cannot evict. A live tree reads every descendant's
//     journal through the shared cache, whose 256 entries a deep tree fills;
//     the root's fold would be evicted and rescanned whole on the next refresh.
//     This fold has no LRU: it lives as long as the job manager, so descendant
//     reads cannot displace it.
//
// The fold never reorders while the journal it watches is the file it folded.
// It only ever extends the list it already built, so a position a page counted
// is still the same job on the next page: an append cannot move a row. A file
// that was truncated or rewritten in place is the exception, and the store says
// which case that is with CommittedJournalState.Generation — a value that
// identifies the CONTENT, not a flag a reader consumes. A fold is reused only
// while the generation it was built from is still the one being reported, so a
// rebuild that is canceled before publishing leaves every later reader
// comparing against a generation the old fold does not carry. The stale rows
// come back for nobody: they are not "already invalidated once", they are
// simply not the fold for this content.
//
// That rebuild is also the one event that can move a position a page counted:
// truncating the journal removes rows, and rows after them shift toward the
// front. So a published rebuild advances jobEpoch, and the projection carries
// it into a continuation. A token minted against the pre-truncation fold then
// fails the same epoch check a historical session's does, instead of applying
// its index to a list that renumbered under it. An ordinary append leaves the
// epoch alone.
//
// The job manager owns it for the life of the session, and within that
// ownership the fold obeys one rule that makes the rest true: an extension
// runs alone. A scan stays registered from the moment it starts until its
// goroutine exits, so no second extension can begin over the same base — and
// therefore no second reader can touch the position map an extension mutates
// in place. A scan nobody is waiting for still has to reach its exit to clear
// that registration; it does so without publishing, and the next request waits
// for it and retries.
type liveJobFold struct {
	mu sync.Mutex
	// fold is the last completed fold, valid once built. It is kept after a
	// canceled or failed extend so a later request resumes from it.
	fold historicalJobFold
	// lastSize is the committed journal length this fold consumed. A request
	// whose committed boundary has not moved past it has nothing new to fold.
	lastSize int64
	built    bool
	// gen is the journal content generation this fold describes. A fold is
	// reused only for a snapshot reporting the same generation; anything else
	// replaced the bytes it was built from.
	gen uint64
	// epoch counts the idempotent rebuilds that replaced an existing fold, plus
	// the refolds an out-of-order append forces. Only those can renumber entries
	// a page counted, so this is what a continuation records and what a resume
	// compares.
	epoch uint64
	// rebuilt records that the PUBLISHED fold descends from a rebuild, and so may
	// be missing rows an earlier fold held. See liveJobFoldResult.rebuilt.
	//
	// It is sticky: it is set by a rebuild and never cleared by an extension,
	// because an extension only APPENDS to the rebuilt list — the rows the
	// rebuild removed are not among the bytes it reads, so they cannot come
	// back. Clearing it on the next in-order append is what made a running job
	// whose start was truncated away reappear for one page and then vanish
	// again for every page after it. The fallback itself retires a job when it
	// stops running (it leaves the live set) or regains a durable row.
	rebuilt bool
	// scanning is non-nil while one goroutine extends the fold. Waiters join
	// it, so concurrent requests share one read, and the last one to leave
	// cancels it. Only run clears it, and only when that goroutine exits.
	scanning *liveJobScan

	// durableRow answers, per job ID, what the published fold's own bytes say
	// about that job: no row at all, or the row's class, owner and position.
	// Both questions the overlay asks come from one walk of it — is this running
	// job stranded, and does its durable row project the way the live record
	// does.
	//
	// It is keyed by job ID and not by publication because the two questions it
	// gets asked are not the same question. Two requests hold different live
	// sets over one published fold — a job that started between them is in one
	// snapshot and not the other — so a single cached set derived from the first
	// caller's live IDs answers the second caller about jobs it never mentioned.
	// Per ID, every caller gets an answer about its own live set.
	//
	// It is valid for exactly one publication and one live set, recorded beside
	// it. Both halves of that matter:
	//
	//   - A row answer names a SLOT. Applying it to a list that publication did
	//     not produce addresses the wrong row, or one past the end of the list,
	//     which is a panic rather than a wrong answer. So a request carries the
	//     publication its list came from and consults the memo only while that
	//     is still the published one; a request that lost the race resolves
	//     against its own list and writes nothing here.
	//   - The answers are asked about one request's live IDs. Keeping entries
	//     for jobs that have since finished makes every later lookup answer a
	//     question nobody asks, and grows the map with the session's whole job
	//     history. Recording the live set bounds it to the running jobs.
	//
	// A missing key means "not answered for this publication and live set", and
	// a present zero row means "this fold does not name that job".
	durableRow  map[string]activityJobDurableRow
	durableFor  uint64
	durableLive map[string]struct{}
	// published counts publications of this fold. It is the memo's key: a
	// publication can add a durable row for a job that was stranded, so the set
	// of stranded IDs is only valid for the revision that answered it.
	published uint64
}

// liveJobScan is one in-progress extend of a liveJobFold. waiters counts the
// callers still interested in its result; when it reaches zero the scan's
// context is canceled.
type liveJobScan struct {
	done    chan struct{}
	cancel  context.CancelFunc
	waiters int
	err     error
	// validate re-reads the store's committed boundary. run calls it after the
	// fold is computed and BEFORE publishing, so a scan whose bytes the store
	// has since disowned installs nothing. It is the same resample the waiting
	// caller uses; see records.
	validate func() (jobstore.CommittedJournalState, error)
	// stale reports that the scan computed a fold for content the store no
	// longer describes, so it published nothing. A waiter must resample and
	// retry rather than treat the untouched fold as this caller's answer.
	stale bool
	// attempted is the committed boundary this scan was started to reach. A
	// scan that decoded no bytes has not reported "there is nothing beyond
	// here" — it reported "nothing between my start and my own boundary" — so a
	// caller whose boundary is later than this one must extend again rather
	// than accept the short fold. Without this, a cold scan of an empty journal
	// answers every joiner forever, and the first job to commit is missing from
	// a page whose revision says it is there.
	attempted int64
	// rebuild is set when the scan must refold from byte zero rather than
	// extend the published fold: the journal's content generation moved, so
	// the fold's bytes no longer describe the file.
	rebuild bool
	// gen is the content generation this scan reads the file at. A scan may
	// only be joined by a request whose own snapshot reports the same one:
	// a scan started against older content can publish a fold that does not
	// describe the bytes a later request sampled.
	gen uint64
	// abandoned is set when the last waiter left before the read finished.
	// Such a scan's error is its own cancellation, which belongs to the
	// waiters that left, never to a caller that joins later, and its result
	// is not published: an extend that gave up cannot be trusted to describe
	// the base a later request extends. The scan stays registered until it
	// exits, so the next request waits for it rather than extending
	// concurrently; it then starts fresh.
	abandoned bool
}

// liveJobFoldResult is what a read of the fold answers with: the list, the
// paging epoch it belongs to, and whether a rebuild produced it.
//
// rebuilt is the caller-visible half of the epoch rule, and it exists because
// the two callers need different things from the same fold. A rebuild can
// remove rows a running job's live record still names, so only a read that
// follows a rebuild may keep a live job whose durable row is gone; an ordinary
// append never removes one, and letting it keep a stray record would render a
// row that has no place in the order.
type liveJobFoldResult struct {
	jobs    *activityJobList
	epoch   uint64
	rebuilt bool
	// positions is the job-ID index of the list above, or nil when no index
	// describes it. It travels WITH the list because an index answer names a
	// slot: consulting the fold's current index on behalf of a request that
	// holds an older list addresses a slot in a list that caller does not have.
	// A lookup must prove positions.list == jobs before trusting it.
	positions *activityJobPositions
	// published identifies the fold revision these came from. It is how a
	// per-caller overlay (see liveJobFold.strandedOverlay) tells whether the
	// list it memoized is still the one this result describes.
	published uint64
}

// resultLocked snapshots the published fold for a answer. Callers hold f.mu.
func (f *liveJobFold) resultLocked() liveJobFoldResult {
	return liveJobFoldResult{jobs: f.fold.jobs, epoch: f.epoch, rebuilt: f.rebuilt, positions: f.fold.positions, published: f.published}
}

// durableRowAt answers one job ID from the result's own position index instead
// of walking its list, and reports whether it could.
//
// The walk it replaces is why this exists: a refresh after an ordinary append
// has one newly live job to ask about, and walking 200k records to find a row at
// the end of them costs the history for a question the index answers directly.
//
// The answer names a SLOT in result.jobs, so the index must be that list's own.
// Checking positions.list against the list here — rather than against whatever
// the fold holds now — is what makes a stale request safe: it either gets a slot
// in the list it is about to read, or it walks that list.
func (r liveJobFoldResult) durableRowAt(jobID string) (activityJobDurableRow, bool) {
	l := r.jobs
	positions := r.positions
	if l == nil || jobID == "" || positions == nil || positions.list != l {
		return activityJobDurableRow{}, false
	}
	at, known := positions.of[jobID]
	if !known {
		// The index describes this list, so a miss is the answer "this list does
		// not name it" rather than "not found yet".
		return activityJobDurableRow{}, true
	}
	chunkIndex, slot := at/activityJobChunkRecords, at%activityJobChunkRecords
	if chunkIndex >= len(l.chunks) || slot >= len(l.chunks[chunkIndex].records) {
		return activityJobDurableRow{}, false
	}
	rec := l.chunks[chunkIndex].records[slot]
	if rec == nil || rec.JobID != jobID {
		return activityJobDurableRow{}, false
	}
	return activityJobDurableRow{
		row: true, chunk: chunkIndex, slot: slot,
		class: classifyActivityJob(rec), owner: rec.OwnerSessionID,
	}, true
}

// overlayLive returns result.jobs with live applied the way the published fold
// requires: replace-only for an ordinary append, and replace-plus-stranded
// after a rebuild.
//
// The sets it hands the list answer two questions per live job: does the durable
// list name it at all, and does the row it names project the way the live record
// does. Both come from liveJobFold.durableRow, one ID at a time. A refresh that
// publishes nothing re-asks only about IDs the memo has not answered, so it
// costs the live set rather than the history — and each caller asks about its
// own snapshot rather than inheriting another caller's.
func (f *liveJobFold) overlayLive(result liveJobFoldResult, live map[string]*jobstore.JobRecord) *activityJobList {
	if !result.rebuilt || len(live) == 0 {
		return result.jobs.withLive(live)
	}
	rows := f.liveRows(result, live)
	stranded := make(map[string]struct{}, len(live))
	changed := make(map[string]activityJobDurableRow)
	for jobID, rec := range live {
		if jobID == "" || rec == nil {
			continue
		}
		row, asked := rows[jobID]
		if !asked {
			continue
		}
		if !row.row {
			// Presence, not truthiness: a present zero row is the answer "this
			// fold does not name it", and only that answer strands the job.
			stranded[jobID] = struct{}{}
			continue
		}
		if row.class != classifyActivityJob(rec) || row.owner != rec.OwnerSessionID {
			// The durable row projects differently from the live one, so the
			// chunk's counts and the page's class filter would answer for a row
			// this caller is not going to render. Substitute it in place.
			changed[jobID] = row
		}
	}
	return result.jobs.withLiveRebuilt(live, stranded, changed)
}

// liveRows returns the durable-row answer for every job in live, as it applies
// to result's own list.
//
// The answer comes from the memo only when the memo describes this exact
// publication and this exact live set. Anything else — a publication published
// after this request sampled its list, or a snapshot with a different live set —
// is answered by asking result.jobs itself, which is always correct, and is
// never written back. That split is what keeps a slot answer from being applied
// to a list it does not describe, which is a panic rather than a stale row.
func (f *liveJobFold) liveRows(result liveJobFoldResult, live map[string]*jobstore.JobRecord) map[string]activityJobDurableRow {
	want := make(map[string]struct{}, len(live))
	for jobID := range live {
		if jobID != "" {
			want[jobID] = struct{}{}
		}
	}
	if len(want) == 0 {
		return nil
	}

	f.mu.Lock()
	if f.durableRow != nil && f.durableFor == result.published && sameJobIDs(f.durableLive, want) {
		rows := f.durableRow
		f.mu.Unlock()
		return rows
	}
	// The index is NOT immutable while an extension owns it. An extend mutates
	// the fold's position map in place — that is what makes extending cost the
	// delta instead of the history — and it does so outside this lock, between
	// the points where run() takes it. Reading positions.of here without the
	// lock, then, races that write: two overlapping activity reads can terminate
	// the process with a concurrent map read/write.
	//
	// The list-identity check does not help. It proves the answers name slots in
	// THIS list, not that nobody is writing the map they come from.
	//
	// So the index is consulted only while no extension is running, and only
	// under f.mu. A request that arrives mid-extension takes the walk instead —
	// which reads the list, and the list a result carries is immutable by
	// construction. It costs that request the fold, and it costs nothing at all
	// once the extension has exited and the index is quiescent again.
	indexed := make(map[string]activityJobDurableRow, len(want))
	walk := make(map[string]struct{}, len(want))
	if f.scanning == nil {
		for jobID := range want {
			if row, ok := result.durableRowAt(jobID); ok {
				indexed[jobID] = row
				continue
			}
			walk[jobID] = struct{}{}
		}
	} else {
		maps.Copy(walk, want)
	}
	f.mu.Unlock()

	// The walk reads the caller's own list, which nothing mutates. It runs
	// outside the lock so a 200k-record history does not block waiters from
	// joining the next scan.
	rows := indexed
	if len(walk) > 0 {
		maps.Copy(rows, result.jobs.durableRows(walk))
	}

	// Keep the answers only for the publication they describe. A rebuild
	// replaced the list, and an answer about the replaced one names a slot in a
	// list nobody will be handed again.
	f.mu.Lock()
	if f.published == result.published {
		f.durableRow, f.durableFor, f.durableLive = rows, result.published, want
	}
	f.mu.Unlock()
	return rows
}

// sameJobIDs reports whether two ID sets hold the same IDs.
func sameJobIDs(a, b map[string]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for id := range a {
		if _, ok := b[id]; !ok {
			return false
		}
	}
	return true
}

// liveJobRecords folds path through endOffset, extending the last completed
// fold by the events appended since it, and returns the current list. The
// returned list is shared and must not be mutated; a page overlays running jobs
// on it rather than substituting records.
//
// committed is the store's boundary snapshot the caller sampled. A shared scan
// may have started before an append that this caller's boundary includes, so a
// scan finishing short of it is not an answer: the fold is extended by the
// remaining delta before returning. Serving the short fold under the caller's
// newer revision would let a continuation minted from it skip the job it never
// saw.
//
// A confirmed in-place truncation is the one case where a smaller boundary must
// NOT be served from the fold: the rows it holds came from bytes the file no
// longer has. The fold is rebuilt from zero for that request, while a caller
// that merely sampled an older boundary during a normal append still hits it.
// resample, when non-nil, re-reads the store's committed boundary. It is how a
// request whose own snapshot is older than the published fold catches up: that
// snapshot's length is a boundary for content this fold has already replaced,
// so reading the current file up to it would both re-admit bytes the store has
// since disowned and label the result with a generation that is not the file's.
func (f *liveJobFold) records(ctx context.Context, path string, committed jobstore.CommittedJournalState, resample func() (jobstore.CommittedJournalState, error)) (liveJobFoldResult, error) {
	for {
		f.mu.Lock()
		obsolete := f.built && committed.Generation < f.gen
		f.mu.Unlock()
		if !obsolete || resample == nil {
			break
		}
		fresh, err := resample()
		if err != nil {
			return liveJobFoldResult{}, err
		}
		committed = fresh
	}
	endOffset := committed.Size
	// Whether a matching generation and a covering length are evidence that the
	// bytes are unchanged. On a filesystem where the store's own appends cannot
	// move the modification time, they are not: a same-length in-place rewrite
	// leaves both exactly where they were, and reusing the cached prefix would
	// keep serving rows the rewrite removed. The store answers this about
	// itself — see CommittedJournalState.MetadataResolvesWrites — and a false
	// here means this call cannot trust a fold it inherited. It can still trust
	// one it refolded itself, which is why the exception is per generation
	// rather than a blanket "refold every iteration": that would walk the
	// history twice per read for as long as the filesystem stays unreliable.
	//
	// The blindness is narrow, and narrowing it is what keeps the incremental
	// path. A fold whose recorded length DIFFERS from this caller's boundary saw
	// a different file length, which an append explains: the fold is either
	// ahead of the caller or extending to it, and either way the bytes it holds
	// are bytes the store wrote. Only a length that matches exactly is
	// unexplained — nothing moved, and on this filesystem nothing would have
	// moved for a rewrite either. That is the case that has to refold.
	// A generation this call itself rebuilt, and whether it has one at all.
	//
	// The generation is a uint64 whose first published content is 0, so "I
	// refolded at generation 0" and "I have not refolded" are the same value.
	// Testing the value alone therefore treats a fold this request INHERITED on
	// a generation-zero journal as one it built, which is exactly the case a
	// same-length rewrite hides in: the bytes changed, nothing moved to say so,
	// and the inherited fold is served as if this call had just re-read them.
	// The flag is what separates the two, and it is set only where a rebuild
	// actually completed.
	var refoldedAt uint64
	hasRefolded := false
	// inheritedEvidence reports whether a fold this call did NOT build can be
	// trusted for the generation it names — that is, whether anything vouches
	// for the prefix it consumed still being the file's prefix.
	//
	// This call's own rebuild is such a voucher: it read those bytes.
	//
	// So is a filesystem whose metadata resolves writes. There a fold whose
	// recorded length DIFFERS from this caller's boundary saw a different file
	// length, which an append explains — the fold is either ahead of the caller
	// or extending to it, and either way the bytes it holds are bytes the store
	// wrote. A length that matches exactly is the unexplained case and refolds.
	//
	// A filesystem that has already proved it cannot tell one of the store's own
	// writes from no write at all offers no such voucher, and a length
	// difference is NOT one there. That is the case a rewrite hides in: it keeps
	// the length, so the pre-write sample cannot see it, and the following
	// append books a larger length under an unchanged generation. The boundary
	// grew, so an extension from the cached prefix looks ordinary — and the page
	// then serves rows the rewrite removed alongside the new append. Nothing
	// available can show the prefix is intact, so the only honest answer is to
	// rebuild it, and the delta path is kept for every filesystem whose metadata
	// can vouch for a write.
	inheritedEvidence := func(gen uint64) bool {
		if hasRefolded && refoldedAt == gen {
			return true
		}
		return committed.MetadataResolvesWrites
	}
	for {
		f.mu.Lock()
		covers := f.built && committed.Generation == f.gen && endOffset <= f.lastSize
		if covers && inheritedEvidence(f.gen) {
			result := f.resultLocked()
			f.mu.Unlock()
			return result, nil
		}
		// The journal's content is no longer the one this fold consumed, so no
		// part of it describes the file any more. A scan already in flight
		// cannot answer this caller either: it may have read the bytes before
		// they went away. A fold that covers this boundary but whose evidence
		// this call cannot accept is rebuilt for the same reason: the bytes it
		// describes cannot be shown to be these bytes.
		rebuild := !f.built || committed.Generation != f.gen || (covers && !inheritedEvidence(f.gen))
		// Where the fold stood when this iteration's scan began. A scan that
		// leaves it there has nothing left to give this caller, and extending
		// again would read the same bytes forever.
		before := f.lastSize
		scan := f.scanning
		if scan == nil || (rebuild && !scan.rebuild) || scan.gen != committed.Generation {
			if scan != nil {
				// A scan for the old content is in flight. Wait it out like an
				// abandoned one: it must exit before the rebuild may extend.
				done := scan.done
				f.mu.Unlock()
				select {
				case <-ctx.Done():
					return liveJobFoldResult{}, ctx.Err()
				case <-done:
				}
				continue
			}
			scanCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
			scan = &liveJobScan{done: make(chan struct{}), cancel: cancel, attempted: endOffset, rebuild: rebuild, gen: committed.Generation, validate: resample}
			f.scanning = scan
			// The scan reads at most endOffset, a committed length, so the
			// boundary is fixed for this extend: a longer file observed later
			// is a later request's delta, never this one's.
			go f.run(scanCtx, scan, path)
		}
		// A scan the last waiter already left is on its way out. Waiting for
		// its exit is what keeps a second extension from mutating the position
		// map beside it; the wait is this caller's own, so a canceled request
		// leaves here without inheriting that scan's cancellation.
		abandoned := scan.abandoned
		attempted := scan.attempted
		scan.waiters++
		f.mu.Unlock()

		select {
		case <-ctx.Done():
			f.leave(scan)
			return liveJobFoldResult{}, ctx.Err()
		case <-scan.done:
		}
		if abandoned {
			// Its result was never published, so it cannot answer this caller.
			// The fold is free now; extend it from where it actually stands.
			continue
		}
		// A scan that found the journal had changed under it published nothing,
		// so the fold is the one this caller already saw. Reading it as the
		// answer would serve rows for content the store has disowned, so take a
		// fresh boundary and let the next iteration rebuild from it.
		f.mu.Lock()
		staleScan := scan.stale
		f.mu.Unlock()
		if staleScan {
			if resample == nil {
				// Nothing can tell this caller what the current content is, so
				// refusing is the only honest answer.
				return liveJobFoldResult{}, errors.New("jobstore: journal changed while it was being read")
			}
			fresh, resampleErr := resample()
			if resampleErr != nil {
				return liveJobFoldResult{}, resampleErr
			}
			if fresh.Generation == committed.Generation {
				// The store reports the same content the rejected scan read, so
				// the change it saw has already been undone. Accepting the fold
				// as it stands is correct again.
				f.mu.Lock()
				if f.built && fresh.Generation == f.gen && endOffset <= f.lastSize {
					result := f.resultLocked()
					f.mu.Unlock()
					return result, nil
				}
				f.mu.Unlock()
			}
			committed = fresh
			endOffset = fresh.Size
			continue
		}

		// A canceled scan has no waiters left to observe its error: every
		// waiter that was waiting on it left. So any error reaching here is
		// the scan's real failure, and a caller that hid it would serve a
		// stale page forever.
		f.mu.Lock()
		err := scan.err
		result := f.resultLocked()
		covers = f.built && committed.Generation == f.gen && endOffset <= f.lastSize
		if covers && scan.rebuild {
			// This scan read those bytes itself, so the fold it just built is
			// evidence even where a stat would not be.
			refoldedAt, hasRefolded = committed.Generation, true
		}
		covered := covers && inheritedEvidence(f.gen)
		progress := f.built && f.lastSize > before
		staleGeneration := f.gen < committed.Generation
		f.mu.Unlock()
		if err != nil {
			return liveJobFoldResult{}, err
		}
		// The scan read the file at the generation this caller sampled, and the
		// bytes it read may already be gone: a same-length rewrite during the
		// scan changes the cached prefix and the appended rows together, so the
		// list it produced is a combination of two files that never existed as
		// one. The published-fold guard cannot catch that — this scan may be the
		// first to notice the change, and every newer reader is waiting for it to
		// exit. Only the store can say whether the content is still the content,
		// so ask it before answering, and retry against what it now reports.
		if resample != nil {
			fresh, resampleErr := resample()
			if resampleErr != nil {
				return liveJobFoldResult{}, resampleErr
			}
			if fresh.Generation != committed.Generation {
				// The bytes this scan read are not the ones the store reports any
				// more, so its result describes nothing and its boundary is not a
				// place to stop at either: the replacement may be longer. Take both
				// from the new snapshot before retrying, or the retry scans the old
				// length again and withholds the rows that landed past it.
				committed = fresh
				endOffset = fresh.Size
				continue
			}
		}
		if covered {
			return result, nil
		}
		if !progress && endOffset <= attempted && !staleGeneration {
			// This scan actually tried to reach this caller's boundary and
			// decoded nothing to fold: a trailing line the boundary cut short,
			// which the next read picks up once it is committed whole.
			// Extending again would read the same bytes forever. A scan that
			// attempted an EARLIER boundary says nothing about this one — which
			// includes a rebuild that refolded from zero while the journal grew
			// past its own boundary: it answered for the bytes it was given, not
			// for the append its caller sampled.
			return result, nil
		}
	}
}

// leave drops one waiter from scan and cancels the scan when none remain, so an
// abandoned cold read stops instead of paying its whole I/O cost. The completed
// fold survives for later requests either way.
//
// It does not clear the fold's in-flight marker: the extension is still
// running, and clearing the marker here is what let a replacement extension
// start over the same base and mutate the same position map. The scan stays
// registered until run exits; see liveJobFold's comment.
func (f *liveJobFold) leave(scan *liveJobScan) {
	f.mu.Lock()
	if scan.waiters > 0 {
		scan.waiters--
	}
	last := scan.waiters == 0 && !scan.abandoned
	scan.abandoned = scan.abandoned || last
	f.mu.Unlock()
	if last {
		scan.cancel()
	}
}

// run extends the fold under endOffset and publishes the result. A canceled
// scan publishes nothing, leaving the last completed fold in place; the next
// request starts a fresh extend from that fold's own offset.
func (f *liveJobFold) run(ctx context.Context, scan *liveJobScan, path string) {
	f.mu.Lock()
	prior, fromOffset := f.fold, int64(0)
	if f.built && !scan.rebuild && scan.attempted >= f.lastSize {
		fromOffset = f.lastSize
	} else if f.built {
		// Either the committed length moved backwards or the store confirmed a
		// truncation: the journal was rewritten to something this fold's bytes
		// never described. Refold from zero rather than resume past the end of
		// content this fold never read.
		//
		// The third case is a prefix nothing vouched for. Acceptance refuses to
		// SERVE such a fold, but a caller that was willing to extend it would
		// still publish, and publication is what every other request reads: the
		// next one inherits a list built from a prefix this store cannot show
		// is the file's, and a rewrite that kept the length is then served as
		// though it had been read. Refolding here costs this scan the history
		// once, on a filesystem that already proved it cannot tell a write from
		// no write, and it makes the published fold's own bytes its evidence.
		prior = historicalJobFold{}
	}
	f.mu.Unlock()

	next, toOffset, reordered, err := extendHistoricalJobFoldAt(ctx, path, fromOffset, prior, jobstore.ScanLimits{EndOffset: scan.attempted, Bounded: true})

	// Validate BEFORE this fold becomes cache-visible.
	//
	// A rewrite during the scan replaces the bytes this extension read. The
	// extension would install a list built from the cached prefix and the
	// rewritten suffix — a combination that never existed as one file — and the
	// next request, which sampled the old generation, would hit that cache and
	// serve it. Checking only against the last published fold cannot catch this:
	// this scan may be the first to notice the change, and every newer reader is
	// waiting for it to exit. So ask the store what its content is now, and on a
	// change publish nothing: the waiting caller resamples and retries against
	// the current boundary, and the last validated fold stays in place until a
	// scan for the current content replaces it.
	// Read the abandonment state under f.mu. leave() writes it there, and the
	// locked publication check much further down does not protect this earlier
	// read: between the two, a waiter can leave and mark the scan abandoned, and
	// an unsynchronized read here is a race on a field two goroutines touch.
	f.mu.Lock()
	abandonedBeforeValidation := scan.abandoned
	f.mu.Unlock()
	if err == nil && !abandonedBeforeValidation && scan.validate != nil {
		fresh, resampleErr := scan.validate()
		switch {
		case resampleErr != nil:
			err = resampleErr
		case fresh.Generation != scan.gen:
			// The bytes are not the ones this scan read. Say so explicitly:
			// the fold below is left untouched, and a waiter must not read
			// that as "nothing new to fold".
			scan.stale = true
		}
	}

	f.mu.Lock()
	superseded := f.built && scan.gen < f.gen
	// A publication may only be replaced by one at least as new, and a fold is
	// only valid for the boundary it was built from.
	//
	// A request that sampled an OLDER boundary can arrive here after a request
	// with a later one already published at the same generation. It then holds a
	// shorter list — it read fewer bytes — and installing it puts back the rows
	// the longer publication had already dropped or added, under a generation
	// that says the content is the same. Its own continuation would be fine; the
	// continuation minted over the longer fold, which other pages are holding,
	// now addresses a list that no longer exists.
	//
	// The boundary is the scan's own attempted offset, and the fold records the
	// offset it reaches. Comparing them is enough: an equal boundary describes
	// the same bytes, and a later one is the ordinary append that should win.
	shorter := f.built && scan.gen == f.gen && toOffset < f.lastSize
	if err == nil && !scan.abandoned && !superseded && !scan.stale && !shorter {
		// An abandoned extend publishes nothing: it stopped somewhere its
		// cancellation chose, and this fold only advances by deltas it can
		// state completely.
		//
		// A superseded scan publishes nothing either. This scan sampled a
		// boundary, and while it was reading another request repaired the journal
		// and published a fold for newer content. Publishing this one now would
		// put the older rows back — and it read them with its own obsolete
		// boundary, which is exactly a page past an append it never saw.
		if scan.rebuild && f.built {
			// A rebuild replaced a fold that existed. Truncating a journal
			// removes rows and pulls the ones after them toward the front, so
			// every position a page counted past the cut moved. Advance the
			// epoch: a continuation minted over the old list must be refused,
			// not resumed at an index that now names a different job.
			//
			// The epoch moves for the PAGING consequence, not for the rebuild.
			// A rebuild can also be a verification: on a filesystem whose
			// timestamps cannot vouch for the content, a read at an unchanged
			// length refolds the same bytes to be sure they are the same bytes.
			// Refolding proves nothing moved, so nothing a page counted past
			// has moved either — and advancing the epoch there makes a
			// continuation invalidate itself, because fetching the next page
			// performs the same verification that refuses the previous one.
			// Every page after the first would be rejected for as long as the
			// filesystem stayed unreliable.
			//
			// So the question is what the rebuild produced, not that it ran:
			// same rows in the same order means the same pagination. The
			// fold's own order fingerprint answers exactly that, and it is
			// maintained by every rebuild and every extension, so it describes
			// the list a page was built over rather than a recount of it.
			if f.fold.orderCount != next.orderCount || f.fold.orderHash != next.orderHash || f.fold.pagingSum != next.pagingSum {
				f.epoch++
			}
		} else if reordered {
			// An extension that had to refold from zero because a delta carried
			// an event older than one already folded. That inserts a row AHEAD of
			// rows earlier pages already rendered, which is the same paging
			// damage a truncation does by a different route: a continuation's
			// index now names a different job. An ordered append moves nothing.
			f.epoch++
		}
		f.fold = next
		f.lastSize = toOffset
		f.gen = scan.gen
		// Sticky: an extension appends to the list a rebuild produced, so it
		// still descends from that rebuild. See the field's comment.
		f.rebuilt = f.rebuilt || scan.rebuild
		// Every publication falsifies every memo entry, because an entry names a
		// slot in the list that was published before this one. Dropping the memo
		// here is the whole invalidation rule: the next reader re-answers against
		// the list it now holds, and a reader still holding the old list answers
		// against that one itself. See durableRow.
		f.durableRow, f.durableFor, f.durableLive = nil, 0, nil
		f.published++
		f.built = true
	} else if !scan.abandoned {
		scan.err = err
	}
	// Clear the in-flight marker here, at exit, and nowhere earlier: while this
	// goroutine could still be extending the fold, the marker is what stops a
	// second extension from running beside it.
	if f.scanning == scan {
		f.scanning = nil
	}
	f.mu.Unlock()

	// The read is over, so releasing the scan's context here costs nothing
	// and does not affect a waiter that has not yet observed done: the
	// result is already published above.
	scan.cancel()
	close(scan.done)
}
