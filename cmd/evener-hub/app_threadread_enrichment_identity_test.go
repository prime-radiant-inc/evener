package hub

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/llm"
)

// enrichmentLiveSource answers a live read with the daemon's own boot token and
// no item candidates, which is what sends the read into the saved-history
// enrichment branch after a daemon has already answered.
type enrichmentLiveSource struct {
	*scriptedAppSource
	bootGeneration string
}

func (s *enrichmentLiveSource) ReadThread(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	s.readParams = append(s.readParams, params)
	response := appwire.ThreadReadResponse{
		Thread:         s.thread,
		BootGeneration: s.bootGeneration,
	}
	if !params.IncludeTurns {
		response.Thread.Turns = nil
	}
	return response, nil
}

// A structured history failure raised while ENRICHING a live answer must carry
// the answering daemon's own token, not `daemonless`.
//
// This drives the production handler rather than the stamping helper: the past
// reader stamps its own refusal `daemonless` before the live branch sees it, so
// a helper that only rewrites a plain ErrorData leaves the false absence claim
// standing, and a client that trusts it retires a hold on a wait the live
// daemon is still sampling.
func TestLiveEnrichmentFailureAnswersTheDaemonsOwnToken(t *testing.T) {
	wireRepairsForTest(t)
	feed, _, _ := installRepairSeam(t)
	cfg, entry, path := seedIndexedPastSession(t,
		execution("turn_m1", schema.NewTurn(schema.TurnUserInput, llm.User("question"))),
		execution("turn_m1", schema.NewTurn(schema.TurnAssistant, llm.Assistant("answer"))),
	)
	staleSidecarForTest(t, path)
	// Park the background repair so the sidecar keeps refusing.
	_ = feed

	ref := "local:" + entry.Meta.ID
	live := &enrichmentLiveSource{
		scriptedAppSource: &scriptedAppSource{
			id:     appwire.LocalSourceID,
			thread: appwire.Thread{ID: entry.Meta.ID, Evener: appwire.EvenerThread{Ref: ref, InstanceID: "live-instance"}},
		},
		bootGeneration: "4",
	}
	sources := appsource.NewRegistry()
	sources.Add(live)
	server := newHubAppServer(cfg, sources)
	value, err := server.Router().Dispatch(t.Context(), appwire.Request{
		ID:     appwire.NewIntID(1),
		Method: appwire.MethodThreadRead,
		Params: mustPagingJSON(t, appwire.ThreadReadParams{
			Ref: ref, IncludeTurns: true, ItemLimit: 40, RequestGeneration: 1,
		}),
	})
	if err == nil {
		response, _ := value.(appwire.ThreadReadResponse)
		t.Fatalf("read succeeded with %d turns, want the enrichment failure", len(response.Thread.Turns))
	}
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) {
		t.Fatalf("read error = %T %v, want appwire.WireError", err, err)
	}
	data, ok := wireErr.Data.(appwire.HistoryReadErrorData)
	if !ok {
		t.Fatalf("read error data = %#v, want HistoryReadErrorData", wireErr.Data)
	}
	if data.BootGeneration != "4" {
		t.Fatalf(
			"failure identity = %q, want the answering daemon's own 4: the read reached a daemon, so the hub must not claim none is running",
			data.BootGeneration,
		)
	}
	if data.EvenerErrorInfo == "" {
		t.Fatal("failure carried no history diagnostic: the client must still learn the read failed")
	}
}
