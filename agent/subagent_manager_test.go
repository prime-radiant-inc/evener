package agent

import (
	"sync"
	"testing"

	"primeradiant.com/serf/agent/events"
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
