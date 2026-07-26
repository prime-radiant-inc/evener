package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
)

// Every kind in the enum must be produced by at least one non-test call site.
// This is the net that catches a kind going stale — the failure mode the
// deleted read-only classifier rule showed, where the UI kept a rule for a
// message the daemon had stopped sending and nothing noticed.
func TestEverySteeringKindHasAProducer(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var src strings.Builder
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatal(err)
		}
		src.Write(b)
	}
	body := src.String()
	for _, kind := range events.AllSteeringKinds {
		constName := steeringKindConstName(kind)
		if !strings.Contains(body, constName) {
			t.Errorf("kind %q (events.%s) has no producer in agent/*.go", kind, constName)
		}
	}
}

// steeringKindConstName maps "tasks-done" to "SteeringKindTasksDone".
func steeringKindConstName(kind string) string {
	var out strings.Builder
	out.WriteString("SteeringKind")
	for part := range strings.SplitSeq(kind, "-") {
		if part == "" {
			continue
		}
		out.WriteString(strings.ToUpper(part[:1]))
		out.WriteString(part[1:])
	}
	return out.String()
}

func TestMaybeInjectTaskReminderReturnsItsKind(t *testing.T) {
	s := newTestSession(t)
	// Trigger 3: task_list never used, 10+ rounds in.
	s.totalRounds = 10
	text, kind := s.maybeInjectTaskReminder()
	if text == "" {
		t.Fatal("expected a reminder text")
	}
	if kind != events.SteeringKindTaskNudge {
		t.Errorf("kind = %q, want %q", kind, events.SteeringKindTaskNudge)
	}
}
