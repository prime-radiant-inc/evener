//go:build serffuzz

package agent

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/clock"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// FuzzSessionCompactionLifecycle drives the two Session-owned forced
// compaction entrances: an explicit idle Compact call and a compact-tool request
// consumed at a turn tail. The context manager itself has focused fuzz coverage;
// this target covers the Session lifecycle around it: copying history, emitting
// events, consuming the one-shot request, resetting the self-compact latch, and
// handing pinned notes and active goals forward.
//
// The only model boundary is an agenttest.ScriptedAdapter used by the real
// summarizer. The session uses a FakeClock and DenyEnv with an empty StateDir, so
// the target cannot use a provider, filesystem, network, shell, or process.
//
// Oracles:
//   - compaction reduces a history larger than PreserveRecentTurns and uses the
//     scripted summarizer;
//   - a pending force request is consumed exactly once and never leaks into the
//     next round;
//   - a pinned note is handed off exactly once and then cleared;
//   - an active goal remains active and is re-injected as steering;
//   - every compaction resets the self-compact nudge latch and emits a checkpoint
//     event; and
//   - rerunning the same byte program yields the same observable trace.
//
// This is intentionally a new target rather than an extension of
// FuzzLifecycleSequence: its state model distinguishes the explicit Compact API
// from the tool-round request tail, an invariant the broad turn-loop artifact
// does not retain.
func FuzzSessionCompactionLifecycle(f *testing.F) {
	for _, seed := range [][]byte{
		{0, 0, 0, 0, 0, 1}, // direct compaction, no handoff state
		{1, 1, 1, 1, 2, 3}, // pending request, pinned note, active goal
		{2, 2, 0, 1, 4, 5}, // direct compaction, pinned note
		{3, 0, 1, 1, 6, 7}, // pending request, active goal
		{},
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		program := sclDecode(data)
		first := sclRun(t, program)
		second := sclRun(t, program)
		if first != second {
			t.Fatalf("compaction lifecycle was not deterministic:\nfirst=%+v\nsecond=%+v", first, second)
		}
	})
}

type sclProgram struct {
	forceRequest bool
	note         string
	withGoal     bool
	historyLen   int
	instructions string
	seed         uint64
}

func sclDecode(data []byte) sclProgram {
	byteAt := func(i int) byte {
		if i >= len(data) {
			return 0
		}
		return data[i]
	}
	notes := []string{"", "SCL retain API boundary", "SCL retain migration state"}
	instructions := []string{"", "retain exact facts", "prefer concise checkpoint"}
	p := sclProgram{
		forceRequest: byteAt(0)&1 != 0,
		note:         notes[int(byteAt(1))%len(notes)],
		withGoal:     byteAt(2)&1 != 0,
		historyLen:   8 + int(byteAt(3)%16),
		instructions: instructions[int(byteAt(4))%len(instructions)],
	}
	for i := 0; i < 8; i++ {
		p.seed |= uint64(byteAt(5+i)) << (8 * i)
	}
	return p
}

type sclTrace struct {
	HistoryLen        int
	SummaryRequests   int
	CheckpointEvents  int
	NoteSteering      int
	GoalSteering      int
	NoteEvents        int
	GoalStatus        string
	GoalPresent       bool
	ForcePending      bool
	NudgeStillLatched bool
}

const sclGoalObjective = "SCL preserve active goal"

