package agent

import (
	"context"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// Attention reaches a session from delivery goroutines at any time. While an
// execution runs it joins that turn; once the turn's completion is recorded it
// takes a delivery turn of its own, even when it was sent while the
// completion was being recorded. The choice is made inside the append lock.
func TestAsyncAttentionJoinsTheRunningTurnAndNeverACompletedOne(t *testing.T) {
	s, adapter := newExecutionSession(t)
	inCall := make(chan struct{})
	release := make(chan struct{})
	adapter.script(func(context.Context) (llm.Response, error) {
		close(inCall)
		<-release
		return communicateResponse(true, "done"), nil
	})

	var lateDone sync.WaitGroup
	completing := make(chan struct{})
	s.attachedTranscript().OnRecorded(func(rec transcript.Record) {
		if rec.Turn.Kind == schema.TurnCompletion {
			close(completing)
		}
	})

	processed := make(chan error, 1)
	go func() {
		_, err := s.ProcessInput(context.Background(), "work", nil)
		processed <- err
	}()
	<-inCall
	if _, err := s.appendDelegateNotificationDurably("delegate:during", "arrived mid-turn"); err != nil {
		t.Fatal(err)
	}
	close(release)
	<-completing
	// Sent while the completion was being recorded: the append lock orders it
	// after the completion.
	lateDone.Go(func() {
		if _, err := s.appendDelegateNotificationDurably("delegate:late", "arrived at the turn's end"); err != nil {
			t.Error(err)
		}
	})
	if err := <-processed; err != nil {
		t.Fatal(err)
	}
	lateDone.Wait()

	var execution string
	byAttention := map[string]schema.Turn{}
	for _, turn := range transcriptTurnsOf(t, s) {
		if turn.Kind == schema.TurnUserInput {
			execution = turn.TurnID
		}
		if turn.AttentionID != "" {
			byAttention[turn.AttentionID] = turn
		}
	}
	if during := byAttention["delegate:during"]; during.TurnID != execution || during.TurnKind != "" {
		t.Fatalf("mid-turn attention = %q (%q), want the running turn %q", during.TurnID, during.TurnKind, execution)
	}
	if late := byAttention["delegate:late"]; late.TurnID == execution || late.TurnKind != schema.TurnSpanDelivery {
		t.Fatalf("late attention = %q (%q), want a delivery turn of its own", late.TurnID, late.TurnKind)
	}
}
