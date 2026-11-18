package hub

import (
	"context"
	"errors"
	"os"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/llm"
)

// liveOwnerRelaySource answers a live read with the identity it serves under
// and no items: a daemon naming itself on a thread whose live projection has
// nothing to show yet, which is exactly the reply that makes the saved-history
// enrichment run.
type liveOwnerRelaySource struct {
	relayLifecycleSource
	threadID       string
	bootGeneration string
	epoch          uint64
}

func (s *liveOwnerRelaySource) ID() string { return appwire.LocalSourceID }

func (s *liveOwnerRelaySource) ReadThread(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	return appwire.ThreadReadResponse{
		Thread:         appwire.Thread{Evener: appwire.EvenerThread{Ref: "local:" + s.threadID}},
		BootGeneration: s.bootGeneration,
		Epoch:          s.epoch,
	}, nil
}

// TestLiveReplyOwnerRidesTheEnrichmentFailure pins the identity the client
// needs to retire the sequence it holds from a daemon that has been replaced.
//
// A live reply that carried a boot token and no items established an owner, so
// a failure of the saved-history enrichment that followed it must carry THAT
// owner, not "unknown-owner": the client uses the failure's identity to name
// the replacement, and an ownerless stamp leaves the outgoing daemon's hold
// sequence in place to outrank the replacement's first update (#6844).
func TestLiveReplyOwnerRidesTheEnrichmentFailure(t *testing.T) {
	cfg, entry, path := seedIndexedPastSession(t,
		execution("turn_m1", schema.NewTurn(schema.TurnUserInput, llm.User("question"))),
	)
	// The enrichment read fails on its own: the transcript cannot be decoded.
	// That is the failure whose stamp is under test, and its structured
	// diagnostic must survive.
	if err := os.WriteFile(path, []byte(`{"kind":"header","format_version":1}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sources := appsource.NewRegistry()
	sources.Add(&liveOwnerRelaySource{threadID: entry.Meta.ID, bootGeneration: appwire.BootGeneration(7), epoch: 3})

	err := dispatchThreadReadOver(t, cfg, sources, appwire.ThreadReadParams{
		Ref: "local:" + entry.Meta.ID, IncludeTurns: true, ItemLimit: 40, RequestGeneration: 1,
	})
	wire, ok := errors.AsType[appwire.WireError](err)
	if !ok {
		t.Fatalf("enrichment failure = %T %v, want a WireError", err, err)
	}
	data, ok := wire.Data.(appwire.HistoryReadErrorData)
	if !ok {
		t.Fatalf("enrichment failure data = %#v, want HistoryReadErrorData", wire.Data)
	}
	if data.BootGeneration != appwire.BootGeneration(7) {
		t.Fatalf("enrichment failure boot generation = %q, want the live reply's %q", data.BootGeneration, appwire.BootGeneration(7))
	}
	if data.Epoch == nil || *data.Epoch != 3 {
		t.Fatalf("enrichment failure epoch = %v, want the live reply's 3", data.Epoch)
	}
	// Only the identity changes; the diagnostic still says what went wrong with
	// the transcript.
	if data.EvenerErrorInfo != appwire.ErrorTranscriptHistoryFailed {
		t.Fatalf("enrichment failure info = %q, want %q", data.EvenerErrorInfo, appwire.ErrorTranscriptHistoryFailed)
	}
}

// TestOwnerlessLiveReplyKeepsUnknownOwner is the other direction over the same
// failing enrichment: a live reply that establishes no owner - here the hub's
// own transcript-only answer - must still stamp unknown-owner. Claiming an
// owner the read never established would clear a hold on a thread whose daemon
// may still be running.
func TestOwnerlessLiveReplyKeepsUnknownOwner(t *testing.T) {
	cfg, entry, path := seedIndexedPastSession(t,
		execution("turn_m1", schema.NewTurn(schema.TurnUserInput, llm.User("question"))),
	)
	if err := os.WriteFile(path, []byte(`{"kind":"header","format_version":1}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sources := appsource.NewRegistry()
	sources.Add(&liveOwnerRelaySource{threadID: entry.Meta.ID, bootGeneration: appwire.DaemonlessBootGeneration})

	err := dispatchThreadReadOver(t, cfg, sources, appwire.ThreadReadParams{
		Ref: "local:" + entry.Meta.ID, IncludeTurns: true, ItemLimit: 40, RequestGeneration: 1,
	})
	requireSavedHistoryFailureStamp(t, err, appwire.UnknownOwnerBootGeneration)
}