func sclRun(t *testing.T, p sclProgram) sclTrace {
	t.Helper()
	sess, adapter := sclNewSession(t, p)

	before := sclSeedHistory(sess, p.historyLen)
	if p.note != "" {
		sess.setPinnedNote(p.note)
	}
	if p.withGoal {
		started, err := sess.SetGoal(context.Background(), sclGoalObjective)
		if err != nil || started {
			t.Fatalf("SetGoal = started:%v err:%v, want armed without kick", started, err)
		}
	}
	// Each compaction path must clear this per-cycle latch through the shared
	// compaction emit function, rather than relying on a caller to reset it.
	sess.mu.Lock()
	sess.nudgedSinceCompact = true
	sess.mu.Unlock()

	if p.forceRequest {
		if err := sess.requestForceCompact(p.instructions); err != nil {
			t.Fatalf("requestForceCompact: %v", err)
		}
		sess.applyPendingForceCompact(context.Background())
	} else if err := sess.Compact(context.Background()); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	history := sclHistory(sess)
	if len(history) >= before {
		t.Fatalf("compaction did not reduce history: before=%d after=%d", before, len(history))
	}
	if got := len(adapter.Requests()); got == 0 {
		t.Fatal("compaction never reached the scripted summarizer")
	}
	if _, ok := sess.takeForceRequest(); ok {
		t.Fatal("compaction left a force request pending for a later round")
	}
	sess.mu.Lock()
	forcePending := sess.forceRequested || sess.pendingInstructions != ""
	nudgeStillLatched := sess.nudgedSinceCompact
	sess.mu.Unlock()
	if forcePending {
		t.Fatal("compaction retained force-request state")
	}
	if nudgeStillLatched {
		t.Fatal("compaction did not reset the self-compact nudge latch")
	}

	noteSteering := sclCountSteering(history, renderNoteHandoff(p.note))
	if p.note == "" {
		if noteSteering != 0 {
			t.Fatalf("empty note produced %d note handoffs", noteSteering)
		}
	} else {
		if got := sess.PinnedNote(); got != "" {
			t.Fatalf("pinned note survived its one-shot compaction handoff: %q", got)
		}
		if noteSteering != 1 {
			t.Fatalf("note handoffs = %d, want 1", noteSteering)
		}
	}

	goalSteering := sclCountSteeringContaining(history, sclGoalObjective)
	status, _, goalPresent := sess.GoalStatus()
	if p.withGoal {
		if !goalPresent || status != "active" {
			t.Fatalf("compaction changed active goal: present=%v status=%q", goalPresent, status)
		}
		if goalSteering != 1 {
			t.Fatalf("goal steering turns = %d, want 1", goalSteering)
		}
	} else if goalPresent || goalSteering != 0 {
		t.Fatalf("compaction invented goal state: present=%v steering=%d", goalPresent, goalSteering)
	}

	// All events fit comfortably in the Session's fixed buffer for this compact
	// fixture, so close before draining to avoid a concurrent collector or timing
	// oracle.
	sess.Close()
	checkpointEvents, noteEvents := sclCompactionEvents(sess, p.note)
	if checkpointEvents == 0 {
		t.Fatal("compaction emitted no checkpoint event")
	}
	if p.note == "" {
		if noteEvents != 0 {
			t.Fatalf("empty note emitted %d steering events", noteEvents)
		}
	} else if noteEvents != 1 {
		t.Fatalf("note steering events = %d, want 1", noteEvents)
	}

	return sclTrace{
		HistoryLen:        len(history),
		SummaryRequests:   len(adapter.Requests()),
		CheckpointEvents:  checkpointEvents,
		NoteSteering:      noteSteering,
		GoalSteering:      goalSteering,
		NoteEvents:        noteEvents,
		GoalStatus:        status,
		GoalPresent:       goalPresent,
		ForcePending:      forcePending,
		NudgeStillLatched: nudgeStillLatched,
	}
}

func sclNewSession(t *testing.T, p sclProgram) (*Session, *agenttest.ScriptedAdapter) {
	t.Helper()
	adapter := &agenttest.ScriptedAdapter{
		Provider: "openai",
		Responder: func(llm.Request) llm.Response {
			return agenttest.FinalResponse("SCL scripted summary")
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	clk := agenttest.NewFakeClock()
	cfg := SessionConfig{
		NoProjectPrompts: true,
		clock:            clk,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
			environmentInfo:     sclEnvironmentInfo,
		},
	}
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), &agenttest.DenyEnv{
		WorkDir: t.TempDir(),
		Seed:    p.seed,
	}, cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	return sess, adapter
}

func sclEnvironmentInfo(env execenv.ExecutionEnvironment, clk clock.Clock) schema.EnvironmentInfo {
	return schema.EnvironmentInfo{
		WorkingDir: env.WorkingDirectory(),
		Platform:   "compaction-fuzz",
		OSVersion:  "deny-env",
		Today:      clk.Now().UTC().Format("2006-01-02"),
	}
}

func sclSeedHistory(sess *Session, n int) int {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	for i := 0; i < n; i++ {
		sess.history = append(sess.history, schema.NewTurn(schema.TurnUserInput, llm.User("SCL history turn")))
	}
	return len(sess.history)
}

func sclHistory(sess *Session) []schema.Turn {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return append([]schema.Turn(nil), sess.history...)
}

func sclCountSteering(history []schema.Turn, text string) int {
	count := 0
	for _, turn := range history {
		if turn.Kind == schema.TurnSteering && turn.Message.Text() == text {
			count++
		}
	}
	return count
}

func sclCountSteeringContaining(history []schema.Turn, needle string) int {
	count := 0
	for _, turn := range history {
		if turn.Kind == schema.TurnSteering && strings.Contains(turn.Message.Text(), needle) {
			count++
		}
	}
	return count
}

func sclCompactionEvents(sess *Session, note string) (checkpointEvents, noteEvents int) {
	wantNote := renderNoteHandoff(note)
	for event := range sess.Events() {
		switch data := event.Data.(type) {
		case events.ContextCompactionData:
			if data.Layer == "checkpoint" {
				checkpointEvents++
			}
		case events.SteeringInjectedData:
			if note != "" && data.Text == wantNote {
				noteEvents++
			}
		}
	}
	return checkpointEvents, noteEvents
}
