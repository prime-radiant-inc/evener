package agent

// Resource regression and benchmark for the per-round request-history snapshot.
//
// prepareModelRequestWithError used to copy s.history at the top of its
// ContextMgmt phase and then overwrite that copy before anything read it: the
// strategy fold re-snapshots at the top of every attempt, and the final
// HistoryExpand snapshot replaces it on the no-strategy path. This file pins
// the request's contents and ordering across large text-only histories and
// holds the per-preparation allocation below a literal ceiling calibrated on
// the copy-bearing implementation, so reintroducing the dead copy fails here.
//
// The fixture runs the real Session and the real preparation path against the
// scripted adapter already used by BenchmarkNewSession. No provider call is
// made and no session internals are mocked.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
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
// preparation of the 1,024-turn fixture. Calibrated on the unchanged
// copy-bearing implementation: the lowest observed baseline was
// 2,331,984 bytes, and the removable copy is
// 1024 * unsafe.Sizeof(schema.Turn{}) = 671,744 bytes, so this ceiling sits
// 335,872 bytes (half the removable copy) under that baseline. The literal
// is deliberate: do not derive it from the implementation under test.
const maxRequestHistoryBytesPerPreparation int64 = 1_996_112

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

// measureRequestHistoryBytesPerPreparation returns the smallest total-allocated
// delta observed across repetitions of one preparation. The minimum discards
// one-off background allocations; the deleted copy is present in every
// repetition, so it cannot be mistaken for noise.
func measureRequestHistoryBytesPerPreparation(t *testing.T, s *Session, repetitions int) int64 {
	t.Helper()
	best := int64(math.MaxInt64)
	for range repetitions {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		prepareRequestHistoryRound(t, s)
		runtime.ReadMemStats(&after)
		if used := int64(after.TotalAlloc - before.TotalAlloc); used < best {
			best = used
		}
	}
	return best
}

// TestPrepareModelRequestHistoryAllocationBudget pins that a preparation
// carries each fixture payload into the request in order and leaves the
// complete source history unchanged, then requires the 1,024-turn preparation
// to stay under a ceiling calibrated against the dead initial snapshot. Content
// assertions run before the budget assertion so a failing baseline demonstrates
// valid behavior alongside excess allocation.
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
			t.Logf("bytes per preparation %d, removable dead copy %d, budget %d", bytesPerPreparation, removable, maxRequestHistoryBytesPerPreparation)
			if bytesPerPreparation >= maxRequestHistoryBytesPerPreparation {
				t.Fatalf("bytes per preparation %d, budget %d", bytesPerPreparation, maxRequestHistoryBytesPerPreparation)
			}
		})
	}
}

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
