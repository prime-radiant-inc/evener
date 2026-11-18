package main

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"primeradiant.com/evener/agent"
)

// serveLine is one parsed componentlog record from the serve component: its
// message plus the session and instant fields every line must carry.
type serveLine struct {
	message string
	at      string
	session string
}

// serveLogStampFor renders an instant the way serveLogAt stamps it, so a test
// can compare a parsed line's instant against a time it chose.
func serveLogStampFor(at time.Time) string {
	return at.UTC().Format(serveLogTimeLayout)
}

// parseServeLine reads the fields the stamper emits. A line that does not
// carry both a session and an instant, or that was not tagged by the serve
// component, fails the test.
func parseServeLine(t *testing.T, line string) serveLine {
	t.Helper()
	rest, ok := strings.CutPrefix(strings.TrimSuffix(line, "\n"), "[serve] ")
	if !ok {
		t.Fatalf("unexpected serve output %q: not a [serve] line", line)
	}
	at := strings.LastIndex(rest, " at=")
	if at < 0 {
		t.Fatalf("unexpected serve output %q: no instant field", line)
	}
	attrs := map[string]string{}
	for part := range strings.FieldsSeq(rest[at+1:]) {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			t.Fatalf("unexpected serve output %q: malformed field %q", line, part)
		}
		attrs[key] = value
	}
	if attrs["session"] == "" {
		t.Fatalf("unexpected serve output %q: no session field", line)
	}
	return serveLine{message: rest[:at], at: attrs["at"], session: attrs["session"]}
}

// A daemon's [serve] lines land in whatever sink the process that launched it
// chose, and on a hub that sink is shared with every other daemon. A line that
// names no session belongs to whichever daemon wrote last: "[serve] error:
// model returned empty response after 3 retries" sat as the last line of a hub
// log directly under a "[serve] listening…" line for a DIFFERENT session and
// read as the smoking gun for the session under investigation; disproving it
// took cross-referencing API logs across all three live sessions sharing that
// hub (kata vca1). The daemon is the process that authoritatively knows both
// the session and the moment, so it stamps them itself and every sink is
// correct.
func TestServeLogStampsSessionAndTime(t *testing.T) {
	t.Parallel()
	const (
		session = "033z7k96Nj0LLiLImAqa9s"
		listen  = "127.0.0.1:52341"
	)
	tests := []struct {
		name   string
		at     time.Time
		wantAt string
	}{
		{
			name:   "session and instant accompany the daemon's own words",
			at:     time.Date(2026, 7, 31, 18, 26, 56, 176_000_000, time.UTC),
			wantAt: "2026-07-31T18:26:56.176Z",
		},
		{
			name:   "a whole second still spells its milliseconds",
			at:     time.Date(2026, 7, 31, 18, 26, 56, 0, time.UTC),
			wantAt: "2026-07-31T18:26:56.000Z",
		},
		{
			name:   "a trailing-zero millisecond keeps the column's width",
			at:     time.Date(2026, 7, 31, 18, 26, 56, 100_000_000, time.UTC),
			wantAt: "2026-07-31T18:26:56.100Z",
		},
		{
			name:   "sub-millisecond detail is dropped, never rounded up",
			at:     time.Date(2026, 7, 31, 18, 26, 56, 176_999_999, time.UTC),
			wantAt: "2026-07-31T18:26:56.176Z",
		},
		{
			name:   "a clock in another zone reports the same instant in UTC",
			at:     time.Date(2026, 8, 1, 3, 26, 56, 176_000_000, time.FixedZone("JST", 9*60*60)),
			wantAt: "2026-07-31T18:26:56.176Z",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			serveLogAt(&out, tc.at, session, "listening on %s", listen)
			line := parseServeLine(t, out.String())
			if line.at != tc.wantAt {
				t.Errorf("instant = %q, want %q", line.at, tc.wantAt)
			}
			if line.session != session {
				t.Errorf("session = %q, want %q", line.session, session)
			}
			if !strings.Contains(line.message, listen) {
				t.Errorf("message = %q, want the daemon's listening diagnostic naming %s", line.message, listen)
			}
		})
	}
}

