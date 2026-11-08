package agent

// Resource regression and benchmark for the per-round request-history snapshot.
//
// prepareModelRequestWithError used to copy s.history at the top of its
// ContextMgmt phase and then overwrite that copy before anything read it: the
// strategy fold re-snapshots at the top of every attempt, and the final
// HistoryExpand snapshot replaces it on the no-strategy path. This file pins
// the request's contents and ordering across large text-only histories and
// holds the per-preparation allocation below a literal ceiling placed between
// the copy-free and copy-bearing implementations, so reintroducing the dead
// copy fails here.
//
// The fixture runs the real Session and the real preparation path against the
// scripted adapter already used by BenchmarkNewSession. No provider call is
// made and no session internals are mocked.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"
	"unsafe"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// requestHistoryBudgetTurns is the fixture size the allocation budget is
// calibrated on. Its text-only turns are large enough that one dead shallow
// copy of s.history is a measurable share of a preparation.
const requestHistoryBudgetTurns = 1024

var requestHistoryFixtureTurns = []int{256, requestHistoryBudgetTurns}

// maxRequestHistoryBytesPerPreparation is the frozen ceiling for one
// preparation of the 1,024-turn fixture. It must sit above the largest
// copy-free preparation and below the smallest copy-bearing one, so that
// reintroducing the dead shallow copy of s.history (1,024 turns *
// unsafe.Sizeof(schema.Turn{}) bytes) fails here while healthy preparations
// pass. Copy-free preparations measure 1,152,312 settled through 1,461,616
// fresh across this file's lanes; the smallest copy-bearing preparation is
// the settled non-race one at 1,987,896 (1,152,312 plus the copy). 1,750,000
// sits between those, 288,384 above the largest copy-free mode and 237,896
// below the smallest copy-bearing one, so attribution noise cannot mask the
// copy and honest drift cannot push a healthy preparation over. The literal
// is deliberate: do not derive it from the implementation under test.
const maxRequestHistoryBytesPerPreparation int64 = 1_750_000

// requestHistoryPayload is the unique body of fixture turn i. It survives
// expansion and request construction verbatim, so an in-order scan of the
// projected messages proves every turn reached the request.
func requestHistoryPayload(i int) string {
	return fmt.Sprintf("fixture-turn-%04d-payload", i)
}

// newRequestHistoryPreparationSession builds a real Session on an isolated
// temporary state directory and replaces its history with text-only fixture
// turns. The context strategy is removed so preparation takes the
// no-fold path, and the missing personal AGENTS.md keeps ambient instructions
// out of the fixture.
func newRequestHistoryPreparationSession(tb testing.TB, turns int) *Session {
	tb.Helper()
	s, err := NewSession(benchClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(tb.TempDir()), SessionConfig{
		StateDir:         tb.TempDir(),
		AgentsDocPath:    filepath.Join(tb.TempDir(), "no-personal-AGENTS.md"),
		MaxSubagentDepth: 1,
		testOnly:         testConfig{skipGitSnapshot: true},
	})
	if err != nil {
		tb.Fatalf("NewSession: %v", err)
	}
	tb.Cleanup(s.Close)
	s.strategy = nil
	s.mu.Lock()
	s.history = make([]schema.Turn, 0, turns)
	for i := range turns {
		s.history = append(s.history, schema.NewTurn(schema.TurnUserInput, llm.User(requestHistoryPayload(i))))
	}
	s.mu.Unlock()
	return s
}

// prepareRequestHistoryRound runs one real preparation and returns the
// projected history and the request it built.
func prepareRequestHistoryRound(tb testing.TB, s *Session) ([]llm.Message, llm.Request) {
	tb.Helper()
	var timings events.RoundTimings
	_, _, history, req, _, _, err := s.prepareModelRequestWithError(context.Background(), 0, &timings)
	if err != nil {
		tb.Fatalf("prepareModelRequestWithError: %v", err)
	}
	return history, req
}

// requestHistorySourceTexts copies the live source history's message texts
// under the session lock.
func requestHistorySourceTexts(t *testing.T, s *Session) []string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	return turnTexts(s.history)
}

// requestHistorySourceSnapshot captures a complete representation of the live
// source history under the session lock. Message text alone says nothing about
// turn kind, identity, provenance, timestamps, roles, tool-call IDs or
// content-part structure, so preservation is proved against these bytes.
func requestHistorySourceSnapshot(t *testing.T, s *Session) []byte {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot, err := json.Marshal(s.history)
	if err != nil {
		t.Fatalf("marshal source history: %v", err)
	}
	return snapshot
}

// assertRequestHistoryPayloadsInOrder requires every fixture payload to appear
// in fixture order in the projected messages.
func assertRequestHistoryPayloadsInOrder(t *testing.T, projected []llm.Message, turns int) {
	t.Helper()
	next := 0
	for _, message := range projected {
		if next == turns {
			break
		}
		if message.Text() == requestHistoryPayload(next) {
			next++
		}
	}
	if next != turns {
		t.Fatalf("projected messages reached fixture payload %d of %d: fixture turns are missing from the request or out of order", next, turns)
	}
}

