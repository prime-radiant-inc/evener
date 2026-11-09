package server

import (
	"context"
	"slices"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/appwire"
)

type conversationEnvelopeSource struct {
	stubThreadEnvelopeSource
	sample func() *appwire.ConversationProvenanceState
	calls  atomic.Int32
}

func (s *conversationEnvelopeSource) ConversationProvenance() *appwire.ConversationProvenanceState {
	s.calls.Add(1)
	return s.sample()
}

func TestConversationProvenance_MaterializedReadAndStaleSampleFence(t *testing.T) {
	s := NewServer(ServerConfig{})
	s.SetAppIdentity("local", "thread")
	source := &conversationEnvelopeSource{sample: func() *appwire.ConversationProvenanceState {
		return &appwire.ConversationProvenanceState{Version: 1, Authoritative: true, InstanceID: "thread", Revision: 1}
	}}
	s.SetThreadEnvelopeSource(source)
	s.RefreshThreadEnvelope()
	read := readThreadOverWire(t, s, "local:thread")
	if read.Evener.ConversationProvenance == nil {
		t.Fatal("materialized thread/read dropped authoritative provenance")
	}
	calls := source.calls.Load()
	readThreadOverWire(t, s, "local:thread")
	if source.calls.Load() != calls {
		t.Fatal("subscription capture called live session")
	}
	parked := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	source.sample = func() *appwire.ConversationProvenanceState {
		if source.calls.Load() == calls+1 {
			close(parked)
			<-release
			return &appwire.ConversationProvenanceState{Version: 1, Authoritative: true, InstanceID: "thread", Revision: 2}
		}
		return &appwire.ConversationProvenanceState{Version: 1, Authoritative: true, InstanceID: "thread", Revision: 3, AdmittedInputs: []string{"new-input"}}
	}
	go func() { s.refreshFacets(facetQueue); close(done) }()
	<-parked
	s.refreshFacets(facetQueue)
	close(release)
	<-done
	read = readThreadOverWire(t, s, "local:thread")
	if read.Evener.ConversationProvenance.Revision != 3 || len(read.Evener.ConversationProvenance.AdmittedInputs) != 1 {
		t.Fatalf("stale refresh erased newer admission: %+v", read.Evener.ConversationProvenance)
	}
}

func TestConversationProvenance_StartPublishesBeforeConsumption(t *testing.T) {
	sess := newMutationReplaySession(t)
	s := NewServer(ServerConfig{})
	s.SetAppIdentity("local", sess.ID())
	source := &conversationEnvelopeSource{sample: sess.ConversationProvenance}
	s.SetThreadEnvelopeSource(source)
	s.RefreshThreadEnvelope()
	s.SetRetrySafeTurnFunctions(RetrySafeTurnFunctions{Start: sess.AcceptClientMutationStart})
	cursor := s.appNotifier.CurrentSequence()
	conn := s.appServer.NewConnection("provenance-admission")
	initializeMutationRecoveryConnection(t, conn)
	response := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnStart,
		appwire.TurnStartParams{Ref: "local:" + sess.ID(), ExpectedInstanceID: sess.ID(), ClientMutationID: "before-consumption", Input: []appwire.InputItem{{Type: "text", Text: "admitted but not consumed"}}}))
	if response.Kind() != appwire.MessageResponse {
		t.Fatalf("real start rejected: %#v", response)
	}
	found := false
	for _, notification := range s.appNotifier.ReplayAfter(cursor, "") {
		if notification.Notification.Method != appwire.NotifyConversationProvenanceChanged {
			continue
		}
		p := notificationParams[appwire.ConversationProvenanceChangedParams](t, notification)
		if p.ThreadID != sess.ID() || p.Ref != "local:"+sess.ID() {
			t.Fatalf("admission control has no authoritative routing identity: %+v", p)
		}
		if p.Provenance == nil || !p.Provenance.Authoritative || !slices.Contains(p.Provenance.AdmittedInputs, "client:before-consumption") || p.Provenance.ConsumedFrontierID != "" {
			t.Fatalf("admission notification missing authoritative unconsumed input: %+v", p)
		}
		found = true
	}
	if !found {
		t.Fatal("durable eventless start emitted no provenance control notification")
	}
}
