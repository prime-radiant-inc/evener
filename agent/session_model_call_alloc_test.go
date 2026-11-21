package agent

// Resource regression and benchmark for the per-round request-history snapshot.
//
// A preparation expands s.history from locked snapshots taken while preparing
// it: one for each fold attempt, and the final HistoryExpand snapshot both paths
// take before expansion. It must not also clone the published history into a
// copy nothing reads. This file pins the request's contents and ordering across
// large text-only histories and holds the per-preparation allocation below a
// literal ceiling placed between a copy-free preparation and one that also makes
// that dead copy, so a publish pass that hands its caller the copy fails here.
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
	"sync/atomic"
	"testing"
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
// pass. The ceiling is PER LANE because the race detector's instrumentation
// adds real allocation to the same code path and moves the whole distribution
// up, so each build's ceiling must independently clear the required margins.
// Measured on 2026-11-17 with the minimum-of-rounds estimator, copy-free
// preparations are 1,001,048 on the plain lane and 1,001,080 under -race, and
// copy-bearing preparations are 1,844,824 and 1,844,856 (the copy is 1,024 *
// unsafe.Sizeof(schema.Turn{}) = 843,776 bytes in both; each lane's
// copy-bearing reading is its own copy-free reading plus exactly that). Both
// ceilings are 1,425,000, near the midpoint of each lane's feasible band:
// 423,952 and 423,920 above the two copy-free measurements, and 419,824 and
// 419,856 below the two copy-bearing ones, so attribution noise and honest
// drift cannot mask the copy on either side of the bound.
//
// The two lanes track each other to within tens of bytes, so the two literals
// hold the same value today. They stay separate so a change that moves one lane
// alone cannot silently move the other.
//
// The margins are a fraction of the copy rather than the copy's own size
// because the gap between a copy-free preparation and its copy-bearing partner
// is 843,776 bytes (the copy itself): a ceiling at the midpoint of the two
// readings holds half a copy on each side, and the regression test below
// requires a quarter copy on each side as its minimum.
//
// Both literals are deliberate: do not derive them from the implementation
// under test.
const (
	maxRequestHistoryBytesPerPreparationPlain int64 = 1_425_000
	maxRequestHistoryBytesPerPreparationRace  int64 = 1_425_000
)

// maxRequestHistoryBytesPerPreparation is this lane's ceiling. -race is
// detected through the package's existing raceDetectorEnabled const so the
// two lanes cannot silently share one number again.
func maxRequestHistoryBytesPerPreparation() int64 {
	if raceDetectorEnabled {
		return maxRequestHistoryBytesPerPreparationRace
	}
	return maxRequestHistoryBytesPerPreparationPlain
}

// requestHistoryPayload is the unique body of fixture turn i. It survives
// expansion and request construction verbatim, so an in-order scan of the
// projected messages proves every turn reached the request.
func requestHistoryPayload(i int) string {
	return fmt.Sprintf("fixture-turn-%04d-payload", i)
}

// requestHistoryLane selects whether the fixture keeps the session's own
// context strategy, which is the only difference between the two lanes: with it
// every preparation runs the per-request fold pass, without it preparation takes
// the no-fold path.
type requestHistoryLane bool

const (
	requestHistoryNoFoldLane requestHistoryLane = false
	requestHistoryFoldLane   requestHistoryLane = true
)

