package hub

import (
	"context"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/transcriptindex"
)

type pastTranscriptView interface {
	Latest(int) (transcriptindex.Window, error)
	LatestSince(int, appwire.SnapshotIdentity) (transcriptindex.Window, *transcriptindex.Changes, error)
	Before(appwire.ThreadItemPosition, int) (transcriptindex.Window, error)
	ResolveTurnWindow(appwire.ThreadTurnWindow) (transcriptindex.ResolvedTurnWindow, error)
	Incarnation() (string, error)
	DerivedTotals(int) (*appwire.EvenerUsage, int, error)
	AnswerContinuation(context.Context, string, string, *appwire.SnapshotIdentity) (bool, appwire.SnapshotIdentity, error)
}

func withPastServingView(ctx context.Context, entry hubcore.PastEntry, fn func(pastTranscriptView, string) error) error {
	if !appwire.HistoryServingFactsRequested(ctx) {
		return withPastTranscriptIndex(ctx, entry, func(index *transcriptindex.Index) error { return fn(index, "") })
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	index, err := pastTranscriptIndexes.Acquire(pastTranscriptPath(entry))
	if err != nil {
		return answerPastTranscriptIndexError(entry, err)
	}
	defer pastTranscriptIndexes.Release(index)
	err = index.ReadObserved(time.Now, func(snapshot *transcriptindex.Snapshot, asOf time.Time) error {
		return fn(snapshot, asOf.UTC().Format(time.RFC3339Nano))
	})
	return answerPastTranscriptIndexError(entry, err)
}

func savedServingFacts(asOf string) *appwire.HistoryServingFacts {
	if asOf == "" {
		return nil
	}
	return &appwire.HistoryServingFacts{ServedFrom: "saved-index", AsOf: asOf}
}

func fallbackServingFacts(response appwire.ThreadReadResponse, reason string) appwire.ThreadReadResponse {
	if response.ServingFacts != nil {
		facts := *response.ServingFacts
		facts.FallbackReason = reason
		facts.InstanceID = ""
		response.ServingFacts = &facts
	}
	return response
}
