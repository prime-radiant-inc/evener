package agent

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/schema"
	taskpkg "primeradiant.com/evener/agent/task"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// A delegate works its seeded list in order, and a later step's instructions
// often decide what an earlier step must build. The child's first model request
// therefore carries every step's title and prompt, not only the first.
func TestDelegateStartupRequestCarriesTheWholePlan(t *testing.T) {
	t.Parallel()
	root, adapter := newDelegateStartupFixture(t)
	plan := []taskpkg.TaskTemplate{
		{Title: "First step", Prompt: "PROMPT_FIRST: implement the parser."},
		{Title: "Second step", Prompt: "PROMPT_SECOND: render the result."},
		{Title: "Third step", Prompt: "PROMPT_THIRD: document the format."},
	}
	spawnDelegateWithPlan(t, root, "do the plan", plan)

	first := messagesText(adapter.waitFirstRequest(t).Messages)
	for _, want := range []string{
		"First step", "Second step", "Third step",
		"PROMPT_FIRST", "PROMPT_SECOND", "PROMPT_THIRD",
	} {
		if !strings.Contains(first, want) {
			t.Errorf("first child request is missing %q:\n%s", want, first)
		}
	}
}

// A delegate with no tasks is unaffected: no plan reminder is injected.
func TestDelegateStartupWithoutTasksHasNoPlanReminder(t *testing.T) {
	t.Parallel()
	empty := taskpkg.NewTaskStore(t.TempDir(), "empty")
	if got := taskReminderFullWithPrompts(empty.View()); got != "" {
		t.Fatalf("empty store rendered a plan: %s", got)
	}

	root, adapter := newDelegateStartupFixture(t)
	spawnDelegateWithPlan(t, root, "BRIEF_ONLY", nil)

	first := messagesText(adapter.waitFirstRequest(t).Messages)
	if !strings.Contains(first, "BRIEF_ONLY") {
		t.Fatalf("first child request is missing its brief:\n%s", first)
	}
	if countCurrentTaskElements(first) > 0 {
		t.Fatalf("a delegate with no tasks received a plan reminder:\n%s", first)
	}
}