// newRequestHistoryPreparationSession builds a real Session on an isolated
// temporary state directory and replaces its history with text-only fixture
// turns. The missing personal AGENTS.md keeps ambient instructions out of the
// fixture. One preparation runs before returning, so every measurement lane
// starts from a settled projection instead of each caller hand-rolling its own
// warm-up.
//
// On the fold lane that warm-up must actually publish a fold. A preparation whose
// publication is refused still returns every fixture message and still measures,
// so without this assertion the allocation budget below would be green while the
// publish pass it exists to bound never ran.
func newRequestHistoryPreparationSession(tb testing.TB, turns int, lane requestHistoryLane) *Session {
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
	if !bool(lane) {
		s.strategy = nil
	} else if s.strategy == nil {
		tb.Fatalf("NewSession built no context strategy: the fold lane would measure the no-fold path")
	}
	s.mu.Lock()
	s.history = make([]schema.Turn, 0, turns)
	for i := range turns {
		s.history = append(s.history, schema.NewTurn(schema.TurnUserInput, llm.User(requestHistoryPayload(i))))
	}
	s.mu.Unlock()
	publicationsBefore := requestHistoryFoldPublications(tb, s)
	prepareRequestHistoryRound(tb, s)
	if lane {
		if after := requestHistoryFoldPublications(tb, s); after <= publicationsBefore {
			tb.Fatalf("fold lane published no fold: publication revision %d before the warm-up preparation and %d after it, so the pass this lane measures never ran", publicationsBefore, after)
		}
	}
	return s
}

