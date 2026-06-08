package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/llm"
)

// fakeEmit records the events forwarded through a manager's emit closure.
type fakeEmit struct {
	mu     sync.Mutex
	events []events.EventKind
}

func (f *fakeEmit) emit(kind events.EventKind, _ events.EventData) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, kind)
}

func (f *fakeEmit) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.events)
}

func TestSubagentManager_TrackGetRemove(t *testing.T) {
	fe := &fakeEmit{}
	m := newSubagentManager(fe.emit)

	if got := m.get("missing"); got != nil {
		t.Fatalf("get on empty manager: got %v, want nil", got)
	}

	a := &subagent{id: "a"}
	b := &subagent{id: "b"}
	m.track(a)
	m.track(b)

	if got := m.get("a"); got != a {
		t.Fatalf("get(a): got %v, want %v", got, a)
	}
	if got := m.get("b"); got != b {
		t.Fatalf("get(b): got %v, want %v", got, b)
	}

	m.remove("a")
	if got := m.get("a"); got != nil {
		t.Fatalf("get(a) after remove: got %v, want nil", got)
	}
	if got := m.get("b"); got != b {
		t.Fatalf("get(b) after removing a: got %v, want %v", got, b)
	}
}

func TestSubagentManager_DrainForCloseReturnsAllAndClears(t *testing.T) {
	fe := &fakeEmit{}
	m := newSubagentManager(fe.emit)

	want := map[string]*subagent{
		"a": {id: "a"},
		"b": {id: "b"},
		"c": {id: "c"},
	}
	for _, sub := range want {
		m.track(sub)
	}

	drained := m.drainForClose()
	if len(drained) != len(want) {
		t.Fatalf("drainForClose returned %d entries, want %d", len(drained), len(want))
	}
	seen := map[string]bool{}
	for _, sub := range drained {
		if want[sub.id] != sub {
			t.Fatalf("drainForClose returned unexpected subagent for id %q", sub.id)
		}
		seen[sub.id] = true
	}
	for id := range want {
		if !seen[id] {
			t.Fatalf("drainForClose did not return subagent %q", id)
		}
	}

	// The map must be cleared: a subsequent get returns nil and a second
	// drain returns nothing.
	for id := range want {
		if got := m.get(id); got != nil {
			t.Fatalf("get(%q) after drain: got %v, want nil", id, got)
		}
	}
	if again := m.drainForClose(); len(again) != 0 {
		t.Fatalf("second drainForClose returned %d entries, want 0", len(again))
	}
}

func TestSubagentManager_EmitClosureIsCaptured(t *testing.T) {
	fe := &fakeEmit{}
	m := newSubagentManager(fe.emit)
	m.emit(events.EventSubagentStart, nil)
	m.emit(events.EventSubagentEnd, nil)
	if got := fe.count(); got != 2 {
		t.Fatalf("captured emit forwarded %d events, want 2", got)
	}
}

func TestSubagentManager_InfosEnumeratesTracked(t *testing.T) {
	fe := &fakeEmit{}
	m := newSubagentManager(fe.emit)
	m.track(&subagent{id: "a", status: SubagentRunning, turnsUsed: 3})
	m.track(&subagent{id: "b", status: SubagentCompleted, turnsUsed: 7})

	infos := m.infos()
	if len(infos) != 2 {
		t.Fatalf("infos returned %d entries, want 2", len(infos))
	}
	byID := map[string]SubagentInfo{}
	for _, info := range infos {
		byID[info.ID] = info
	}
	if byID["a"].Status != SubagentRunning || byID["a"].TurnsUsed != 3 {
		t.Fatalf("infos[a] = %+v, want running/3", byID["a"])
	}
	if byID["b"].Status != SubagentCompleted || byID["b"].TurnsUsed != 7 {
		t.Fatalf("infos[b] = %+v, want completed/7", byID["b"])
	}
}

// TestInfos_HidesClosedByDefault proves the /status enumeration path drops closed
// records so retained-but-closed children (Task 7) don't accumulate, while a
// completed record stays visible. The closed record is constructed directly — at
// this task nothing produces one — so this exercises the filter, not the close path.
func TestInfos_HidesClosedByDefault(t *testing.T) {
	fe := &fakeEmit{}
	m := newSubagentManager(fe.emit)
	m.track(&subagent{id: "done", status: SubagentCompleted, turnsUsed: 2})
	m.track(&subagent{id: "gone", status: SubagentClosed, turnsUsed: 5})

	infos := m.infos()
	byID := map[string]SubagentInfo{}
	for _, info := range infos {
		byID[info.ID] = info
	}
	if _, ok := byID["gone"]; ok {
		t.Errorf("infos() must hide closed records; got %+v", infos)
	}
	if _, ok := byID["done"]; !ok {
		t.Errorf("infos() must keep completed records; got %+v", infos)
	}
}