// The production steering path queues ONE message carrying the whole plan and
// the step to work first. Splitting it into a plan message plus a current-task
// message would reopen the crash window: a crash between the two writes leaves
// the plan announced with no step to work.
func TestDelegatePlanIsOneQueuedMessage(t *testing.T) {
	t.Parallel()
	root, _ := newDelegateStartupFixture(t)
	store := taskpkg.NewTaskStore(t.TempDir(), "child")
	if err := store.PopulateFromTemplates([]taskpkg.TaskTemplate{
		{Title: "First step", Prompt: "PROMPT_FIRST"},
		{Title: "Second step", Prompt: "PROMPT_SECOND"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := steerDelegatePlan(root, store); err != nil {
		t.Fatalf("steerDelegatePlan: %v", err)
	}

	root.mu.Lock()
	queued := append([]steeringMessage(nil), root.steeringQueue...)
	root.mu.Unlock()
	if len(queued) != 1 {
		t.Fatalf("startup steering = %d messages, want exactly one: %#v", len(queued), queued)
	}
	if queued[0].Kind != events.SteeringKindTaskList {
		t.Fatalf("startup steering kind = %q, want %q", queued[0].Kind, events.SteeringKindTaskList)
	}
	assertCurrentTaskElement(t, queued[0].Text, 1, "PROMPT_FIRST")
	if !strings.Contains(queued[0].Text, "PROMPT_SECOND") {
		t.Fatalf("startup steering omits a later step's prompt:\n%s", queued[0].Text)
	}
}

// A role's workflow can put its own steps before the parent_tasks slot, so the
// step to work first is the first in-progress task, not the first task.
func TestDelegatePlanNamesTheInProgressStep(t *testing.T) {
	t.Parallel()
	root, adapter := newDelegateStartupFixture(t)
	root.pluginAgents["prefix-role"] = plugin.Agent{
		Name:       "prefix-role",
		AllTools:   true,
		PluginName: "test",
		Tasks: []taskpkg.TaskTemplate{
			{Title: "Role intro", Prompt: "ROLE_INTRO"},
			{Title: "Insert the parent's steps", Prompt: "placeholder", Insert: "parent_tasks"},
		},
	}
	created := root.createDelegate(context.Background(), delegateArgs{
		Task:                "do the seeded step",
		AgentType:           "prefix-role",
		TaskList:            []taskpkg.TaskTemplate{{Title: "Seeded step", Prompt: "SEEDED_PROMPT"}},
		DelegationAllowance: new(0),
	})
	if created.Err != nil {
		t.Fatalf("createDelegate: %v", created.Err)
	}

	first := messagesText(adapter.waitFirstRequest(t).Messages)
	assertCurrentTaskElement(t, first, 1, "ROLE_INTRO")
	if got := countCurrentTaskElements(first); got != 1 {
		t.Fatalf("plan names %d current steps, want one:\n%s", got, first)
	}
}

// A restored delegate whose task file survived is not announced to again: its
// queue still holds the spawn-time plan. Re-announcing would duplicate it.
func TestRestoreDoesNotRepeatPersistedPlanSteering(t *testing.T) {
	t.Parallel()
	fixture := newColdStableDelegateFixtureConfigured(t, "", func(descriptor *delegatestore.Descriptor) {
		descriptor.TaskTemplates = []taskpkg.TaskTemplate{{
			Title:  "Restored committed workflow",
			Prompt: "Resume the committed workflow.",
		}}
	})
	// A spawn always writes the child's task file, so a real restore finds the
	// tasks already there. Write it here too; otherwise the fixture restores a
	// state production never produces.
	childStore := taskpkg.NewTaskStore(fixture.stateDir, fixture.childID)
	if err := childStore.PopulateFromTemplates([]taskpkg.TaskTemplate{{
		Title:  "Restored committed workflow",
		Prompt: "Resume the committed workflow.",
	}}, nil); err != nil {
		t.Fatalf("seed child task file: %v", err)
	}
	// A spawn also persists its steering, which restore reloads. Without it the
	// restore would be the crash-window case, not the resumed-child case.
	if err := saveQueues(fixture.stateDir, fixture.childID, []steeringMessage{{
		Kind: events.SteeringKindTaskList,
		Text: "<SYSTEM-REMINDER>\nTask list:\n  [in_progress] #1: Restored committed workflow\n    prompt: Resume the committed workflow.\n</SYSTEM-REMINDER>",
	}}, nil); err != nil {
		t.Fatalf("persist child plan steering: %v", err)
	}
	root, err := restoreDelegateResourceBootstrapSession(fixture.client, fixture.profile, fixture.workspace, fixture.meta, fixture.stateDir)
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	defer root.Close()

	reservation, err := root.delegateController.ReserveStart(rootDelegateActor(root.id), fixture.delegateID)
	if err != nil {
		t.Fatalf("ReserveStart: %v", err)
	}
	started, err := root.delegateController.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart: %v", err)
	}
	sub, restored, err := (delegateRuntime{owner: root}).restoreIdle(started)
	if err != nil {
		t.Fatalf("restoreIdle: %v", err)
	}
	if !restored {
		t.Fatal("restoreIdle unexpectedly reused a resident child")
	}
	defer func() {
		sub.sess.discardRestoredCandidate()
		_, _ = root.delegateController.FailCommittedRestart(started.lease, delegatePermanentStartFailure(context.Canceled, "test_cleanup"))
	}()

	// The persisted plan is reloaded, so one is expected. A second copy would
	// mean restore announced again over steering that was already queued.
	if got := countPlanSteering(sub.sess); got != 1 {
		t.Fatalf("restore queued %d plan messages for a resumed child, want exactly the reloaded one", got)
	}
}

// A crash can land between the task store write and the steering write. The
// tasks survive, nothing is queued, and nothing was delivered; restore must
// announce the plan rather than treating the tasks as proof.
func TestRestoreAnnouncesAPlanThatWasNeverDelivered(t *testing.T) {
	t.Parallel()
	fixture := newColdStableDelegateFixtureConfigured(t, "", func(descriptor *delegatestore.Descriptor) {
		descriptor.TaskTemplates = []taskpkg.TaskTemplate{{
			Title:  "Crash-window workflow",
			Prompt: "CRASH_WINDOW_PROMPT",
		}}
	})
	// The tasks exist (the store write happened); the steering write did not.
	childStore := taskpkg.NewTaskStore(fixture.stateDir, fixture.childID)
	if err := childStore.PopulateFromTemplates([]taskpkg.TaskTemplate{{
		Title:  "Crash-window workflow",
		Prompt: "CRASH_WINDOW_PROMPT",
	}}, nil); err != nil {
		t.Fatalf("seed child task file: %v", err)
	}
	root, err := restoreDelegateResourceBootstrapSession(fixture.client, fixture.profile, fixture.workspace, fixture.meta, fixture.stateDir)
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	defer root.Close()

	reservation, err := root.delegateController.ReserveStart(rootDelegateActor(root.id), fixture.delegateID)
	if err != nil {
		t.Fatalf("ReserveStart: %v", err)
	}
	started, err := root.delegateController.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart: %v", err)
	}
	sub, restored, err := (delegateRuntime{owner: root}).restoreIdle(started)
	if err != nil {
		t.Fatalf("restoreIdle: %v", err)
	}
	if !restored {
		t.Fatal("restoreIdle unexpectedly reused a resident child")
	}
	defer func() {
		sub.sess.discardRestoredCandidate()
		_, _ = root.delegateController.FailCommittedRestart(started.lease, delegatePermanentStartFailure(context.Canceled, "test_cleanup"))
	}()

	if got := countPlanSteering(sub.sess); got != 1 {
		t.Fatalf("restore queued %d plan messages for an undelivered plan, want 1", got)
	}
}

// Once the plan has been delivered, the queue is empty and the only evidence is
// the transcript. Restore must find it there and stay silent.
func TestRestoreSeesADeliveredPlanInTheTranscript(t *testing.T) {
	t.Parallel()
	fixture := newColdStableDelegateFixtureConfigured(t, "", func(descriptor *delegatestore.Descriptor) {
		descriptor.TaskTemplates = []taskpkg.TaskTemplate{{
			Title:  "Delivered workflow",
			Prompt: "DELIVERED_PROMPT",
		}}
	})
	childStore := taskpkg.NewTaskStore(fixture.stateDir, fixture.childID)
	if err := childStore.PopulateFromTemplates([]taskpkg.TaskTemplate{{
		Title:  "Delivered workflow",
		Prompt: "DELIVERED_PROMPT",
	}}, nil); err != nil {
		t.Fatalf("seed child task file: %v", err)
	}
	writeDeliveredPlanTranscript(t, fixture.stateDir, fixture.childID, fixture.meta.ID)

	root, err := restoreDelegateResourceBootstrapSession(fixture.client, fixture.profile, fixture.workspace, fixture.meta, fixture.stateDir)
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	defer root.Close()
	sub := restoreFixtureDelegate(t, root, fixture.delegateID)

	if got := countPlanSteering(sub.sess); got != 0 {
		t.Fatalf("restore queued %d plan messages for a plan already in the transcript, want 0", got)
	}
}

// writeDeliveredPlanTranscript appends a delivered task-list announcement to a
// child's existing transcript, which is what remains after the queue drains.
// The fixture already created that transcript; appending keeps one file and one
// header, as a real session keeps.
func writeDeliveredPlanTranscript(t *testing.T, stateDir, childID, parentID string) {
	t.Helper()
	path := transcriptPath(stateDir, childID)
	writer, err := transcript.NewWriter(path, transcript.Header{
		SessionID:       childID,
		ParentSessionID: parentID,
	})
	if err != nil {
		t.Fatalf("transcript writer: %v", err)
	}
	defer writer.Close()
	turn := schema.NewTurn(schema.TurnSteering, llm.System("<SYSTEM-REMINDER>\nTask list:\n</SYSTEM-REMINDER>"))
	turn.SteeringKind = events.SteeringKindTaskList
	if _, err := writer.Record(turn, transcript.RecordOptions{}); err != nil {
		t.Fatalf("record steering turn: %v", err)
	}
}

// restoreFixtureDelegate runs one cold restore of a committed delegate.
func restoreFixtureDelegate(t *testing.T, root *Session, delegateID string) *subagent {
	t.Helper()
	reservation, err := root.delegateController.ReserveStart(rootDelegateActor(root.id), delegateID)
	if err != nil {
		t.Fatalf("ReserveStart: %v", err)
	}
	started, err := root.delegateController.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart: %v", err)
	}
	sub, restored, err := (delegateRuntime{owner: root}).restoreIdle(started)
	if err != nil {
		t.Fatalf("restoreIdle: %v", err)
	}
	if !restored {
		t.Fatal("restoreIdle unexpectedly reused a resident child")
	}
	t.Cleanup(func() {
		sub.sess.discardRestoredCandidate()
		_, _ = root.delegateController.FailCommittedRestart(started.lease, delegatePermanentStartFailure(context.Canceled, "test_cleanup"))
	})
	return sub
}