// requestHistoryFoldPublications reports the revision of the newest fold
// publication, the counter publishFoldTransaction stamps under s.mu when a fold
// publishes. It is zero until a fold publishes.
func requestHistoryFoldPublications(tb testing.TB, s *Session) int {
	tb.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.newestPublishedFoldRevision
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

// measureRequestHistoryBytesPerPreparation returns the smallest raw allocation
// observed across the measurement rounds of one preparation.
//
// TotalAlloc is a process-global counter, so in a full test binary on a loaded
// runner any concurrent background allocation lands inside a measured window.
// Background can only add bytes to a window: nothing outside the preparation
// makes a preparation allocate less. The minimum over several rounds is
// therefore the round least contaminated by background, and it is an upper
// bound on the bytes truly attributable to one preparation. Crucially it needs
// no assumption about the background's rate. The estimator this replaced
// subtracted a busy-waiting control window of the preparation's own length,
// which cancels a background only when that background delivers the same rate
// in both windows. It does not when another process competes for cores: the OS
// decides when a churner runs, so its delivered rate differs between the two
// windows and the subtraction leaves churn in the attributed figure. That was
// the flake in #6086, and no window length or control window can repair it,
// because the rate difference is not a property of this process.
//
// A background present in every round still contributes its share of the
// least-loaded round, since no estimator can subtract bytes it cannot
// distinguish from the preparation. Callers therefore keep this measurement
// serial (the // Serial: comments below) so the package's other tests inject no
// allocation into these windows, and the budget below keeps headroom for one
// round's share of a hostile background.
//
// The minimum is also the safe direction for the budget: it cannot understate a
// preparation, so a reintroduced dead copy cannot slip under the ceiling. Each
// ceiling's teeth are pinned separately, by
// TestPrepareModelRequestHistoryCeilingCatchesDeadCopy and
// TestPrepareModelRequestHistoryFoldCeilingCatchesPublishedCopy.

// requestHistoryMeasurementRounds is the round count the helper's minimum is
// designed around.
const requestHistoryMeasurementRounds = 5

func measureRequestHistoryBytesPerPreparation(t *testing.T, s *Session) int64 {
	t.Helper()
	return measureRequestHistoryBytesAttributableToPreparation(t, func(int) {
		prepareRequestHistoryRound(t, s)
	})
}

// measureRequestHistoryBytesAttributableToPreparation is the estimator over an
// injectable round. The seam exists so a test can pin the attribution with a
// round it pollutes itself, deterministically, instead of waiting for a loaded
// host to produce the same shape by chance; see
// TestPrepareModelRequestHistoryAttributionIgnoresSparseBackground.
func measureRequestHistoryBytesAttributableToPreparation(t *testing.T, round func(round int)) int64 {
	t.Helper()
	var quietest int64
	for i := range requestHistoryMeasurementRounds {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		round(i)
		runtime.ReadMemStats(&after)
		if delta := int64(after.TotalAlloc - before.TotalAlloc); i == 0 || delta < quietest {
			quietest = delta
		}
	}
	return quietest
}

// requestHistoryDeadCopyBytes is one shallow copy of the budget fixture's
// history: the bytes a preparation adds when the publish pass hands its caller a
// clone of the published slice instead of the slice itself.
func requestHistoryDeadCopyBytes() int64 {
	return int64(requestHistoryBudgetTurns) * int64(unsafe.Sizeof(schema.Turn{}))
}

// measureRequestHistoryBytesWithDeadCopy measures one preparation of the
// session's lane. With copyBearing the round also makes the copy a publish pass
// must not make: s.history cloned under s.mu and discarded unread, the pass's own
// allocation shape rather than an arbitrary allocation of the same size.
func measureRequestHistoryBytesWithDeadCopy(t *testing.T, s *Session, copyBearing bool) int64 {
	t.Helper()
	return measureRequestHistoryBytesAttributableToPreparation(t, func(int) {
		prepareRequestHistoryRound(t, s)
		if copyBearing {
			s.mu.Lock()
			dead := append([]schema.Turn(nil), s.history...)
			s.mu.Unlock()
			_ = dead
		}
	})
}

// assertRequestHistoryCeilingHasTeeth requires a lane's ceiling to sit between a
// copy-free and a copy-bearing measurement of one preparation, with a quarter of
// the dead copy as margin on each side: the ceiling clears a healthy preparation
// and catches one that also makes the copy, and each gap is a real share of the
// copy rather than a few bytes of noise. A quarter copy is the minimum margin
// this requires, not the most a ceiling can hold: a lane's two readings sit one
// copy apart, so a ceiling at their midpoint leaves half a copy on each side.
func assertRequestHistoryCeilingHasTeeth(t *testing.T, free, bearing, ceiling, deadCopyBytes int64) {
	t.Helper()
	if free >= ceiling {
		t.Fatalf("copy-free preparation measured %d at or over the ceiling %d: the ceiling does not clear a healthy preparation in this lane", free, ceiling)
	}
	if bearing <= ceiling {
		t.Fatalf("copy-bearing preparation measured %d at or under the ceiling %d: reintroducing the dead copy would pass", bearing, ceiling)
	}
	minMargin := deadCopyBytes / 4
	if margin := bearing - ceiling; margin < minMargin {
		t.Fatalf("only %d bytes between the ceiling %d and the copy-bearing measurement %d, want at least %d (a quarter of the dead copy)", margin, ceiling, bearing, minMargin)
	}
	if margin := ceiling - free; margin < minMargin {
		t.Fatalf("only %d bytes between the copy-free measurement %d and the ceiling %d, want at least %d (a quarter of the dead copy)", margin, free, ceiling, minMargin)
	}
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
			s := newRequestHistoryPreparationSession(t, turns, requestHistoryNoFoldLane)

			// The session arrives settled; this is the assertion round: source
			// stability across the settle and this round, and this round's
			// outputs asserted, before measuring.
			settledTexts := requestHistorySourceTexts(t, s)
			settledSnapshot := requestHistorySourceSnapshot(t, s)
			history, req := prepareRequestHistoryRound(t, s)
			assertPreparedRequestHistory(t, s, history, req, settledTexts, settledSnapshot, turns)

			if turns != requestHistoryBudgetTurns {
				return
			}
			bytesPerPreparation := measureRequestHistoryBytesPerPreparation(t, s)
			removable := requestHistoryDeadCopyBytes()
			t.Logf("attributable bytes per preparation %d, removable dead copy %d, budget %d", bytesPerPreparation, removable, maxRequestHistoryBytesPerPreparation())
			if bytesPerPreparation >= maxRequestHistoryBytesPerPreparation() {
				t.Fatalf("bytes per preparation %d, budget %d", bytesPerPreparation, maxRequestHistoryBytesPerPreparation())
			}
		})
	}
}

