package server

import (
	"testing"

	"primeradiant.com/evener/appwire"
)

// askMispairEnvelopeSource is stubThreadEnvelopeSource plus the PendingAskCalls
// half of the ask facet, modeling a session whose pending set grows BETWEEN the
// envelope's two ask samples. The production source (agent.Session) takes the
// session lock independently in PendingQuestion and in PendingAskCalls
// (agent/session_tools_ask.go), and refreshFacets samples them as two separate
// calls (server/thread_envelope.go), so an append between the two acquisitions
// is an ordering production cannot prevent. This stub places that append
// deterministically: the first PendingQuestion sample sees the pre-append set,
// every later ask sample sees the post-append set.
type askMispairEnvelopeSource struct {
	stubThreadEnvelopeSource
	appended bool
}

func (s *askMispairEnvelopeSource) PendingAskSnapshot() (*appwire.PendingQuestion, *appwire.PendingAskState) {
	// An append cannot intervene inside the one source acquisition.
	s.appended = true
	return s.PendingQuestion(), s.PendingAskCalls()
}

func (s *askMispairEnvelopeSource) PendingQuestion() *appwire.PendingQuestion {
	if s.appended {
		// The first question is unchanged by the append; the count and the
		// revision are what move.
		return &appwire.PendingQuestion{Question: "Which datastore for the ingest path?", Count: 2}
	}
	s.appended = true
	return &appwire.PendingQuestion{Question: "Which datastore for the ingest path?", Count: 1}
}

func (s *askMispairEnvelopeSource) PendingAskCalls() *appwire.PendingAskState {
	if s.appended {
		return &appwire.PendingAskState{
			Version:    1,
			InstanceID: "root",
			Revision:   2,
			Calls: []appwire.PendingAskCall{
				{TurnID: "turn-ask-1", CallID: "call-ask-1"},
				{TurnID: "turn-ask-2", CallID: "call-ask-2"},
			},
		}
	}
	return &appwire.PendingAskState{
		Version:    1,
		InstanceID: "root",
		Revision:   1,
		Calls:      []appwire.PendingAskCall{{TurnID: "turn-ask-1", CallID: "call-ask-1"}},
	}
}

func TestThreadEnvelopeOlderAskSampleKeepsTheNewerExcerpt(t *testing.T) {
	t.Parallel()
	newer := &appwire.PendingAskState{Version: 1, InstanceID: "root", Revision: 2,
		Calls: []appwire.PendingAskCall{{TurnID: "turn-2", CallID: "call-2"}}}
	older := &appwire.PendingAskState{Version: 1, InstanceID: "root", Revision: 1,
		Calls: []appwire.PendingAskCall{{TurnID: "turn-1", CallID: "call-1"}}}
	want := &appwire.PendingQuestion{Count: 2}
	envelope := threadEnvelope{PendingQuestion: want, PendingAsk: newer}
	envelope.assign(facetAsk, threadEnvelope{PendingQuestion: &appwire.PendingQuestion{Count: 1}, PendingAsk: older}, false, false, false)
	if envelope.PendingQuestion != want || envelope.PendingAsk != newer {
		t.Fatalf("an older sample replaced part of the newer ask snapshot: %+v", envelope)
	}
}

func TestThreadEnvelopeOlderBootKeepsTheNewerAskSample(t *testing.T) {
	t.Parallel()
	want := &appwire.PendingQuestion{Count: 2}
	state := &appwire.PendingAskState{InstanceID: "root", BootGeneration: "2", Revision: 1}
	envelope := threadEnvelope{PendingQuestion: want, PendingAsk: state}
	envelope.assign(facetAsk, threadEnvelope{PendingQuestion: &appwire.PendingQuestion{Count: 1}, PendingAsk: &appwire.PendingAskState{InstanceID: "root", BootGeneration: "1", Revision: 100}}, false, false, false)
	if envelope.PendingQuestion != want || envelope.PendingAsk != state {
		t.Fatalf("older boot replaced newer ask sample: %+v", envelope)
	}
}

func TestThreadEnvelopeSourceReplacementClearsBothAskHalves(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerConfig{})
	s.appEnvelope.PendingQuestion = &appwire.PendingQuestion{Count: 1}
	s.appEnvelope.PendingAsk = &appwire.PendingAskState{InstanceID: "old", Revision: 1}
	s.SetThreadEnvelopeSource(&stubThreadEnvelopeSource{})
	if s.appEnvelope.PendingQuestion != nil || s.appEnvelope.PendingAsk != nil {
		t.Fatalf("source replacement retained part of the retired ask snapshot: question=%+v state=%+v", s.appEnvelope.PendingQuestion, s.appEnvelope.PendingAsk)
	}
}

// TestThreadEnvelopeAskFacetSamplesOneSet asserts the atomic-snapshot contract
// (spec 4 scope 2, round-7 publication-sampling finding): the published row's
// excerpt and pending-ask state must describe ONE pending set. The sampler
// samples the excerpt and the state in two separately-locked calls, so an ask
// appended between them mispairs the row — the count-one excerpt of Q1 with
// Q2's revision — and publication installs the excerpt unconditionally while
// guarding only the state's revision advance (threadEnvelope.assign). The
// fixture asks one question per call, so a coherent snapshot has
// excerpt-count == len(state.calls).
//
// RED on today's two-call sampling: the row publishes the mixed pair.
func TestThreadEnvelopeAskFacetSamplesOneSet(t *testing.T) {
	t.Parallel()
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "root")
	src := &askMispairEnvelopeSource{}
	// Install the source the way publishEnvelope installs a plain stub
	// (SetThreadEnvelopeSource + RefreshThreadEnvelope). That pass is the
	// mispairing one — its PendingQuestion sample runs before the append and
	// its PendingAskCalls sample after — and a cold read landing in the window
	// before the next event-driven refresh sees the mixed pair (the next
	// refresh self-corrects, which is why the read must happen now).
	srv.SetThreadEnvelopeSource(src)
	srv.RefreshThreadEnvelope()

	got := readThreadOverWire(t, srv, "local:root").Evener
	if got.PendingQuestion == nil || got.PendingAsk == nil {
		t.Fatalf("published row lost the ask facet: question=%v ask=%v", got.PendingQuestion, got.PendingAsk)
	}
	if got.PendingQuestion.Count == 1 && (len(got.PendingAsk.Calls) != 1 || got.PendingAsk.Revision != 1) {
		t.Fatalf("published row mispairs the ask facet: count-one excerpt with %d calls at revision %d — the excerpt was sampled before the append and the state after; one atomic snapshot must publish one set",
			len(got.PendingAsk.Calls), got.PendingAsk.Revision)
	}
	if got.PendingQuestion.Count == 2 && (len(got.PendingAsk.Calls) != 2 || got.PendingAsk.Revision != 2) {
		t.Fatalf("published row mispairs the ask facet: count-two excerpt with %d calls at revision %d", len(got.PendingAsk.Calls), got.PendingAsk.Revision)
	}
}
