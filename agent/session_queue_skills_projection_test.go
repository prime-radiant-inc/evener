// Tests that per-entry canonical skill names ride the queue projection, so a
// client editing (or returning/draining) a queued entry can restore its skill
// selections — a queued {type:"skill"} item is otherwise unrecoverable.
package agent

import (
	"slices"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// TestClientMutationProjection_QueueEntrySkillNames queues a skill-only entry
// and a mixed text+skill entry through the real mutation endpoint: BOTH the
// event-driven QueueChangedData and the reconstructible ClientMutationProjection
// must surface each entry's canonical skill names FIFO-aligned with the other
// per-entry arrays, or an edit/return of that entry silently drops the
// selection.
func TestClientMutationProjection_QueueEntrySkillNames(t *testing.T) {
	root := t.TempDir()
	writeSkillMD(t, root, "probe", "---\nname: probe\ndescription: fixture\n---\nBODY_queue_projection")
	adapter := &agenttest.ScriptedAdapter{Provider: "anthropic", Responder: func(llm.Request) llm.Response {
		return toolCallResponse(communicateCall("done-1", "ok"))
	}}
	s := newSkillSelectionSession(t, root, adapter)
	evs, stop := captureEvents(s)
	defer stop()

	if _, err := s.AcceptClientMutationQueue(appwire.TurnQueueParams{
		ClientMutationID: "skill-only-queue-entry",
		Input:            []appwire.InputItem{{Type: "skill", Name: "probe"}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationQueue (skill-only): %v", err)
	}
	if _, err := s.AcceptClientMutationQueue(appwire.TurnQueueParams{
		ClientMutationID: "mixed-queue-entry",
		Input: []appwire.InputItem{
			{Type: "text", Text: "queued text"},
			{Type: "skill", Name: "probe"},
		},
	}); err != nil {
		t.Fatalf("AcceptClientMutationQueue (mixed): %v", err)
	}

	queue, _ := s.ClientMutationProjection()
	if len(queue.SkillNames) != 2 {
		t.Fatalf("projection SkillNames = %+v, want one entry per queued input", queue.SkillNames)
	}
	if !slices.Equal(queue.SkillNames[0], []string{"probe"}) {
		t.Fatalf("skill-only entry SkillNames = %+v, want [probe]", queue.SkillNames[0])
	}
	if !slices.Equal(queue.SkillNames[1], []string{"probe"}) {
		t.Fatalf("mixed entry SkillNames = %+v, want [probe]", queue.SkillNames[1])
	}

	// The event-driven snapshot carries the same per-entry names: a client
	// that never re-fetches the projection still learns the selections. The
	// capture goroutine drains asynchronously, so poll for the depth-2
	// snapshot rather than assuming it landed.
	var latest *events.QueueChangedData
	deadline := time.Now().Add(5 * time.Second)
	for latest == nil {
		for i := range *evs {
			if (*evs)[i].Kind == events.EventQueueChanged {
				if data, ok := (*evs)[i].Data.(events.QueueChangedData); ok && data.Depth == 2 {
					snapshot := data
					latest = &snapshot
				}
			}
		}
		if latest == nil {
			if time.Now().After(deadline) {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	if latest == nil {
		t.Fatal("no depth-2 QueueChanged event observed")
	}
	if len(latest.SkillNames) != 2 ||
		!slices.Equal(latest.SkillNames[0], []string{"probe"}) ||
		!slices.Equal(latest.SkillNames[1], []string{"probe"}) {
		t.Fatalf("QueueChangedData.SkillNames = %+v, want [[probe] [probe]]", latest.SkillNames)
	}
}
