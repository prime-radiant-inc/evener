package agent

// Tests for steering provenance (issue #24): steering that originates from the
// human user (the steer button, or queued user input drained as steering) is
// marked Source="user" on the SteeringInjectedData event and persisted on the
// transcript turn, so UIs can render it as user speech rather than as a
// system-looking steering divider. Daemon/system nudges keep an empty Source.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

func TestSteerFromUser_EmitsUserSourceAndPersists(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// One user-sent steering message (the steer button path) and one
	// daemon/system nudge, drained together at a turn boundary.
	sess.SteerFromUser("please focus on the tests")
	sess.Steer("<SYSTEM-REMINDER>system nudge</SYSTEM-REMINDER>")
	sess.injectDrainedSteering()

	sess.mu.Lock()
	turns := append([]schema.Turn{}, sess.history...)
	sess.mu.Unlock()
	if len(turns) != 2 {
		t.Fatalf("history turns = %d, want 2", len(turns))
	}
	for i, want := range []string{events.SteeringSourceUser, ""} {
		if turns[i].Kind != schema.TurnSteering {
			t.Fatalf("turn %d kind = %s, want STEERING", i, turns[i].Kind)
		}
		if turns[i].SteeringSource != want {
			t.Fatalf("turn %d SteeringSource = %q, want %q", i, turns[i].SteeringSource, want)
		}
	}

	// The source marker must survive transcript serialization (JSONL round
	// trip) so reload/hydration can render user steering as user speech.
	raw, err := json.Marshal(turns[0])
	if err != nil {
		t.Fatalf("marshal turn: %v", err)
	}
	var back schema.Turn
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal turn: %v", err)
	}
	if back.SteeringSource != events.SteeringSourceUser {
		t.Fatalf("round-tripped SteeringSource = %q, want %q", back.SteeringSource, events.SteeringSourceUser)
	}

	sess.Close()

	var sources []string
	var texts []string
	for ev := range sess.Events() {
		if d, ok := ev.Data.(events.SteeringInjectedData); ok {
			sources = append(sources, d.Source)
			texts = append(texts, d.Text)
		}
	}
	if len(sources) != 2 {
		t.Fatalf("steering events = %d, want 2 (%v)", len(sources), texts)
	}
	if sources[0] != events.SteeringSourceUser || !strings.Contains(texts[0], "focus on the tests") {
		t.Fatalf("user steering event = {source:%q text:%q}, want source %q", sources[0], texts[0], events.SteeringSourceUser)
	}
	if sources[1] != "" || !strings.Contains(texts[1], "system nudge") {
		t.Fatalf("system steering event = {source:%q text:%q}, want empty source", sources[1], texts[1])
	}
}

func TestSteerFromUserWithImages_EmitsUserSource(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	sess.SteerFromUserWithImages("look at this", []ImageAttachment{{
		MediaType: "image/png",
		Data:      sessionPngSig,
		Name:      "shot.png",
	}})
	sess.injectDrainedSteering()
	sess.Close()

	found := false
	for ev := range sess.Events() {
		if d, ok := ev.Data.(events.SteeringInjectedData); ok {
			found = true
			if d.Source != events.SteeringSourceUser {
				t.Fatalf("Source = %q, want %q", d.Source, events.SteeringSourceUser)
			}
			if len(d.Images) != 1 {
				t.Fatalf("Images = %d, want 1", len(d.Images))
			}
		}
	}
	if !found {
		t.Fatal("expected a STEERING_INJECTED event")
	}
}

// TestDrainAsSteer_MarksUserSource covers the queued-user-input path: text the
// user typed into the queue, force-drained into the in-flight turn as
// steering, is still user speech.
func TestDrainAsSteer_MarksUserSource(t *testing.T) {
	t.Parallel()
	s := &Session{state: SessionProcessing}
	if err := s.DrainAsSteerWithInput(context.Background(), "human queued text", nil); err != nil {
		t.Fatalf("DrainAsSteerWithInput: %v", err)
	}
	got := s.drainSteeringForTurn()
	if len(got) != 1 {
		t.Fatalf("drained = %d, want 1", len(got))
	}
	if got[0].Source != events.SteeringSourceUser {
		t.Fatalf("drained steering Source = %q, want %q", got[0].Source, events.SteeringSourceUser)
	}
}

// TestSystemSteeringPaths_KeepEmptySource pins the other queue entry points
// (internal Steer callers: task nudges, hook context, job notifications, …)
// to the empty system source so they keep the steering-divider rendering.
func TestSystemSteeringPaths_KeepEmptySource(t *testing.T) {
	t.Parallel()
	s := &Session{}
	s.Steer("system nudge")
	s.SteerWithProvenance("watch nudge", nil, "")
	got := s.drainSteeringForTurn()
	if len(got) != 2 {
		t.Fatalf("drained = %d, want 2", len(got))
	}
	for i, msg := range got {
		if msg.Source != "" {
			t.Fatalf("entry %d Source = %q, want empty", i, msg.Source)
		}
	}
}