// countPlanSteering counts queued task-list announcements.
func countPlanSteering(sess *Session) int {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	count := 0
	for _, message := range sess.steeringQueue {
		if message.Kind == events.SteeringKindTaskList {
			count++
		}
	}
	return count
}

// currentTaskElement is the parsed CURRENT-TASK block a reminder carries.
type currentTaskElement struct {
	id     int
	title  string
	prompt string
}

// parseCurrentTaskElements parses every CURRENT-TASK block in text. Tests assert
// on these elements rather than on prose, so a wording change does not break
// them.
func parseCurrentTaskElements(text string) []currentTaskElement {
	var elements []currentTaskElement
	remaining := text
	for {
		start := strings.Index(remaining, "<CURRENT-TASK ")
		if start < 0 {
			return elements
		}
		block := remaining[start:]
		end := strings.Index(block, "</CURRENT-TASK>")
		if end < 0 {
			return elements
		}
		self := block[:end]
		block = block[:end]
		remaining = remaining[start+len(self):]

		idText, rest, ok := strings.Cut(block, ">")
		if !ok {
			continue
		}
		idText = strings.TrimPrefix(idText, "<CURRENT-TASK ")
		id, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(idText, "id=\""), "\""))
		if err != nil {
			continue
		}
		element := currentTaskElement{id: id}
		if title, after, ok := strings.Cut(rest, "</TITLE>"); ok {
			element.title = strings.TrimPrefix(title, "\n<TITLE>")
			if instructions, _, ok := strings.Cut(after, "</INSTRUCTIONS>"); ok {
				element.prompt = strings.TrimSpace(strings.TrimPrefix(instructions, "\n<INSTRUCTIONS>\n"))
			}
		}
		elements = append(elements, element)
	}
}