// TestPrepareModelRequestHistoryCeilingCatchesDeadCopy pins the bound's
// teeth independently of the assertion that consumes it: a preparation that
// also makes the dead shallow copy of s.history must measure over this
// lane's ceiling, with real margin, while a copy-free preparation stays
// under it. The budget test itself can only fail when the copy is
// reintroduced into production code; this test fails if the ceiling is ever
// moved to where the copy would slip under it, or down past a healthy
// preparation. It measures both shapes back to back on one fixture, so the
// margin is a ratio of two readings from the same process rather than a
// comparison against a number in a comment.
// Serial: it measures process-global TotalAlloc, like its siblings.
func TestPrepareModelRequestHistoryCeilingCatchesDeadCopy(t *testing.T) {
	s := newRequestHistoryPreparationSession(t, requestHistoryBudgetTurns, requestHistoryNoFoldLane)
	deadCopyBytes := requestHistoryDeadCopyBytes()
	free := measureRequestHistoryBytesWithDeadCopy(t, s, false)
	bearing := measureRequestHistoryBytesWithDeadCopy(t, s, true)
	ceiling := maxRequestHistoryBytesPerPreparation()
	t.Logf("copy-free %d, copy-bearing %d, dead copy %d, ceiling %d, race %v", free, bearing, deadCopyBytes, ceiling, raceDetectorEnabled)
	assertRequestHistoryCeilingHasTeeth(t, free, bearing, ceiling, deadCopyBytes)
}

// TestPrepareModelRequestHistoryBudgetIgnoresConcurrentBackground pins the
// measurement's attribution: a process that keeps allocating in the
// background during the measured rounds must report the preparation's own
// bytes. The churner allocates a fresh buffer per burst, and a handshake
// (measureWithChurnRounds) puts a fixed, known number of bursts inside each of
// a majority of the rounds while the churner provably parks for the rest. The
// estimator's minimum therefore has a clean round to report, and an estimator
// that summarizes the polluted majority reports the inflated figure. A
// measurement that counts the churn lands above the band or over the budget
// ceiling outright; one that attributes away more than the background masks a
// real regression and lands below the band. The delivered tick count is
// asserted, so the churned phase cannot silently measure an unchurned process.
// Serial: it measures process-global TotalAlloc, and allocations from
// concurrently running tests would land inside every window it measures.

// TestPrepareModelRequestHistoryAttributionIgnoresSparseBackground pins the
// estimator's attribution against #6086's mechanism deterministically, with no
// dependence on how a loaded host schedules a churner.
//
// The measurement's job is to report the bytes one preparation allocates. It
// cannot see who allocated them, so background that lands inside a measured
// round is indistinguishable from the preparation's own bytes. The estimator
// this test was written against subtracted an equal-length busy-waiting control
// window and took the median of the differences, which cancels a background
// only when the background delivers the same rate in both windows. When another
// process competes for cores, the OS decides when the churner runs, so the rate
// differs window to window and the subtraction leaves churn in the figure.
//
// The round below is a stand-in for a preparation, not the preparation itself
// (the budget test measures that): it allocates a fixed, known number of bytes,
// and in three of the five rounds it also allocates a fixed, known background
// block, synchronously inside the round, so the pollution is a certainty rather
// than a scheduler outcome. Because the block lands inside the round, the control
// window the old estimator measured afterwards is clean and subtracts nothing:
// that estimator reports the polluted majority, which is exactly the recorded
// failure's shape (1,810,064 over a quiet 1,614,848). Three of five rounds is
// the majority a median lands on; the estimator's minimum has two clean rounds
// to report instead.
//
// Serial: it measures process-global TotalAlloc, like its siblings.
func TestPrepareModelRequestHistoryAttributionIgnoresSparseBackground(t *testing.T) {
	const (
		preparationBytes = 1 << 20
		backgroundBytes  = 512 << 10
		// backgroundFromRound is the first round that carries background. The
		// three polluted rounds are a majority of requestHistoryMeasurementRounds.
		backgroundFromRound = 2
	)
	attributed := measureRequestHistoryBytesAttributableToPreparation(t, func(round int) {
		size := preparationBytes
		if round >= backgroundFromRound {
			size += backgroundBytes
		}
		sink = make([]byte, size)
	})
	t.Logf("attributed %d for a %d-byte preparation with %d bytes of background in rounds %d onward of %d, race %v",
		attributed, preparationBytes, backgroundBytes, backgroundFromRound, requestHistoryMeasurementRounds, raceDetectorEnabled)
	// The attribution must be a clean round's figure, so background that lands
	// in most rounds cannot inflate it. An estimator that reports the polluted
	// majority reads preparationBytes+backgroundBytes here. The bound sits
	// halfway between the clean and the polluted reading rather than at the
	// polluted one, because memstats-read jitter moves a reading by tens to
	// hundreds of bytes: a bound at the polluted value would let a slightly
	// under-reading polluted estimator pass.
	if attributed >= preparationBytes+backgroundBytes/2 {
		t.Fatalf("attributed %d bytes to a %d-byte preparation: %d bytes of background from %d of %d rounds were counted as the preparation's own",
			attributed, preparationBytes, backgroundBytes, requestHistoryMeasurementRounds-backgroundFromRound, requestHistoryMeasurementRounds)
	}
	// It must also not understate: every round held at least the preparation.
	if attributed < preparationBytes {
		t.Fatalf("attributed %d bytes to a %d-byte preparation: the estimator reported less than the preparation allocated", attributed, preparationBytes)
	}
}