// Two lines stamped a millisecond apart have to spell the instant in the same
// number of columns, or an operator scanning a log for the moment something
// happened is reading a ragged edge. RFC3339Nano trims trailing zeros and
// would ripple it.
func TestServeLogStampIsFixedWidth(t *testing.T) {
	t.Parallel()
	instants := []time.Time{
		time.Date(2026, 7, 31, 18, 26, 56, 0, time.UTC),
		time.Date(2026, 7, 31, 18, 26, 56, 1_000_000, time.UTC),
		time.Date(2026, 7, 31, 18, 26, 56, 100_000_000, time.UTC),
		time.Date(2026, 12, 31, 23, 59, 59, 999_000_000, time.UTC),
	}
	width := -1
	for _, at := range instants {
		var out bytes.Buffer
		serveLogAt(&out, at, "s", "x")
		line := parseServeLine(t, out.String())
		got := len(line.at)
		if width == -1 {
			width = got
			continue
		}
		if got != width {
			t.Fatalf("instant %q is %d columns, want %d: the timestamp column is ragged", line.at, got, width)
		}
	}
}

// serveLogSources are the daemon files that speak the "[serve]" vocabulary.
// Every line they emit has to carry its session and its instant, so none of
// them may write the bare prefix directly: the whole point of stamping at the
// source is that there is no unattributed path out.
var serveLogSources = []string{"serve.go", "serve_log.go"}

// A stamped line is only worth having if every [serve] line is one. The
// stamper is one call away from being bypassed by an ordinary Fprintf, and a
// bypassed line reads exactly like the anonymous ones that cost a whole
// cross-session investigation to disprove (kata vca1). The literal prefix is
// the tell, so this refuses to let it back into the source.
func TestEveryServeLineGoesThroughTheStamper(t *testing.T) {
	t.Parallel()
	for _, name := range serveLogSources {
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if before, _, found := bytes.Cut(src, []byte(`"[serve`)); found {
			line := 1 + bytes.Count(before, []byte("\n"))
			t.Fatalf("%s:%d writes a bare \"[serve]\" prefix: route it through serveLogf so it carries its session and instant", name, line)
		}
	}
}

// stalledSink is a daemon output sink whose reader has stopped draining: a
// write blocks until release closes, exactly as a full pipe does.
type stalledSink struct {
	release chan struct{}
	writes  atomic.Int64
	// entered is closed by the first write once it is inside the sink and
	// about to block, so a test can await entry instead of polling a counter.
	entered sync.Once
	onEnter chan struct{}
}

func (s *stalledSink) Write(p []byte) (int, error) {
	s.writes.Add(1)
	if s.onEnter != nil {
		s.entered.Do(func() { close(s.onEnter) })
	}
	<-s.release
	return len(p), nil
}

// TestServeDiagnosticSpoolDoesNotBlockOnAStalledSink is the input loop's
// liveness property. The loop is the only goroutine that runs turns, and
// shutdown joins it, so a diagnostic it emits may never wait on the daemon's
// output reader: a Stop in flight would then be stuck behind a log write that
// no one is draining, on both the turn path and the exit.
//
// The two halves are the before and after. The first writes straight to the
// stalled sink, which is what the loop used to do, and it does not return. The
// second emits through the spool, which does.
func TestServeDiagnosticSpoolDoesNotBlockOnAStalledSink(t *testing.T) {
	// In a bubble: synctest.Wait returns once every goroutine here is blocked,
	// so a write that regressed to blocking fails the assertion below directly
	// instead of hanging the suite until the timeout releases the sink.
	synctest.Test(t, func(t *testing.T) {
		sink := &stalledSink{release: make(chan struct{}), onEnter: make(chan struct{})}

		direct := make(chan struct{})
		go func() {
			defer close(direct)
			serveLogf(sink, "session-1", "diagnostic straight to the sink")
		}()
		// The sink closes onEnter as it is about to block, so awaiting that is
		// awaiting the write's entry rather than polling for it.
		<-sink.onEnter
		select {
		case <-direct:
			t.Fatal("a direct write to a stalled sink returned; this test cannot show what the input loop used to wait on")
		default:
		}

		spool := newServeDiagnosticSpool(sink)
		t.Cleanup(spool.Close)
		// The worker takes the first line and blocks in the write, as the direct
		// write above is still blocked. Everything after it must not wait.
		spool.Logf("session-1", "first stalled diagnostic")
		synctest.Wait()
		if got := sink.writes.Load(); got != 2 {
			t.Fatalf("the sink saw %d writes, want 2: the worker did not reach the stalled write, so the emits below prove nothing", got)
		}

		emitted := make(chan struct{})
		go func() {
			defer close(emitted)
			for range 100 {
				spool.Logf("session-1", "diagnostic %d behind a stalled sink", 1)
			}
		}()
		synctest.Wait()
		select {
		case <-emitted:
		default:
			t.Fatal("emitting 100 diagnostics did not finish while the sink was stalled: the input loop would stall with it, and shutdown would wait on that loop")
		}
		// The sink is still stalled: release has not closed, so the worker
		// cannot have finished its write. That is what makes the emits above
		// meaningful -- they completed against a sink that is still stuck.
		if got := sink.writes.Load(); got != 2 {
			t.Fatalf("the sink saw %d writes while 100 diagnostics were enqueued, want 2: the worker drained the spool instead of blocking in the first write", got)
		}
		close(sink.release)
	})
}

