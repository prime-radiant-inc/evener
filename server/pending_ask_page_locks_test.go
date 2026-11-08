package server

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
)

func TestPendingAskPageSamplesOutsideProjectionLocks(t *testing.T) {
	t.Parallel()
	for _, replace := range []bool{false, true} {
		t.Run(map[bool]string{false: "unchanged", true: "source replaced"}[replace], func(t *testing.T) {
			s := NewServer(ServerConfig{})
			s.SetAppIdentity("local", "root")
			sampled, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			var block atomic.Bool
			state := &appwire.PendingAskState{Version: 1, InstanceID: "root", BootGeneration: "1", Revision: 1, CalllessCount: 2, Calls: []appwire.PendingAskCall{}}
			source := &pendingAskEnvelopeSource{stubThreadEnvelopeSource: stubThreadEnvelopeSource{question: &appwire.PendingQuestion{Count: 2}}, sample: func() *appwire.PendingAskState {
				if block.Load() {
					close(sampled)
					<-release
				}
				return state
			}}
			s.SetThreadEnvelopeSource(source)
			s.RefreshThreadEnvelope()
			block.Store(true)
			result := make(chan error, 1)
			go func() {
				response, err := s.handleAppThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:root", ExpectedAskRevision: &state.Revision, ExpectedAskBootGeneration: "1"})
				if err == nil && (response.PendingAskPage == nil || response.PendingAskPage.CalllessCount != 2 || response.PendingAskPage.Composites == nil || response.PendingAskPage.NextOffset != nil) {
					err = errors.New("call-less page did not preserve empty composite list and count")
				}
				result <- err
			}()
			<-sampled
			committed := make(chan struct{})
			go func() {
				s.appServer.CommitProjection(func() []appserver.SequencedNotification {
					s.mu.Lock()
					s.mu.Unlock()
					if replace {
						s.SetThreadEnvelopeSource(&stubThreadEnvelopeSource{})
					}
					return nil
				})
				close(committed)
			}()
			// A deadlock tripwire, not a throughput assertion. Completion is channel-owned.
			select {
			case <-committed:
			case <-time.After(10 * time.Second):
				t.Fatal("live sampler blocked projection commit")
			}
			unblock()
			err := <-result
			if !replace {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var wire appwire.WireError
			if !errors.As(err, &wire) {
				t.Fatalf("retired source page returned no typed refusal: %v", err)
			}
			data, ok := wire.Data.(appwire.ErrorData)
			if !ok || data.EvenerErrorInfo != appwire.ErrorAskFenceMismatch {
				t.Fatalf("retired source error=%+v", wire)
			}
		})
	}
}

func TestPendingAskPageRejectsLiveDescendant(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerConfig{})
	s.SetAppIdentity("local", "root")
	s.appDescendants["child"] = &appDescendantProjection{thread: appwire.Thread{ID: "child", Evener: appwire.EvenerThread{Ref: "local:child", Kind: "subagent"}}}
	revision := uint64(1)
	_, err := s.handleAppThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:child", ExpectedAskRevision: &revision, ExpectedAskBootGeneration: "1"})
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("descendant page was not refused: %v", err)
	}
}