// churnBurstsPerPollutedRound is the background injected into one polluted
// measurement round: twenty 32KB bursts, a little under the ~950KB per window
// the CI failure this file's budget was recalibrated from carried. It is large
// on purpose: it puts an estimator that reports the polluted majority over
// maxRequestHistoryBytesPerPreparation by a wide margin, so the failure is a
// structural reading and not a few bytes of noise.
const churnBurstsPerPollutedRound = 20

// churnCleanRounds is how many of the measurement rounds the churner stays
// provably idle for. The estimator's minimum needs one; two give it a second
// reading of the same quiet figure, and the remaining three rounds are a
// majority, so an estimator that summarizes the polluted majority — the median
// this file used to take — reports the inflated figure.
const churnCleanRounds = 2

// minimumChurnTicks is the churned phase's liveness floor: background that
// never arrived would let any attribution pass against an unchurned process.
// The handshake below delivers churnBurstsPerPollutedRound bursts per polluted
// round and the test awaits each round's acknowledgement, so this floor is met
// by construction on every runtime, single- or multi-P; it stays as the
// assertion that the churn happened rather than as a scheduler-dependent hope.
const minimumChurnTicks = 10

func TestPrepareModelRequestHistoryBudgetIgnoresConcurrentBackground(t *testing.T) {
	s := newRequestHistoryPreparationSession(t, requestHistoryBudgetTurns, requestHistoryNoFoldLane)
	quiet := measureRequestHistoryBytesPerPreparation(t, s)
	churned, churnTicksInWindow := measureWithChurnRounds(t, s)
	// A preparation's attributable allocation settles downward as the
	// session warms, so the churned phase is judged against quiet
	// measurements that bracket it in warmth, not only the colder one
	// taken before the churner started.
	quietAgain := measureRequestHistoryBytesPerPreparation(t, s)
	t.Logf("attributable bytes per preparation %d quiet, %d under concurrent churn (%d ticks), %d quiet again, budget %d", quiet, churned, churnTicksInWindow, quietAgain, maxRequestHistoryBytesPerPreparation())
	if churned >= maxRequestHistoryBytesPerPreparation() {
		t.Fatalf("attributable bytes per preparation %d under concurrent churn, budget %d", churned, maxRequestHistoryBytesPerPreparation())
	}
	// The band is wide because honest spreads live inside it: the session's own
	// per-preparation allocation drifts as its projections warm.
	lo, hi := min(quiet, quietAgain)*6/10, max(quiet, quietAgain)*14/10
	if churned < lo || churned > hi {
		t.Fatalf("attributable bytes per preparation %d under concurrent churn, want within [%d, %d] of the quiet measurements %d and %d", churned, lo, hi, quiet, quietAgain)
	}
	// A churner that stops delivering would pass every assertion above against
	// an unchurned process. The handshake delivers every burst before its round
	// closes, so this holds on any runtime, single- or multi-P: the assertion
	// is no longer excused on a single processor, because a single processor no
	// longer starves the churner.
	if churnTicksInWindow < minimumChurnTicks {
		t.Fatalf("churner delivered %d ticks during the churned phase, want at least %d: the churn measurement was taken without background", churnTicksInWindow, minimumChurnTicks)
	}
}