// gatedSink records the instant its first write began blocking and releases
// only when told, so a test can hold a line in the sink while later events
// happen.
type gatedSink struct {
	release chan struct{}
	entered chan struct{}
	lines   chan string
	once    atomic.Bool
}

func (s *gatedSink) Write(p []byte) (int, error) {
	if s.once.CompareAndSwap(false, true) {
		close(s.entered)
		<-s.release
	}
	s.lines <- string(p)
	return len(p), nil
}

// A diagnostic waits in the spool while the sink is blocked on the line before
// it, and the stamp it carries has to be the moment it was emitted. Stamping in
// the worker would place a stand-down after events that happened while it
// waited, which is the ordering the instant exists to establish.
//
// The line is offered a stamp well before the write that carries it runs, and
// the assertion reads the stamp off the sink: an offer that was refused is not a
// report, so the test offers again once the write can proceed.
func TestServeDiagnosticSpoolStampsWhenTheLineIsEmitted(t *testing.T) {
	t.Parallel()
	sink := &gatedSink{
		release: make(chan struct{}),
		entered: make(chan struct{}),
		lines:   make(chan string, 4),
	}
	spool := newServeDiagnosticSpool(sink)
	defer spool.Close()

	emitted := time.Date(2026, 7, 31, 18, 26, 56, 0, time.UTC)
	later := time.Date(2026, 7, 31, 18, 27, 30, 0, time.UTC)

	// The line is stamped here, and the write that carries it happens after the
	// sink is released -- a different instant, so the two cannot be confused.
	if !spool.logAt(emitted, "session-1", "the line whose instant is under test") {
		t.Fatal("the offer was refused with an idle spool")
	}
	<-sink.entered
	close(sink.release)

	line := parseServeLine(t, <-sink.lines)
	if line.at != serveLogStampFor(emitted) {
		t.Fatalf("the line's instant = %q, want %q: it was stamped when the write ran, not when it was emitted", line.at, serveLogStampFor(emitted))
	}
	if line.at == serveLogStampFor(later) {
		t.Fatalf("the line's instant = %q: the write's instant, not the emission's", line.at)
	}
}