// TestListAgents_IncludeClosed proves include_closed=true surfaces a closed record
// that the default filter would hide.
func TestListAgents_IncludeClosed(t *testing.T) {
	fe := &fakeEmit{}
	m := newSubagentManager(fe.emit)
	m.track(&subagent{id: "done", status: SubagentCompleted, turnsUsed: 2})
	m.track(&subagent{id: "gone", status: SubagentClosed, turnsUsed: 5})

	hidden, count := m.listAgents("01ROOT", "", false)
	if count != 1 || len(hidden) != 1 || hidden[0].ID != "done" {
		t.Fatalf("default list must hide closed; got count=%d agents=%+v", count, hidden)
	}

	shown, count := m.listAgents("01ROOT", "", true)
	byID := map[string]SubagentInfo{}
	for _, info := range shown {
		byID[info.ID] = info
	}
	if count != 2 || len(shown) != 2 {
		t.Fatalf("include_closed must surface closed; got count=%d agents=%+v", count, shown)
	}
	if _, ok := byID["gone"]; !ok {
		t.Errorf("include_closed=true must include closed record; got %+v", shown)
	}

	// status=closed implies include_closed even when the flag is false.
	closedOnly, count := m.listAgents("01ROOT", string(SubagentClosed), false)
	if count != 1 || len(closedOnly) != 1 || closedOnly[0].ID != "gone" {
		t.Fatalf("status=closed must surface only the closed record; got count=%d agents=%+v", count, closedOnly)
	}
}

// TestListAgents_RunningChildAppearsImmediately spawns a child non-blocking whose
// run blocks inside an in-flight LLM call, then drives list_agents through its
// registered tool and asserts the child shows up running with no reason and no
// result available. The full record shape (ids/task/transcript_ref/parent/timestamps)
// is asserted here too. Releasing the run via sess.Close() (deferred) drains the
// goroutine — no sleeps.
func TestListAgents_RunningChildAppearsImmediately(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	blocked := make(chan struct{})
	c.Register(&cancelBlockAdapter{name: "openai", blocked: blocked, resume: "resumed work"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	const task = "do long work"
	spawnRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "spawn_agent",
		Arguments: json.RawMessage(fmt.Sprintf(`{"task":%q}`, task)),
	})
	if spawnRes.IsError {
		t.Fatalf("spawn_agent error: %s", spawnRes.Output)
	}
	var spawned map[string]any
	if err := json.Unmarshal([]byte(spawnRes.Output), &spawned); err != nil {
		t.Fatalf("unmarshal spawn result: %v", err)
	}
	agentID := strings.TrimSpace(fmt.Sprint(spawned["agent_id"]))

	// Block until the child's run is in-flight inside the LLM call.
	<-blocked

	listRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c2",
		Name:      "list_agents",
		Arguments: json.RawMessage(`{}`),
	})
	if listRes.IsError {
		t.Fatalf("list_agents error: %s", listRes.Output)
	}

	var payload struct {
		Agents []SubagentInfo `json:"agents"`
		Count  int            `json:"count"`
	}
	if err := json.Unmarshal([]byte(listRes.Output), &payload); err != nil {
		t.Fatalf("unmarshal list result: %v (output: %s)", err, listRes.Output)
	}
	if payload.Count != 1 || len(payload.Agents) != 1 {
		t.Fatalf("list_agents count=%d agents=%d, want 1/1 (output: %s)", payload.Count, len(payload.Agents), listRes.Output)
	}
	got := payload.Agents[0]
	if got.Status != SubagentRunning {
		t.Errorf("status = %q, want running", got.Status)
	}
	if got.Reason != "" {
		t.Errorf("reason = %q, want empty for a running child", got.Reason)
	}
	if got.ResultAvailable {
		t.Errorf("result_available must be false for a running child")
	}
	// Full record shape.
	if got.ID != agentID || got.AgentID != agentID {
		t.Errorf("id=%q agent_id=%q, want both %q", got.ID, got.AgentID, agentID)
	}
	if got.Task != task {
		t.Errorf("task = %q, want %q", got.Task, task)
	}
	if got.ParentSessionID != sess.ID() {
		t.Errorf("parent_session_id = %q, want %q", got.ParentSessionID, sess.ID())
	}
	wantRef := encodeRef("", agentID)
	if got.TranscriptRef != wantRef {
		t.Errorf("transcript_ref = %q, want %q", got.TranscriptRef, wantRef)
	}
	if got.CreatedAt.IsZero() || got.StartedAt.IsZero() {
		t.Errorf("created_at/started_at must be set; got created=%v started=%v", got.CreatedAt, got.StartedAt)
	}
	if got.EndedAt != nil {
		t.Errorf("ended_at must be nil while running; got %v", got.EndedAt)
	}
}
