package agent

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

func TestGoalContinuationPersistsDisplayAndModelInput(t *testing.T) {
	t.Parallel()
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	dir := t.TempDir()
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sess.Close)
	path := filepath.Join(dir, "goal.transcript.jsonl")
	writer, err := transcript.NewWriter(path, transcript.Header{SessionID: sess.ID()})
	if err != nil {
		t.Fatal(err)
	}
	sess.attachTranscript(writer)
	const objective = "d27552e0-5245-49e0-819d-4d8876602c94"
	const input = "e83bb9b1-7447-4fa2-9dba-e4cbfce61a17"
	const turnID = "turn_goal_1"
	sess.getOrCreateGoalStore().Set(objective, time.Now())
	stop := drainEvents(sess)
	sess.acceptContinuationInput(context.Background(), input, turnID)
	sess.mu.Lock()
	live := sess.history[len(sess.history)-1]
	sess.mu.Unlock()
	sess.Close()
	evs := stop()
	var notice events.GoalContinuationData
	for _, ev := range evs {
		if ev.Kind == events.EventGoalContinuation {
			notice = ev.Data.(events.GoalContinuationData)
		}
	}
	if notice.Text == "" || notice.StableTurnID != turnID {
		t.Fatal("continuation event lost its notice or reserved turn identity")
	}
	_, entries, _, err := readTranscript(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("persisted %d entries, want one continuation", len(entries))
	}
	persisted := entries[0].Turn
	if !reflect.DeepEqual(live, persisted) {
		t.Fatal("persisted continuation differs from live model history")
	}
	if persisted.Kind != schema.TurnSteering || persisted.Message.Text() != input {
		t.Fatal("continuation model input did not survive persistence")
	}
	if persisted.GoalContinuation == nil || persisted.GoalContinuation.Text != notice.Text || persisted.StableTurnID != notice.StableTurnID {
		t.Fatal("persisted continuation lost the live display notice or turn identity")
	}
}