// TestTurnOutcomeRecordsNeverBlockOnAStalledSink is the input loop's exit
// property, for the records it owes for a turn.
//
// A turn error written straight to the sink can fill the last room in a stalled
// pipe, and the next turn's record then blocks the only goroutine that runs
// turns -- which shutdown joins, so the daemon cannot stop either. That is the
// same stall the stand-down spool exists to remove, one turn later, so both
// records take the same path.
//
// What is asserted is that the records complete, not that they appear: a
// daemon that logs and cannot shut down is the failure this exists for.
func TestTurnOutcomeRecordsNeverBlockOnAStalledSink(t *testing.T) {
	// In a bubble: the clock is fake and synctest.Wait returns once every
	// goroutine here is blocked, so a record that regressed to a synchronous
	// write fails this assertion directly instead of hanging the suite until
	// the test timeout releases the sink.
	synctest.Test(t, func(t *testing.T) {
		sink := &stalledSink{release: make(chan struct{}), onEnter: make(chan struct{})}
		diagnostics := newServeDiagnosticSpool(sink)
		t.Cleanup(diagnostics.Close)

		// The sink is stalled from the first write, which is the state a full
		// pipe puts the process in.
		diagnostics.Logf("session-1", "the line the sink is stuck on")
		<-sink.onEnter

		done := make(chan struct{})
		go func() {
			defer close(done)
			// A turn that failed, then a turn whose runnable input stood down:
			// the two records the loop emits on this goroutine's behalf.
			reportTurnOutcome(diagnostics, "session-1", errors.New("injected: turn failed"), true, agent.PendingInputSteeringParked, nil)
			reportTurnOutcome(diagnostics, "session-1", nil, true, agent.PendingInputStopHeld, nil)
			reportTurnOutcome(diagnostics, "session-1", errors.New("injected: another turn failed"), false, agent.PendingInputNoOutcome, nil)
		}()
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("the loop's records did not finish while the sink was stalled: a synchronous write would block it here")
		}
		// The sink never resumed, so a record that had gone straight to it
		// would still be blocked.
		if got := sink.writes.Load(); got != 1 {
			t.Fatalf("the sink saw %d writes while the loop emitted 3 records, want 1: the records went to the sink instead of the spool", got)
		}
		close(sink.release)
	})
}

// TestServeDiagnosticSpoolDrainsAnAcceptedLineOnClose is the other half of the
// shutdown contract.
//
// A record the spool accepted has left its producer, so dropping it on the way
// out loses the line that says why the daemon stopped -- the very line a reader
// opens the log for. Close therefore drains what was already accepted and waits
// for it, bounded so a stalled sink still cannot hold shutdown.
//
// The sink here is responsive, so the record must be on it before Close returns:
// no await, no retry, nothing left to chance.
func TestServeDiagnosticSpoolDrainsAnAcceptedLineOnClose(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		var sink bytes.Buffer
		spool := newServeDiagnosticSpool(&sink)
		spool.Logf("session-1", "the line the daemon stopped for")
		spool.Close()

		// Inside the bubble the worker's write is ordered against Close's
		// wait, so a readable record here means the flush happened rather
		// than the worker racing the buffer from another goroutine.
		line := parseServeLine(t, sink.String())
		if !strings.Contains(line.message, "the line the daemon stopped for") {
			t.Fatalf("the record after Close = %q, want the accepted line: it was taken from its producer and then dropped", line.message)
		}
		if line.session != "session-1" {
			t.Fatalf("the record's session after Close = %q, want session-1", line.session)
		}
	})
}

// TestRetirementTeardownReportDoesNotBlockOnAStalledSink covers the other loop
// shutdown joins.
//
// A post-commit teardown failure is reported from the retirement loop, and
// shutdown joins that loop after the input loop, so a record written straight to
// a pipe whose reader stopped draining would hold the exit open after every turn
// was already done. With the daemon's spool installed the report cannot wait.
func TestRetirementTeardownReportDoesNotBlockOnAStalledSink(t *testing.T) {
	t.Parallel()
	sink := &stalledSink{release: make(chan struct{}), onEnter: make(chan struct{})}
	t.Cleanup(func() { close(sink.release) })
	spool := newServeDiagnosticSpool(sink)
	t.Cleanup(spool.Close)

	// The pipe is full before the report is attempted.
	spool.Logf("session-1", "the line the sink is stuck on")
	<-sink.onEnter

	// The pipe the fallback writes to is the stalled sink as well: without the
	// spool, either record would land on it and block the loop shutdown joins.
	r := &serveRetirement{deps: serveDeps{serveLogOut: sink}, diagnostics: spool}
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.reportTeardown("session-1", "retirement reader drain failed: %v", errors.New("injected: drain failed"))
		r.reportTeardown("session-1", "retirement release failed: %v", errors.New("injected: release failed"))
	}()
	<-done

	if got := sink.writes.Load(); got != 1 {
		t.Fatalf("the sink saw %d writes while the retirement loop reported twice, want 1: the reports went to the pipe instead of the spool", got)
	}
}