// assertPreparedRequestHistory checks one preparation's outputs against the
// fixture and against the settled source history captured before it. It is
// shared by both fixture sizes and runs outside any allocation measurement.
func assertPreparedRequestHistory(t *testing.T, s *Session, history []llm.Message, req llm.Request, settledTexts []string, settledSnapshot []byte, turns int) {
	t.Helper()
	assertRequestHistoryPayloadsInOrder(t, history, turns)
	assertRequestHistoryPayloadsInOrder(t, req.Messages, turns)
	if got := requestHistorySourceTexts(t, s); !slices.Equal(got, settledTexts) {
		t.Fatalf("preparation mutated the source history text (len %d, want %d)", len(got), len(settledTexts))
	}
	if got := requestHistorySourceSnapshot(t, s); !bytes.Equal(got, settledSnapshot) {
		t.Fatalf("preparation mutated the complete source history: its text is unchanged, but turn kind, identity, provenance, timestamp, role or content structure changed")
	}
}

// measureRequestHistoryBytesPerPreparation returns the median attributable
// allocation observed across repetitions of one preparation. TotalAlloc is a
// process-global counter, so in a full test binary on a loaded runner any
// concurrent background allocation lands in every measured window, and
// neither a minimum nor a median of bare window deltas can discard it. Each
// round therefore subtracts a control window that runs no preparation: a
// stationary background rate lands in both windows alike and cancels, while
// the preparation's own allocation is present in every round and never
// cancels. The control window mirrors the preparation window — it spans the
// same wall-clock length, bounded by its own memstats reads, and it
// busy-waits rather than sleeps. Staying runnable matters: on a single-P
// runtime a sleeping window would lend its processor to background
// allocators that the preparation window starves, and the subtraction would
// strip real preparation bytes along with the background. The cancellation
// is first-order: the windows can still differ by memstats-read jitter
// against the background rate, so the rounds scatter. A round whose control
// window out-allocates its preparation measures garbage (a burst landing in
// the control window only) and is discarded; the mirror image, a burst
// landing only in the preparation window, is absorbed instead — the median
// tolerates two such rounds of five. The median, not the minimum, summarizes
// the remaining rounds: a minimum would systematically understate the
// preparation, and under load that bias would eat the very margin this
// budget guards. On a quiet process the rounds agree closely, so the median
// costs nothing there.
func measureRequestHistoryBytesPerPreparation(t *testing.T, s *Session, repetitions int) int64 {
	t.Helper()
	var rounds []int64
	for range repetitions {
		var before, after runtime.MemStats
		started := time.Now()
		runtime.ReadMemStats(&before)
		prepareRequestHistoryRound(t, s)
		runtime.ReadMemStats(&after)
		elapsed := time.Since(started)
		var idleBefore, idleAfter runtime.MemStats
		controlStart := time.Now()
		runtime.ReadMemStats(&idleBefore)
		// Busy-wait so the control window stays runnable for the same wall
		// clock the preparation window spans, between its own pair of
		// memstats reads: both windows must sample the same background, and
		// a sleeping window on a single-P runtime would lend its processor
		// to background allocators the preparation window starves.
		for time.Since(controlStart) < elapsed {
		}
		runtime.ReadMemStats(&idleAfter)
		used := int64(after.TotalAlloc-before.TotalAlloc) - int64(idleAfter.TotalAlloc-idleBefore.TotalAlloc)
		if used < 0 {
			continue
		}
		rounds = append(rounds, used)
	}
	if len(rounds) == 0 {
		// Every control window out-allocated its preparation: the process
		// is too loud to measure a preparation against, and no sentinel
		// value would mean anything to the caller's arithmetic.
		t.Fatalf("every control window out-allocated its preparation: process too loud to measure")
	}
	slices.Sort(rounds)
	if len(rounds)%2 == 1 {
		return rounds[len(rounds)/2]
	}
	mid := len(rounds) / 2
	return (rounds[mid-1] + rounds[mid]) / 2
}

