package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// Manager and human admissions share the durable mutation owner. A retry after
// transport response loss may change its authenticated caller, not its actor.
func TestManagerInputActorRetryAndPendingQuestion(t *testing.T) {
	dir := t.TempDir()
	s := newQueuePersistTestSession(t, dir)
	manager := schema.SessionActor{Kind: "manager", ManagerRef: "local:manager"}
	human := schema.SessionActor{Kind: "human"}
	input := []appwire.InputItem{{Type: "text", Text: "answer"}, {Type: "skill", Name: "pkg:probe"}}
	p := appwire.TurnStartParams{ClientMutationID: "manager-answer", Input: input}
	first, err := s.AcceptClientMutationStartAs(p, manager)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.AcceptClientMutationStartAs(p, human)
	if err != nil {
		t.Fatal(err)
	}
	if replay.Receipt.ClientMutationID != first.Receipt.ClientMutationID || replay.Turn.ID != first.Turn.ID {
		t.Fatalf("retry created another input: first=%+v replay=%+v", first, replay)
	}
	snap := s.clientMutations.snapshot()
	if len(snap.Journal) != 1 || snap.Journal[p.ClientMutationID].Actor != manager {
		t.Fatalf("accepted actor or count changed on retry: %+v", snap.Journal)
	}
	if got := snap.PendingExecutions[p.ClientMutationID].Actor; got.Kind != manager.Kind || got.ManagerRef != manager.ManagerRef {
		t.Fatalf("pending actor = %+v", got)
	}
	id := s.ID()
	s.Close()
	restored := restoreQueuePersistTestSession(t, dir, id)
	defer restored.Close()
	if got := restored.clientMutations.snapshot().Journal[p.ClientMutationID].Actor; got != manager {
		t.Fatalf("reloaded actor = %+v", got)
	}
}

func TestManagerInputNotificationDoesNotAnswerPendingQuestion(t *testing.T) {
	adapter := newMessagingAdapter("WORKER-BRIEF")
	adapter.script("root",
		func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(askUserCall("ask1", askUserArgsValid()))
		},
		func(llm.Request) llm.Response { return agenttest.FinalResponse("answer accepted") },
	)
	root := newMessagingRoot(t, adapter, nil)
	ctx := context.Background()
	if _, err := root.ProcessInput(ctx, "ask me", nil); err != nil {
		t.Fatal(err)
	}
	if root.State() != SessionAwaiting {
		t.Fatalf("not awaiting question: %s", root.State())
	}
	before := len(adapter.requestsFor("root"))
	if _, err := root.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
		t.Fatal(err)
	}
	if root.State() != SessionAwaiting || len(adapter.requestsFor("root")) != before {
		t.Fatal("notification answered pending question")
	}
	p := appwire.TurnStartParams{ClientMutationID: "reply", Input: []appwire.InputItem{{Type: "text", Text: "the answer"}}}
	if _, err := root.AcceptClientMutationStartAs(p, schema.SessionActor{Kind: "manager", ManagerRef: "local:manager"}); err != nil {
		t.Fatal(err)
	}
	if _, ran, err := root.ProcessClientMutationStart(ctx, nil); err != nil || !ran {
		t.Fatalf("Manager reply: ran=%v err=%v", ran, err)
	}
	if root.State() == SessionAwaiting || len(adapter.requestsFor("root")) <= before {
		t.Fatal("reply did not answer pending question")
	}
	root.mu.Lock()
	defer root.mu.Unlock()
	found := false
	for _, turn := range root.history {
		if turn.Kind == schema.TurnUserInput && turn.ClientMutationID == "reply" {
			found = turn.Actor.Kind == "manager" && turn.Actor.ManagerRef == "local:manager"
		}
	}
	if !found {
		t.Fatal("accepted Manager answer lost its input-turn actor")
	}
}

