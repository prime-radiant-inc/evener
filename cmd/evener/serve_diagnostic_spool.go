package main

import (
	"io"
	"time"
)

// writerShutdownJoinBudget bounds how long a shutdown waits for a diagnostic
// writer's goroutine to finish a write already in flight. It is short on
// purpose: it exists to let accepted lines reach a sink that is still draining,
// not to give a stalled sink more time. A write stuck on a full pipe cannot be
// recalled, so the join gives up rather than holding shutdown behind the sink
// it is leaving.
const writerShutdownJoinBudget = 250 * time.Millisecond

// serveDiagnosticSpoolCapacity is how many lines the spool holds for a sink
// that has stopped draining. A burst is normal -- a failed turn reports itself
// and the shutdown path reports what it is doing -- so one slot would refuse
// the report that matters behind a still-running write. It is a bound, not a
// queue: an offer is never allowed to block, and a sink that never drains costs
// the daemon nothing beyond these slots.
const serveDiagnosticSpoolCapacity = 8

// serveDiagnosticSpool carries the daemon's own [serve] diagnostic lines onto a
// sink the daemon does not own without letting that sink stall the daemon.
//
// A daemon's stderr is a pipe to whatever started it. When that reader stops
// draining, the pipe fills and every write blocks. Written synchronously, a
// diagnostic parks whichever goroutine emitted it -- and the two that matter
// most are the input loop, the only goroutine that runs turns, and the teardown
// that joins it. Shutdown then waits on a log line. The spool takes lines on a
// non-blocking send, writes them from one worker, and drops an offer when its
// slots are full, so the invariant holds: whatever goroutine processes input
// never waits on a diagnostic sink. Losing a repeat of a report is cheaper than
// stalling on the report of one.
type serveDiagnosticSpool struct {
	out   io.Writer
	lines chan []byte
	stop  chan struct{}
	done  chan struct{}
}

// newServeDiagnosticSpool starts the spool's one writer goroutine, so accepted
// lines keep their order relative to one another.
func newServeDiagnosticSpool(out io.Writer) *serveDiagnosticSpool {
	p := &serveDiagnosticSpool{
		out:   out,
		lines: make(chan []byte, serveDiagnosticSpoolCapacity),
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
	go p.run()
	return p
}

func (p *serveDiagnosticSpool) run() {
	defer close(p.done)
	for {
		select {
		case line := <-p.lines:
			p.write(line)
		case <-p.stop:
			// A stop can arrive with accepted lines still queued. Write them
			// before exiting: they were taken from their producers, and a
			// writable sink would otherwise lose the last lines of a shutdown.
			for {
				select {
				case line := <-p.lines:
					p.write(line)
				default:
					return
				}
			}
		}
	}
}

func (p *serveDiagnosticSpool) write(line []byte) {
	_, _ = p.out.Write(line)
}

// Writer adapts the spool to io.Writer for serveLogf. A write never blocks: the
// bytes are copied, offered, and reported as written whether or not the spool
// had room, because the caller is emitting a diagnostic and has nothing useful
// to do with a refusal.
func (p *serveDiagnosticSpool) Writer() io.Writer {
	return serveDiagnosticWriter{spool: p}
}

type serveDiagnosticWriter struct {
	spool *serveDiagnosticSpool
}

func (w serveDiagnosticWriter) Write(b []byte) (int, error) {
	select {
	case w.spool.lines <- append([]byte(nil), b...):
	default:
		// Full behind a stalled worker: the line is dropped.
	}
	return len(b), nil
}

// Close stops the spool, lets its worker write what was already accepted, and
// joins it within writerShutdownJoinBudget.
//
// The bounded wait is what keeps the join from becoming a new dependency: a
// responsive sink gets its shutdown tail, and a stalled one does not get to
// hold the exiting process. The channel is never closed, so a producer that
// still holds the writer after Close gets a dropped line rather than a panic.
func (p *serveDiagnosticSpool) Close() {
	close(p.stop)
	select {
	case <-p.done:
	case <-time.After(writerShutdownJoinBudget):
	}
}
