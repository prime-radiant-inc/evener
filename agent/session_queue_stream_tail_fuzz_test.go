//go:build serffuzz

package agent

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/serf/llm"
)

type sessionTailNilAccumulator struct{}

func (*sessionTailNilAccumulator) Process(llm.StreamEvent)        {}
func (*sessionTailNilAccumulator) Response() *llm.Response        { return nil }
func (*sessionTailNilAccumulator) PartialResponse() *llm.Response { return nil }

func FuzzSessionQueueStreamTails(f *testing.F) {
	f.Add(byte(0))
	f.Fuzz(func(t *testing.T, _ byte) {
		closed := &Session{state: SessionClosed}
		if closed.trySteerWithProvenanceAndNotify("steer", nil, "") {
			t.Fatal("closed session accepted steering")
		}
		closed.FollowUp("ignored")
		open := &Session{}
		open.FollowUp(" ")

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, _, err := open.consumeModelStream(ctx, llm.Request{}, newMsfzStream(nil))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("truncated canceled stream = %v", err)
		}

		old := newSessionStreamAccumulator
		newSessionStreamAccumulator = func() sessionStreamAccumulator { return &sessionTailNilAccumulator{} }
		t.Cleanup(func() { newSessionStreamAccumulator = old })
		_, _, err = open.consumeModelStream(context.Background(), llm.Request{}, newMsfzStream([]llm.StreamEvent{{Type: llm.StreamEventFinish}}))
		if err == nil {
			t.Fatal("finish without accumulated response succeeded")
		}
	})
}
