package transcript

import (
	"sync"
)

// A resume scan decodes every recorded entry before the daemon that owns the
// transcript can announce itself, and that decode is pure CPU over lines that
// do not depend on each other. On a 510 MB / 38,371-entry transcript it is 85%
// of `evener serve --resume`'s startup: 9.15s of 10.75s sampled inside
// scanForResume's DecodeEntry call, of which 7.20s is the strict decode and
// 1.43s the record-boundary peek. The scan therefore hands its entry lines to a
// bounded pool of decode workers and keeps file order and the first file-order
// error itself, so none of the sequential scan's guarantees move.

// resumeDecodeMaxWorkers caps the decode pool. The decode is memory-bandwidth
// bound well before a wide host's core count, and each worker holds one raw
// entry line, so this cap bounds what the pool costs the rest of the process.
const resumeDecodeMaxWorkers = 16

// resumeDecodeInFlightBytes bounds the raw entry lines the scan holds for
// decode at once, so a pool of resumeDecodeMaxWorkers cannot hold that many
// maximum-size records. A single line larger than the whole budget is admitted
// once the budget is otherwise empty (see resumeDecodeBudget.acquire), because
// DefaultMaxLineBytes allows one 128 MB record.
const resumeDecodeInFlightBytes = 64 << 20

// resumeDecodeSeam, when non-nil, is told each time a worker enters (true) and
// leaves (false) the decode of one line. Test seam only, nil in production: it
// is what pins that the scan's decode really is concurrent, and that the scan
// joins its workers before it returns.
var resumeDecodeSeam func(entering bool)

// resumeDecodeWorkerCount is how many decode workers a scan runs on a process
// with gomaxprocs usable cores.
func resumeDecodeWorkerCount(gomaxprocs int) int {
	if gomaxprocs < 1 {
		gomaxprocs = 1
	}
	return min(gomaxprocs, resumeDecodeMaxWorkers)
}

// resumeDecodeBudget charges raw entry-line bytes until their decode finishes.
type resumeDecodeBudget struct {
	mu   sync.Mutex
	cond *sync.Cond
	used int64
}

func newResumeDecodeBudget() *resumeDecodeBudget {
	b := &resumeDecodeBudget{}
	b.cond = sync.NewCond(&b.mu)
	return b
}

// acquire charges n bytes, waiting while the budget is full. A line that alone
// exceeds the budget is admitted once the budget is otherwise empty, so one
// maximum-size record always proceeds instead of deadlocking the scan.
func (b *resumeDecodeBudget) acquire(n int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for b.used > 0 && b.used+n > resumeDecodeInFlightBytes {
		b.cond.Wait()
	}
	b.used += n
}

func (b *resumeDecodeBudget) release(n int64) {
	b.mu.Lock()
	b.used -= n
	b.cond.Broadcast()
	b.mu.Unlock()
}

// resumeDecodeJob is one entry line, at the file-order index the scan read it
// at, handed to a worker.
type resumeDecodeJob struct {
	index int
	line  []byte
}

// resumeDecodeResult is one decoded entry and the index it belongs at.
type resumeDecodeResult struct {
	index int
	entry Entry
	err   error
}

// resumeDecoder decodes a resume scan's entry lines on a bounded pool of
// workers. The scanner submits lines in file order and finish returns the
// entries in that order with the first file-order error; neither property
// depends on which worker finished first, and no worker outlives finish.
type resumeDecoder struct {
	jobs      chan resumeDecodeJob
	results   chan resumeDecodeResult
	budget    *resumeDecodeBudget
	workers   sync.WaitGroup
	collected chan struct{}
	closed    sync.Once

	// entries and firstErr belong to the collector alone. finish reads them
	// after collected is closed, which orders that read after every write.
	entries  []Entry
	firstErr error

	// next is the submitting scanner's alone.
	next int
}

func newResumeDecoder(workers int) *resumeDecoder {
	if workers < 1 {
		workers = 1
	}
	d := &resumeDecoder{
		jobs:    make(chan resumeDecodeJob, workers),
		results: make(chan resumeDecodeResult, workers),
		budget:  newResumeDecodeBudget(),
		// entries is non-nil even for a header-only transcript: the
		// delegate-attention fold keys on nilness to decide whether it can fold
		// in memory or must re-read the file.
		entries:   make([]Entry, 0),
		collected: make(chan struct{}),
	}
	d.workers.Add(workers)
	for range workers {
		go d.work()
	}
	go d.collect()
	return d
}

// submit hands line, the next entry line in file order, to a worker. The
// caller must not touch line afterwards.
func (d *resumeDecoder) submit(line []byte) {
	d.budget.acquire(int64(len(line)))
	d.jobs <- resumeDecodeJob{index: d.next, line: line}
	d.next++
}

// finish closes the pool, waits for every worker and the collector, and
// returns the decoded entries in file order with the first file-order error.
// It is safe to call only after the last submit.
func (d *resumeDecoder) finish() ([]Entry, error) {
	d.closed.Do(func() {
		close(d.jobs)
		d.workers.Wait()
		close(d.results)
		<-d.collected
	})
	return d.entries, d.firstErr
}

func (d *resumeDecoder) work() {
	defer d.workers.Done()
	for job := range d.jobs {
		if resumeDecodeSeam != nil {
			resumeDecodeSeam(true)
		}
		entry, err := DecodeEntry(job.line)
		if resumeDecodeSeam != nil {
			resumeDecodeSeam(false)
		}
		// The raw line is free once it is decoded, whatever the decode said.
		d.budget.release(int64(len(job.line)))
		d.results <- resumeDecodeResult{index: job.index, entry: entry, err: err}
	}
}

// collect applies results in file order. pending holds the results that
// arrived before their predecessor; it holds entries the scan's own slice
// would hold anyway, and only as many as were submitted.
func (d *resumeDecoder) collect() {
	defer close(d.collected)
	pending := make(map[int]resumeDecodeResult)
	next := 0
	for res := range d.results {
		pending[res.index] = res
		for {
			ready, ok := pending[next]
			if !ok {
				break
			}
			delete(pending, next)
			d.entries = append(d.entries, ready.entry)
			if ready.err != nil && d.firstErr == nil {
				d.firstErr = ready.err
			}
			next++
		}
	}
}