// TestRendezvousRemovalWarningIsAnOfferToTheSpool covers the last report the
// daemon makes on its way out: the warning that its rendezvous entry may be
// stale. It runs after both processing loops have exited, so a synchronous
// write there is the one line that can still hold the exit behind a pipe the
// daemon has stopped draining.
//
// The property that keeps it bounded is that the report is an offer to the
// spool: it must return with the sink stalled, and it must refuse rather than
// wait when the spool is already full.
func TestRendezvousRemovalWarningIsAnOfferToTheSpool(t *testing.T) {
	t.Parallel()
	sink := &stalledSink{release: make(chan struct{}), onEnter: make(chan struct{})}
	t.Cleanup(func() { close(sink.release) })
	spool := newServeDiagnosticSpool(sink)
	t.Cleanup(spool.Close)

	// The worker is stuck in a write, and the pipe is the same stall the
	// removal warning would otherwise write to.
	spool.Logf("session-1", "the line the sink is stuck on")
	<-sink.onEnter

	done := make(chan struct{})
	go func() {
		defer close(done)
		removeRendezvousAtShutdown(
			func() error { return errors.New("injected: remove failed") },
			func() <-chan time.Time {
				elapsed := make(chan time.Time, 1)
				elapsed <- time.Now()
				return elapsed
			},
			func(err error) {
				spool.Logf("session-1", "rendezvous removal failed after attempts, entry may be stale: %v", err)
			},
		)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the removal warning did not return with the pipe stalled: a synchronous write there holds the daemon's exit")
	}

	if got := sink.writes.Load(); got != 1 {
		t.Fatalf("the sink saw %d writes while the removal warning was reported, want 1: the warning went to the pipe instead of the spool", got)
	}
}

// TestServeDiagnosticSpoolReportsWhetherItAcceptedALine pins the contract the
// park's once-per-attempt report depends on.
//
// An offer either lands or reports that it did not. A caller that updated state
// on the strength of a refused offer would silence the condition it meant to
// report -- the park would say "already told" about a line no sink ever saw.
func TestServeDiagnosticSpoolReportsWhetherItAcceptedALine(t *testing.T) {
	t.Parallel()
	sink := &stalledSink{release: make(chan struct{}), onEnter: make(chan struct{})}
	t.Cleanup(func() { close(sink.release) })
	spool := newServeDiagnosticSpool(sink)
	t.Cleanup(spool.Close)

	// The worker takes this line and blocks in the sink, so the buffer begins
	// filling from here on.
	if !spool.Logf("session-1", "the line the worker is stuck writing") {
		t.Fatal("the first offer was refused with an idle spool")
	}
	<-sink.onEnter

	// The burst the shutdown path actually emits: a failed turn reports its
	// error, and the wake answering the same failure reports the park. A worker
	// stuck in one write must not refuse either, or the park goes unreported
	// until some later wake happens by.
	burst := []string{"error: the turn failed", "queued input stood down: the steer is parked"}
	for _, line := range burst {
		if !spool.Logf("session-1", "%s", line) {
			t.Fatalf("a two-line burst was refused while the worker was busy: %q would reach no reader until a later wake", line)
		}
	}

	// The buffer is a bound, not a queue. Offering well past any capacity must
	// be refused rather than wait, so a sink that never drains cannot grow the
	// daemon's memory or hold a caller that promised not to block.
	refused := false
	for i := range 16 {
		if !spool.Logf("session-1", "flood record %d", i) {
			refused = true
		}
	}
	if !refused {
		t.Fatal("sixteen offers past a two-line burst were all accepted with the worker stuck: the spool is unbounded, so a stalled sink would grow without limit")
	}
}

