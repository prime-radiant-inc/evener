package server

import (
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/appwire"
)

type pendingAskEnvelopeSource struct {
	stubThreadEnvelopeSource
	sample func() *appwire.PendingAskState
	calls  atomic.Int32
}

func (s *pendingAskEnvelopeSource) PendingAskCalls() *appwire.PendingAskState {
	s.calls.Add(1)
	return s.sample()
}

// This is a projection concurrency test, not a session execution test.
func TestVoicePendingAsk_MaterializedMonotonicSample(t *testing.T) {
	s := NewServer(ServerConfig{})
	s.SetAppIdentity("local", "thread")
	state := func(rev uint64, calls ...appwire.PendingAskCall) *appwire.PendingAskState {
		return &appwire.PendingAskState{Version: 1, InstanceID: "thread", Revision: rev, Calls: append([]appwire.PendingAskCall{}, calls...)}
	}
	source := &pendingAskEnvelopeSource{sample: func() *appwire.PendingAskState {
		return state(1, appwire.PendingAskCall{TurnID: "turn-a", CallID: "call-a"})
	}}
	s.SetThreadEnvelopeSource(source)
	s.RefreshThreadEnvelope()
	read := readThreadOverWire(t, s, "local:thread")
	if read.Evener.PendingAsk == nil || len(read.Evener.PendingAsk.Calls) != 1 {
		t.Fatal("thread/read dropped exact pending references")
	}
	calls := source.calls.Load()
	readThreadOverWire(t, s, "local:thread")
	if source.calls.Load() != calls {
		t.Fatal("read sampled live owner")
	}
	for _, replacement := range [][]appwire.PendingAskCall{nil, {{TurnID: "turn-b", CallID: "call-b"}}} {
		calls = source.calls.Load()
		parked, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
		oldRevision := read.Evener.PendingAsk.Revision + 1
		source.sample = func() *appwire.PendingAskState {
			if source.calls.Load() == calls+1 {
				close(parked)
				<-release
				return state(oldRevision, appwire.PendingAskCall{TurnID: "turn-a", CallID: "call-a"})
			}
			return state(oldRevision+1, replacement...)
		}
		go func() { s.refreshFacets(facetAsk); close(done) }()
		<-parked
		s.refreshFacets(facetAsk)
		close(release)
		<-done
		read = readThreadOverWire(t, s, "local:thread")
		got := read.Evener.PendingAsk
		if got.Revision != oldRevision+1 || len(got.Calls) != len(replacement) || len(got.Calls) > 0 && got.Calls[0] != replacement[0] {
			t.Fatalf("stale ask resurrected: %+v", got)
		}
	}
	// A newly installed source for the same durable session may reset revisions.
	parked, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	source.sample = func() *appwire.PendingAskState {
		close(parked)
		<-release
		return state(100, appwire.PendingAskCall{TurnID: "old", CallID: "old"})
	}
	go func() { s.refreshFacets(facetAsk); close(done) }()
	<-parked
	next := &pendingAskEnvelopeSource{sample: func() *appwire.PendingAskState { return state(0) }}
	s.SetThreadEnvelopeSource(next)
	s.refreshFacets(facetAsk)
	close(release)
	<-done
	if got := readThreadOverWire(t, s, "local:thread").Evener.PendingAsk; got == nil || got.Revision != 0 || len(got.Calls) != 0 {
		t.Fatalf("retired source beat reset revision: %+v", got)
	}
	s.SetThreadEnvelopeSource(&stubThreadEnvelopeSource{})
	s.RefreshThreadEnvelope()
	if got := readThreadOverWire(t, s, "local:thread").Evener.PendingAsk; got != nil {
		t.Fatalf("unsupported source claimed pending knowledge: %+v", got)
	}
}
