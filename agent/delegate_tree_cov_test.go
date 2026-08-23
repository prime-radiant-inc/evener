package agent

import (
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// TestLiveGenerationOwesSteering covers liveGenerationOwesSteering
// (delegate_tree_steer.go:473-480): returns true when any pending steer
// does not carry across generation.
func TestLiveGenerationOwesSteering(t *testing.T) {
	t.Parallel()
	// No pending steers -> false.
	live := &delegateLiveState{}
	if liveGenerationOwesSteering(live) {
		t.Fatal("no pending steers should return false")
	}
	// All steers carry across generation -> false.
	live.pendingSteers = []delegateSteeringAdmission{
		{entryID: "e1", carriesAcrossGeneration: true},
		{entryID: "e2", carriesAcrossGeneration: true},
	}
	if liveGenerationOwesSteering(live) {
		t.Fatal("all carriesAcrossGeneration should return false")
	}
	// At least one steer does NOT carry across generation -> true.
	live.pendingSteers = []delegateSteeringAdmission{
		{entryID: "e1", carriesAcrossGeneration: true},
		{entryID: "e2", carriesAcrossGeneration: false},
	}
	if !liveGenerationOwesSteering(live) {
		t.Fatal("one non-carrying steer should return true")
	}
	// Single non-carrying steer -> true.
	live.pendingSteers = []delegateSteeringAdmission{
		{entryID: "e1", carriesAcrossGeneration: false},
	}
	if !liveGenerationOwesSteering(live) {
		t.Fatal("single non-carrying steer should return true")
	}
}

// TestProjectDelegatePendingSteers covers projectDelegatePendingSteers
// (delegate_tree_steer.go:397-428) for the main cases: pending steers found
// in history, pending steers not found, excluded steers skipped.
func TestProjectDelegatePendingSteers(t *testing.T) {
	t.Parallel()
	// Empty history and no pending -> empty projected, empty bound.
	projected, bound := projectDelegatePendingSteers(nil, nil, nil)
	if len(projected) != 0 || len(bound) != 0 {
		t.Fatalf("empty: projected=%d bound=%d", len(projected), len(bound))
	}

	// History with turns, no pending steers -> all turns projected.
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("hello")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("hi")),
	}
	projected, _ = projectDelegatePendingSteers(history, nil, nil)
	if len(projected) != 2 {
		t.Fatalf("no pending: projected=%d, want 2", len(projected))
	}

	// Pending steer found in history -> appended after other turns, bound.
	steerTurn := schema.NewTurn(schema.TurnSteering, llm.User("steer"))
	steerTurn.StableTurnID = "turn_steer_1"
	history = []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("hello")),
		steerTurn,
	}
	pending := []delegateSteeringAdmission{{entryID: "turn_steer_1"}}
	projected, bound = projectDelegatePendingSteers(history, pending, nil)
	// The steer should be removed from its position and appended at the end.
	if len(projected) != 2 {
		t.Fatalf("found: projected=%d, want 2", len(projected))
	}
	if projected[0].Kind != schema.TurnUserInput {
		t.Fatal("first should be user input")
	}
	if projected[1].Kind != schema.TurnSteering {
		t.Fatal("second should be the steering turn")
	}
	if _, ok := bound["turn_steer_1"]; !ok {
		t.Fatal("bound should contain turn_steer_1")
	}

	// Pending steer NOT found in history -> not projected, not bound.
	history = []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("hello")),
	}
	pending = []delegateSteeringAdmission{{entryID: "turn_missing"}}
	projected, bound = projectDelegatePendingSteers(history, pending, nil)
	if len(projected) != 1 {
		t.Fatalf("not found: projected=%d, want 1", len(projected))
	}
	if len(bound) != 0 {
		t.Fatalf("not found: bound=%d, want 0", len(bound))
	}

	// Excluded steering turn -> skipped from history.
	excludedSteer := schema.NewTurn(schema.TurnSteering, llm.User("excluded"))
	excludedSteer.StableTurnID = "turn_excluded"
	history = []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("hello")),
		excludedSteer,
	}
	projected, _ = projectDelegatePendingSteers(history, nil, map[string]struct{}{"turn_excluded": {}})
	if len(projected) != 1 {
		t.Fatalf("excluded: projected=%d, want 1", len(projected))
	}
	if projected[0].Kind != schema.TurnUserInput {
		t.Fatal("excluded steer should be removed from projection")
	}
}