// measureWithChurnRounds measures one preparation while a background goroutine
// allocates inside a fixed majority of the measurement rounds, and returns the
// attributable figure with the background ticks it actually delivered.
//
// The churner runs only when a round asks it to and each polluted round waits
// for its acknowledgement, so which rounds carry background is fixed by this
// function rather than by how the host schedules a goroutine. That is the point:
// the estimator's job is to attribute the preparation's own bytes while
// background is running, and a background whose rate the OS decides cannot test
// that — the host, not the estimator, would decide how much of it each window
// saw. Here the polluted rounds are provably polluted by churnBurstsPerPollutedRound
// bursts each, and the clean rounds are provably idle because the churner is
// parked on its request channel for them.
//
// The deterministic reproduction of #6086 lives in
// TestPrepareModelRequestHistoryAttributionIgnoresSparseBackground, which drives
// the estimator directly with a round whose own allocation differs between
// rounds. This helper drives the real preparation, so the background it injects
// is measured the way the test above intends: as background that must not move
// the attributable figure.
func measureWithChurnRounds(t *testing.T, s *Session) (int64, int64) {
	t.Helper()
	type churnRequest struct {
		bursts int
		done   chan struct{}
	}
	requests := make(chan churnRequest)
	churnerDone := make(chan struct{})
	var churnTicks atomic.Int64
	go func() {
		defer close(churnerDone)
		for request := range requests {
			for range request.bursts {
				sink = make([]byte, 32<<10)
				churnTicks.Add(1)
			}
			close(request.done)
		}
	}()
	t.Cleanup(func() {
		close(requests)
		<-churnerDone
	})
	attributed := measureRequestHistoryBytesAttributableToPreparation(t, func(round int) {
		prepareRequestHistoryRound(t, s)
		if round < churnCleanRounds {
			return
		}
		done := make(chan struct{})
		requests <- churnRequest{bursts: churnBurstsPerPollutedRound, done: done}
		<-done
	})
	return attributed, churnTicks.Load()
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
			s := newRequestHistoryPreparationSession(b, turns, requestHistoryNoFoldLane)
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

// BenchmarkPrepareModelRequestHistoryFold measures one real preparation with
// the fold pass enabled, the lane a configured strategy runs. Pressure stays far
// below the checkpoint threshold, so the fold folds nothing and makes no
// summarizer call: what remains is the pass this lane measures, the snapshot and
// publication every request pays for whether or not a layer folds anything.
func BenchmarkPrepareModelRequestHistoryFold(b *testing.B) {
	for _, turns := range requestHistoryFixtureTurns {
		b.Run(strconv.Itoa(turns), func(b *testing.B) {
			s := newRequestHistoryPreparationSession(b, turns, requestHistoryFoldLane)
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

// maxRequestFoldHistoryBytesPerPreparation* is the frozen ceiling for one
// fold-lane preparation of the 1,024-turn fixture, the lane a session with a
// configured context strategy runs. It must sit above the copy-free preparation
// and below the copy-bearing one by at least a quarter of the copy itself, so
// reintroducing the copy fails here while a healthy preparation passes. Its
// teeth are pinned directly by
// TestPrepareModelRequestHistoryFoldCeilingCatchesPublishedCopy; do not derive
// the literal from the implementation under test.
const maxRequestFoldHistoryBytesPerPreparationPlain int64 = 2_268_000

// maxRequestFoldHistoryBytesPerPreparationRace is the same bound for the race
// lane. The literals are separate so a change that moves one lane cannot
// silently move the other.
const maxRequestFoldHistoryBytesPerPreparationRace int64 = 2_268_000

// maxRequestFoldHistoryBytesPerPreparation is this lane's ceiling; -race is
// detected through the package's existing raceDetectorEnabled const.
func maxRequestFoldHistoryBytesPerPreparation() int64 {
	if raceDetectorEnabled {
		return maxRequestFoldHistoryBytesPerPreparationRace
	}
	return maxRequestFoldHistoryBytesPerPreparationPlain
}

// TestPrepareModelRequestHistoryFoldAllocationBudget pins that a preparation on
// the fold lane carries every fixture payload into the request in order, leaves
// the complete source history unchanged, and stays under a ceiling placed between
// a copy-free preparation and one that also clones the published history. The
// fixture's warm-up asserts the lane actually published a fold, so a refused
// publication cannot leave this test measuring a pass that never ran.
// Serial: it measures process-global TotalAlloc, and allocations from
// concurrently running tests would land inside every window it measures.
func TestPrepareModelRequestHistoryFoldAllocationBudget(t *testing.T) {
	s := newRequestHistoryPreparationSession(t, requestHistoryBudgetTurns, requestHistoryFoldLane)

	// The session arrives settled; this is the assertion round: source stability
	// across the settle and this round, and this round's outputs asserted, before
	// measuring.
	settledTexts := requestHistorySourceTexts(t, s)
	settledSnapshot := requestHistorySourceSnapshot(t, s)
	history, req := prepareRequestHistoryRound(t, s)
	assertPreparedRequestHistory(t, s, history, req, settledTexts, settledSnapshot, requestHistoryBudgetTurns)

	bytesPerPreparation := measureRequestHistoryBytesPerPreparation(t, s)
	removable := requestHistoryDeadCopyBytes()
	t.Logf("fold-lane bytes per preparation %d, dead published copy %d, budget %d, race %v",
		bytesPerPreparation, removable, maxRequestFoldHistoryBytesPerPreparation(), raceDetectorEnabled)
	if bytesPerPreparation >= maxRequestFoldHistoryBytesPerPreparation() {
		t.Fatalf("fold-lane bytes per preparation %d, budget %d", bytesPerPreparation, maxRequestFoldHistoryBytesPerPreparation())
	}
}

// TestPrepareModelRequestHistoryFoldCeilingCatchesPublishedCopy pins the bound's
// teeth independently of the assertion that consumes it: a preparation that also
// makes the dead shallow copy of the published history must measure over this
// lane's ceiling, with real margin, while a copy-free preparation stays under
// it. The copy reproduced here is the publish pass's own shape — the published
// history, which is s.history's backing array, cloned under s.mu and discarded
// unread — so the margin is a ratio of two readings from one process rather than
// a number in a comment. The budget test itself can only fail when the copy is
// reintroduced into production code; this test fails if the ceiling is ever
// moved to where that copy would slip under it.
// Serial: it measures process-global TotalAlloc, like its siblings.
func TestPrepareModelRequestHistoryFoldCeilingCatchesPublishedCopy(t *testing.T) {
	s := newRequestHistoryPreparationSession(t, requestHistoryBudgetTurns, requestHistoryFoldLane)
	deadCopyBytes := requestHistoryDeadCopyBytes()
	free := measureRequestHistoryBytesWithDeadCopy(t, s, false)
	bearing := measureRequestHistoryBytesWithDeadCopy(t, s, true)
	ceiling := maxRequestFoldHistoryBytesPerPreparation()
	t.Logf("fold-lane copy-free %d, copy-bearing %d, dead copy %d, ceiling %d, race %v",
		free, bearing, deadCopyBytes, ceiling, raceDetectorEnabled)
	assertRequestHistoryCeilingHasTeeth(t, free, bearing, ceiling, deadCopyBytes)
}
