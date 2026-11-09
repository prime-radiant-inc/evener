package agent

import (
	"fmt"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// benchResumeEntries builds the transcript a rerun-heavy session leaves for
// the restore path: reopenMarkers superseded attention rows — a source
// consumed for a start that never presented, and its reopen's fresh copy —
// each followed by the reopen marker that reopens the turn, interleaved with
// the user turns a real conversation carries. The history build and the
// coordinate mappers all read the same slice.
func benchResumeEntries(reopenMarkers int) []transcript.Entry {
	entries := make([]transcript.Entry, 0, reopenMarkers*5+1)
	entries = append(entries, transcript.Entry{Turn: schema.NewTurn(schema.TurnUserInput, llm.User("first"))})
	for i := range reopenMarkers {
		source := fmt.Sprintf("delegate:dlg_bench/delivery/%d", i+1)
		reopened := delegateAttentionReopenID(source, 2)
		row := func(id string) transcript.Entry {
			turn := schema.NewTurn(schema.TurnSteering, llm.User("the report content"))
			turn.AttentionID = id
			return transcript.Entry{Turn: turn}
		}
		resolution := func(id string) transcript.Entry {
			turn := schema.NewTurn(schema.TurnAttentionResolution, llm.System("x"))
			turn.AttentionResolution = &schema.AttentionResolutionInfo{AttentionID: id, Disposition: "consumed", ResumeGeneration: 2}
			return transcript.Entry{Turn: turn}
		}
		reopen := func() transcript.Entry {
			turn := schema.NewTurn(schema.TurnReopen, llm.System("x"))
			turn.TurnID = fmt.Sprintf("t_attempt_%d", i+1)
			return transcript.Entry{Turn: turn}
		}
		entries = append(entries,
			row(source),
			resolution(source),
			row(reopened),
			reopen(),
			transcript.Entry{Turn: schema.NewTurn(schema.TurnUserInput, llm.User("work"))},
		)
	}
	return entries
}

// benchResumeCoordinates runs one restore's coordinate pipeline over the
// entries: the history build with its repair insertions, the reopen markers'
// latest-attempt starts, and the fork boundary's divergence map — the three
// reads the resumed-history build and its mappers serve. Every one of them
// reads the same superseded-attention facts, so the transcript folds once
// for all three, not once per position mapped.
func benchResumeCoordinates(entries []transcript.Entry) {
	superseded := supersededDelegateAttentionIDs(entries)
	history, insertions := resumeHistoryIndexedWithSuperseded(entries, superseded)
	_ = latestAttemptStarts(entries, insertions, superseded)
	_ = resumedDivergence(entries, len(entries), insertions, superseded)
	_ = history
}

func benchResumeCoordinatesOverMarkers(b *testing.B, reopenMarkers int) {
	entries := benchResumeEntries(reopenMarkers)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchResumeCoordinates(entries)
	}
}

func BenchmarkResumeCoordinatesOver16ReopenMarkers(b *testing.B) {
	benchResumeCoordinatesOverMarkers(b, 16)
}

func BenchmarkResumeCoordinatesOver64ReopenMarkers(b *testing.B) {
	benchResumeCoordinatesOverMarkers(b, 64)
}
