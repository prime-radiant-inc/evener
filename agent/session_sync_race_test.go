package agent

import (
	"context"
	"sync"
	"testing"

	"primeradiant.com/serf/llm"
)

// hammerSetters runs SetModel/SetReasoningEffort/DetailedStatus concurrently from
// several goroutines while driving ProcessInput in a loop, then stops the loop.
// Used by the PRI-1958 races to exercise the per-round reads against the setters.
func hammerSetters(t *testing.T, sess *Session) {
	t.Helper()
	stop := make(chan struct{})
	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		for {
			select {
			case <-stop:
				return
			default:
			}
			_, _ = sess.ProcessInput(context.Background(), "hi", nil)
		}
	}()
	var setters sync.WaitGroup
	for _, fn := range []func(){
		func() { sess.SetModel("gpt-5.2") },
		func() { sess.SetReasoningEffort("high") },
		func() { _ = sess.DetailedStatus() },
	} {

		setters.Add(1)
		go func() {
			defer setters.Done()
			for i := 0; i < 150; i++ {
				fn()
			}
		}()
	}
	setters.Wait()
	close(stop)
	<-loopDone
}

// TestSession_ProcessInput_NoRaceWithSetters drives the non-streaming round path
// concurrently with SetModel/SetReasoningEffort/DetailedStatus. SetModel mutates
// the profile, the prompt/tool-def caches, and s.cfg under s.mu while the round
// reads them (PRI-1958 A2/A4). Run under -race: RED before the per-round snapshot
// fix, GREEN after.
func TestSession_ProcessInput_NoRaceWithSetters(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"}) // Stream unsupported → Complete path
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	hammerSetters(t, sess)
}

// TestSession_ProcessInputStreaming_NoRaceWithSetters drives the STREAMING model
// path (consumeModelStream → canonicalToolName, the production default) with a
// tool call each round, concurrently with the setters. This covers the A4 reads
// the non-streaming test misses (s.profile read inside canonicalToolName during
// the stream). RED before the currentProfile() accessor fix, GREEN after.
func TestSession_ProcessInputStreaming_NoRaceWithSetters(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&streamingAdapter{
		name: "openai",
		streamScript: func(st *llm.ChanStream) {
			st.Send(llm.StreamEvent{Type: llm.StreamEventStreamStart})
			st.Send(llm.StreamEvent{
				Type:     llm.StreamEventToolCallStart,
				ToolCall: &llm.ToolCallData{ID: "tc1", Name: "read_file"},
			})
			st.Send(llm.StreamEvent{
				Type:     llm.StreamEventToolCallEnd,
				ToolCall: &llm.ToolCallData{ID: "tc1", Name: "read_file", Arguments: []byte(`{"path":"x"}`)},
			})
			finish := llm.FinishReason{Reason: llm.FinishReasonStop}
			st.Send(llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &finish})
		},
	})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxToolRoundsPerInput: 2, // bound the tool loop; each round canonicalizes the tool name
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	hammerSetters(t, sess)
}

// TestSpawnAgent_NoRaceWithSetReasoningEffort covers the s.cfg read in spawnAgent
// (PRI-1958): spawnAgent copies s.cfg during a turn while SetReasoningEffort writes
// s.cfg.ReasoningEffort under s.mu. RED before snapshotting the copy under s.mu.
func TestSpawnAgent_NoRaceWithSetReasoningEffort(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{MaxSubagentDepth: 1})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 30; i++ {
			_, _ = sess.spawnAgent(context.Background(), "task", "", "", 0, "", "", nil, nil)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 300; i++ {
			sess.SetReasoningEffort("high")
		}
	}()
	wg.Wait()
}