// TestServeStartupAnnouncementIsAnOfferToTheSpool covers the startup lines the
// daemon writes while the input loop it just started is already probing
// restored work: the address it listens on, and why its rendezvous entry could
// not be published.
//
// A restored steering claim that fails reports a stand-down from that loop. If
// that record fills an undrained pipe, a synchronous announcement would block
// the goroutine that has not yet started the HTTP server or the shutdown path,
// so the SIGTERM that follows would cancel a context nothing is waiting on.
// The announcement must be an offer: it returns while the pipe is stalled.
func TestServeStartupAnnouncementIsAnOfferToTheSpool(t *testing.T) {
	t.Parallel()
	sink := &stalledSink{release: make(chan struct{}), onEnter: make(chan struct{})}
	t.Cleanup(func() { close(sink.release) })
	spool := newServeDiagnosticSpool(sink)
	t.Cleanup(spool.Close)

	// The pipe is full before the announcement is made.
	spool.Logf("session-1", "the stand-down that filled the pipe")
	<-sink.onEnter

	done := make(chan struct{})
	go func() {
		defer close(done)
		reportServeStartup(spool, "session-1", "listening on %s", "127.0.0.1:0")
		reportServeStartup(spool, "session-1", "rendezvous write failed: %v", errors.New("injected: write failed"))
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("a startup announcement did not return with the pipe stalled: it holds the daemon before its shutdown path exists")
	}

	if got := sink.writes.Load(); got != 1 {
		t.Fatalf("the sink saw %d writes while two startup announcements were made, want 1: an announcement went to the pipe instead of the spool", got)
	}
}

// TestServeDiagnosticSpoolReservesASlotForStartupLines pins the reservation the
// rendezvous registration failure depends on.
//
// That failure is the one startup line whose information exists nowhere else:
// when registration fails no entry is written, so a refused line leaves an
// operator without the reason discovery did not work. Producers therefore stop
// one slot short of the buffer and a startup line may take the last one.
//
// The counts are the assertion, not the text: producers get exactly the
// unreserved capacity, the startup line is accepted with every producer slot
// taken, and the reservation does not cost the producers a slot while it is
// unused.
func TestServeDiagnosticSpoolReservesASlotForStartupLines(t *testing.T) {
	t.Parallel()
	sink := &stalledSink{release: make(chan struct{}), onEnter: make(chan struct{})}
	t.Cleanup(func() { close(sink.release) })
	spool := newServeDiagnosticSpool(sink)
	t.Cleanup(spool.Close)

	// The worker takes this line and blocks, so the buffer fills from here on.
	spool.Logf("session-1", "the line the worker is stuck writing")
	<-sink.onEnter

	unreserved := serveDiagnosticSpoolCapacity - serveDiagnosticSpoolReservedStartup
	accepted := 0
	for i := range serveDiagnosticSpoolCapacity * 2 {
		if spool.Logf("session-1", "producer record %d", i) {
			accepted++
		}
	}
	if accepted != unreserved {
		t.Fatalf("producer offers accepted = %d while the worker was stuck, want %d: the reservation did not hold a slot back for a startup line", accepted, unreserved)
	}

	// With every producer slot taken, the startup line still fits.
	if !spool.LogStartupf("session-1", "rendezvous write failed: %v", errors.New("injected: write failed")) {
		t.Fatal("the startup line was refused with the buffer full of producer records: a failed rendezvous registration would leave no record of why discovery did not work")
	}

	// And the reservation is one slot, not a second channel: the next producer
	// offer is refused because the buffer is now genuinely full.
	if spool.Logf("session-1", "a producer record after the startup line") {
		t.Fatal("a producer offer was accepted past the full buffer: the reservation is not a bound")
	}
}

// TestServeDiagnosticSpoolLeavesProducersTheirSlotsWithoutAStartupLine pins the
// other half: an unused reservation must not shrink what producers get, or the
// burst fix would be quietly undone by the reservation meant to protect it.
func TestServeDiagnosticSpoolLeavesProducersTheirSlotsWithoutAStartupLine(t *testing.T) {
	t.Parallel()
	sink := &stalledSink{release: make(chan struct{}), onEnter: make(chan struct{})}
	t.Cleanup(func() { close(sink.release) })
	spool := newServeDiagnosticSpool(sink)
	t.Cleanup(spool.Close)

	spool.Logf("session-1", "the line the worker is stuck writing")
	<-sink.onEnter

	// The two-line burst a failed turn emits, with no startup line anywhere.
	burst := []string{"error: the turn failed", "queued input stood down: the steer is parked"}
	for _, line := range burst {
		if !spool.Logf("session-1", "%s", line) {
			t.Fatalf("a burst record was refused with an unused reservation: %q would reach no reader until a later wake", line)
		}
	}
}