func TestManagerInputQueueSteerAndHumanRace(t *testing.T) {
	dir := t.TempDir()
	s := newQueuePersistTestSession(t, dir)
	defer s.Close()
	setTestClientMutationActiveTurn(t, s, "turn_1")
	manager := schema.SessionActor{Kind: "manager", ManagerRef: "local:manager"}
	human := schema.SessionActor{Kind: "human"}
	q := appwire.TurnQueueParams{ClientMutationID: "queue-race", Input: []appwire.InputItem{{Type: "command", Name: "pwd"}}}
	var wg sync.WaitGroup
	wg.Add(2)
	errs := make(chan error, 2)
	go func() { defer wg.Done(); _, err := s.AcceptClientMutationQueueAs(q, human); errs <- err }()
	go func() { defer wg.Done(); _, err := s.AcceptClientMutationQueueAs(q, manager); errs <- err }()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	snap := s.clientMutations.snapshot()
	if len(snap.InputQueue) != 1 || len(snap.Journal) != 1 {
		t.Fatalf("concurrent queue duplicated: %+v", snap)
	}
	first := snap.Journal[q.ClientMutationID].Actor
	if first.Kind != "human" && first != manager {
		t.Fatalf("invalid accepted actor: %+v", first)
	}
	if snap.InputQueue[0].Actor != first {
		t.Fatalf("queue actor = %+v, journal = %+v", snap.InputQueue[0].Actor, first)
	}
	other := human
	if first.Kind == "human" {
		other = manager
	}
	if _, err := s.AcceptClientMutationQueueAs(q, other); err != nil {
		t.Fatal(err)
	}
	if got := s.clientMutations.snapshot().Journal[q.ClientMutationID].Actor; got != first {
		t.Fatalf("retry replaced first actor: %+v", got)
	}
	steer := appwire.TurnSteerParams{ClientMutationID: "steer", Input: []appwire.InputItem{{Type: "text", Text: "redirect"}}}
	if _, err := s.AcceptClientMutationSteerAs(steer, manager); err != nil {
		t.Fatal(err)
	}
	if got := s.clientMutations.snapshot().Journal["steer"].Actor; got != manager {
		t.Fatalf("steer actor = %+v", got)
	}
	id := s.ID()
	s.Close()
	restored := restoreQueuePersistTestSession(t, dir, id)
	defer restored.Close()
	reloaded := restored.clientMutations.snapshot()
	if reloaded.Journal[q.ClientMutationID].Actor != first || reloaded.Journal["steer"].Actor != manager {
		t.Fatalf("reloaded queue/steer actors changed: %+v", reloaded.Journal)
	}
}

func TestManagerApprovalStaleDecision(t *testing.T) {
	s := recordingEscalationSession(t)
	defer s.Close()
	observed := s.Events() // Attach the real subscription before requesting approval.
	// TRIPWIRE: subscription and scripted approval complete promptly; guard a blocked request.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	denial, _ := deniedResult("/etc/hosts")
	resolved := make(chan bool, 1)
	go func() {
		result := s.escalateOnSandboxDenial(ctx, "write_file", denial, func(context.Context) tool.ExecResult { return succeededResult() })
		resolved <- result.IsError
	}()
	var escalationID string
	for escalationID == "" {
		select {
		case event := <-observed:
			if event.Kind == events.EventSandboxEscalationRequested {
				escalationID = event.Data.(events.SandboxEscalationRequestedData).EscalationID
			}
		case <-ctx.Done():
			t.Fatal("subscribed approval request never arrived")
		}
	}
	manager := schema.SessionActor{Kind: "manager", ManagerRef: "local:manager"}
	if err := s.ResolveSandboxEscalationAs(escalationID, true, manager); err != nil {
		t.Fatal(err)
	}
	if err := s.ResolveSandboxEscalationAs(escalationID, false, schema.SessionActor{Kind: "human"}); err == nil {
		t.Fatal("stale approval changed decision")
	}
	select {
	case failed := <-resolved:
		if failed {
			t.Fatal("Manager-approved invocation did not rerun")
		}
	case <-ctx.Done():
		t.Fatal("approved invocation remained blocked")
	}
	decisions := approvalDecisions(t, s)
	if len(decisions) != 1 || !decisions[0].Approved {
		t.Fatalf("approval history = %+v", decisions)
	}
	found := false
	for _, turn := range entriesOfKind(transcriptTurnsOf(t, s), schema.TurnNotice) {
		if turn.Notice != nil && turn.Notice.Kind == schema.NoticeApprovalDecision {
			found = turn.Notice.Actor == manager
		}
	}
	if !found {
		t.Fatal("approval notice lost the Manager actor")
	}
}
