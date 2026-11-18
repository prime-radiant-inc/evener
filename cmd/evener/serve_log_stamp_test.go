package main

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/internal/testwait"
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
	t.Parallel()
	sink := &stalledSink{release: make(chan struct{}), onEnter: make(chan struct{})}
	t.Cleanup(func() { close(sink.release) })

	direct := make(chan struct{})
	go func() {
		defer close(direct)
		serveLogf(sink, "session-1", "diagnostic straight to the sink")
	}()
	// The sink closes entered as it is about to block, so awaiting that is
	// awaiting the write's entry rather than polling for it. A failed
	// assertion below still releases the writer, which keeps the failure a
	// report instead of a hang.
	<-sink.onEnter
	select {
	case <-direct:
		t.Fatal("a direct write to a stalled sink returned; this test cannot show what the input loop used to wait on")
	default:
	}

	spool := newServeDiagnosticSpool(sink)
	defer spool.Close()
	// The worker takes the first line and blocks in the write, as the direct
	// write above is still blocked. Everything after it must not wait.
	spool.Logf("session-1", "first stalled diagnostic")
	// onEnter fires once, for the direct write above. The worker's own write is
	// the second, so the counter is what says the worker is inside the sink
	// and therefore cannot be the one making the emits below return.
	testwait.Until(t, "the spool's worker to reach the stalled sink", func() bool {
		return sink.writes.Load() == 2
	})
	emitted := make(chan struct{})
	go func() {
		defer close(emitted)
		for range 100 {
			spool.Logf("session-1", "diagnostic %d behind a stalled sink", 1)
		}
	}()
	<-emitted
	// The sink is still stalled at this point: release has not closed, so the
	// worker cannot have finished its write. That is what makes the emits
	// above meaningful -- they completed against a sink that is still stuck.
	if got := sink.writes.Load(); got != 2 {
		t.Fatalf("the sink saw %d writes while 100 diagnostics were enqueued, want 2: the worker drained the spool instead of blocking in the first write, so this proves nothing", got)
	}
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
func TestServeDiagnosticSpoolStampsWhenTheLineIsEmitted(t *testing.T) {
	t.Parallel()
	sink := &gatedSink{
		release: make(chan struct{}),
		entered: make(chan struct{}),
		lines:   make(chan string, 4),
	}
	spool := newServeDiagnosticSpool(sink)
	defer spool.Close()

	blocker := time.Date(2026, 7, 31, 18, 26, 56, 0, time.UTC)
	waiting := time.Date(2026, 7, 31, 18, 26, 57, 0, time.UTC)
	released := time.Date(2026, 7, 31, 18, 27, 30, 0, time.UTC)

	// The first line reaches the sink and blocks there.
	spool.logAt(blocker, "session-1", "the line the sink is stuck on")
	<-sink.entered

	// The second is emitted while the sink is blocked, and the sink does not
	// resume until much later.
	spool.logAt(waiting, "session-1", "a line that waits behind the sink")
	testwait.Until(t, "the waiting line to be enqueued", func() bool {
		return len(spool.lines) == 1
	})
	close(sink.release)

	first := parseServeLine(t, <-sink.lines)
	second := parseServeLine(t, <-sink.lines)
	if first.at != serveLogStampFor(blocker) {
		t.Fatalf("the blocked line's instant = %q, want %q", first.at, serveLogStampFor(blocker))
	}
	if second.at != serveLogStampFor(waiting) {
		t.Fatalf("the waiting line's instant = %q, want %q: it was stamped when the sink resumed (%q), not when it was emitted", second.at, serveLogStampFor(waiting), serveLogStampFor(released))
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
	t.Parallel()
	sink := &stalledSink{release: make(chan struct{}), onEnter: make(chan struct{})}
	t.Cleanup(func() { close(sink.release) })
	diagnostics := newServeDiagnosticSpool(sink)
	t.Cleanup(diagnostics.Close)

	// The sink is stalled from the first write, which is the state a full pipe
	// puts the process in.
	diagnostics.Logf("session-1", "the line the sink is stuck on")
	<-sink.onEnter

	done := make(chan struct{})
	go func() {
		defer close(done)
		// A turn that failed, then a turn whose runnable input stood down: the
		// two records the loop emits on this goroutine's behalf.
		reportTurnOutcome(diagnostics, "session-1", errors.New("injected: turn failed"), true, agent.PendingInputSteeringParked)
		reportTurnOutcome(diagnostics, "session-1", nil, true, agent.PendingInputStopHeld)
		reportTurnOutcome(diagnostics, "session-1", errors.New("injected: another turn failed"), false, agent.PendingInputNoOutcome)
	}()
	<-done

	// The sink never resumed, so a record that had gone straight to it would
	// still be blocked here and this test would not have reached the assertion.
	if got := sink.writes.Load(); got != 1 {
		t.Fatalf("the sink saw %d writes while the loop emitted 3 records, want 1: the records went to the sink instead of the spool", got)
	}
}