// TestPrepareModelRequestHistoryAllocationBudget pins that a preparation
// carries each fixture payload into the request in order and leaves the
// complete source history unchanged, then requires the 1,024-turn preparation
// to stay under a ceiling placed between the copy-free and copy-bearing
// implementations. Content assertions run before the budget assertion so a
// failing baseline demonstrates valid behavior alongside excess allocation.
// Serial: it measures process-global TotalAlloc, and allocations from
// concurrently running tests would land inside every window it measures.
func TestPrepareModelRequestHistoryAllocationBudget(t *testing.T) {
	for _, turns := range requestHistoryFixtureTurns {
		t.Run(fmt.Sprintf("%d turns", turns), func(t *testing.T) {
			s := newRequestHistoryPreparationSession(t, turns)

			// The first warm round lets the notes/memory projection settle. The
			// second warm round is also the assertion round: source stability
			// across both, and this round's outputs asserted, before measuring.
			prepareRequestHistoryRound(t, s)
			settledTexts := requestHistorySourceTexts(t, s)
			settledSnapshot := requestHistorySourceSnapshot(t, s)
			history, req := prepareRequestHistoryRound(t, s)
			assertPreparedRequestHistory(t, s, history, req, settledTexts, settledSnapshot, turns)

			if turns != requestHistoryBudgetTurns {
				return
			}
			bytesPerPreparation := measureRequestHistoryBytesPerPreparation(t, s, 5)
			removable := int64(requestHistoryBudgetTurns) * int64(unsafe.Sizeof(schema.Turn{}))
			t.Logf("attributable bytes per preparation %d, removable dead copy %d, budget %d", bytesPerPreparation, removable, maxRequestHistoryBytesPerPreparation)
			if bytesPerPreparation >= maxRequestHistoryBytesPerPreparation {
				t.Fatalf("bytes per preparation %d, budget %d", bytesPerPreparation, maxRequestHistoryBytesPerPreparation)
			}
		})
	}
}

// TestPrepareModelRequestHistoryBudgetIgnoresConcurrentBackground pins the
// measurement's attribution: a process that keeps allocating in the
// background during the measured windows must not move the attributable
// value. The churner below allocates a fresh buffer each tick, paced on wall
// clock rather than processor time: both windows then sample whatever rate
// the loaded host actually delivers, and a stationary rate is exactly what
// the control-window subtraction cancels. A measurement that counts that
// delivered churn lands above the band or over the budget ceiling outright;
// one that attributes away more than the background masks a real regression
// and lands below the band.
// Serial: it measures process-global TotalAlloc, and allocations from
// concurrently running tests would land inside every window it measures.
func TestPrepareModelRequestHistoryBudgetIgnoresConcurrentBackground(t *testing.T) {
	s := newRequestHistoryPreparationSession(t, requestHistoryBudgetTurns)
	prepareRequestHistoryRound(t, s) // settle the projection as the budget test does
	quiet := measureRequestHistoryBytesPerPreparation(t, s, 5)
	stop := make(chan struct{})
	churnDone := make(chan struct{})
	go func() {
		defer close(churnDone)
		for {
			select {
			case <-stop:
				return
			default:
			}
			sink = make([]byte, 32<<10)
			time.Sleep(200 * time.Microsecond)
		}
	}()
	var stopChurnOnce sync.Once
	stopChurn := func() {
		stopChurnOnce.Do(func() {
			close(stop)
			<-churnDone
		})
	}
	t.Cleanup(stopChurn)
	churned := measureRequestHistoryBytesPerPreparation(t, s, 5)
	// A preparation's attributable allocation settles downward as the
	// session warms, so the churned phase is judged against quiet
	// measurements that bracket it in warmth, not only the colder one
	// taken before the churner started.
	stopChurn()
	quietAgain := measureRequestHistoryBytesPerPreparation(t, s, 5)
	t.Logf("attributable bytes per preparation %d quiet, %d under concurrent churn, %d quiet again, budget %d", quiet, churned, quietAgain, maxRequestHistoryBytesPerPreparation)
	if churned >= maxRequestHistoryBytesPerPreparation {
		t.Fatalf("attributable bytes per preparation %d under concurrent churn, budget %d", churned, maxRequestHistoryBytesPerPreparation)
	}
	// The churned measurement must not just pass the ceiling: it must measure
	// the same preparation. Attributing away MORE than the background masks
	// a real regression just as counting it invents one. The band is wide
	// because honest spreads live inside it: the churner's background
	// arrives in coarse ticks, and the session's own per-preparation
	// allocation drifts as its projections warm.
	lo, hi := min(quiet, quietAgain)*6/10, max(quiet, quietAgain)*14/10
	if churned < lo || churned > hi {
		t.Fatalf("attributable bytes per preparation %d under concurrent churn, want within [%d, %d] of the quiet measurements %d and %d", churned, lo, hi, quiet, quietAgain)
	}
}

// sink gives the churner's buffers a package-level home so each tick's
// allocation escapes to the heap and reaches the allocator; a buffer the
// compiler could keep on the stack would inject no churn at all.
var sink []byte

// BenchmarkPrepareModelRequestHistory measures one real preparation for both
// fixture sizes, including expansion, request construction, continuation
// planning and token budgeting.
func BenchmarkPrepareModelRequestHistory(b *testing.B) {
	for _, turns := range requestHistoryFixtureTurns {
		b.Run(strconv.Itoa(turns), func(b *testing.B) {
			s := newRequestHistoryPreparationSession(b, turns)
			prepareRequestHistoryRound(b, s) // warm the projection before the timed loop
			var timings events.RoundTimings
			b.ReportAllocs()
			for b.Loop() {
				if _, _, _, _, _, _, err := s.prepareModelRequestWithError(context.Background(), 0, &timings); err != nil {
					b.Fatalf("prepareModelRequestWithError: %v", err)
				}
			}
		})
	}
}