// countCurrentTaskElements reports how many CURRENT-TASK blocks text carries.
func countCurrentTaskElements(text string) int {
	return len(parseCurrentTaskElements(text))
}

// assertCurrentTaskElement requires exactly one CURRENT-TASK block naming id,
// carrying prompt, and titled with its task's description.
func assertCurrentTaskElement(t *testing.T, text string, id int, prompt string) {
	t.Helper()
	elements := parseCurrentTaskElements(text)
	if len(elements) != 1 {
		t.Fatalf("CURRENT-TASK elements = %d, want one:\n%s", len(elements), text)
	}
	if elements[0].id != id || elements[0].prompt != prompt || elements[0].title == "" {
		t.Fatalf("CURRENT-TASK element = %+v, want id %d with prompt %q", elements[0], id, prompt)
	}
}

// newDelegateStartupFixture builds a delegate-capable session whose scripted
// adapter answers one final report and keeps every request it saw, so a test
// can read the child's first turn.
func newDelegateStartupFixture(t *testing.T) (*Session, *fakeAdapter) {
	t.Helper()
	workspace := t.TempDir()
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response { return finalResponse("child done") },
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	sess, err := NewSession(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(workspace), SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 2,
		ForceRealIO:      true,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			sandboxProber:       bwrapCapableProber(workspace),
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)
	return sess, adapter
}

// waitFirstRequest waits for the first request the adapter answered.
func (a *fakeAdapter) waitFirstRequest(t *testing.T) llm.Request {
	t.Helper()
	var first llm.Request
	// TRIPWIRE: the child's adapter is scripted in process and answers in
	// milliseconds; 30s only fires on a genuine hang.
	waitForCondition(t, 30*time.Second, "the child's first model request", func() bool {
		requests := a.Requests()
		if len(requests) == 0 {
			return false
		}
		first = requests[0]
		return true
	})
	return first
}

// spawnDelegateWithPlan starts one leaf delegate through the production path.
func spawnDelegateWithPlan(t *testing.T, root *Session, brief string, plan []taskpkg.TaskTemplate) string {
	t.Helper()
	created := root.createDelegate(context.Background(), delegateArgs{
		Task:                brief,
		TaskList:            plan,
		DelegationAllowance: new(0),
	})
	if created.Err != nil {
		t.Fatalf("createDelegate: %v", created.Err)
	}
	return created.ChildSessionID
}
