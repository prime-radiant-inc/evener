package agent

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/spf13/afero"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/delegatestore"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
)

func TestDelegateControllerSteerPersistsBeforeAcknowledgement(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	fs := &delegateSteerBarrierFS{Fs: afero.NewMemMapFs()}
	runtime := attachDelegateSteerRuntime(t, c, "dlg_target", fs)
	fs.controller = c
	fs.syncEntered = make(chan struct{})
	fs.allowSync = make(chan struct{})
	fs.blockSync = true

	result := make(chan error, 1)
	go func() {
		_, err := c.Steer(context.Background(), rootDelegateActor("root-session"), "dlg_target", "persist me")
		result <- err
	}()
	select {
	case <-fs.syncEntered:
	case err := <-result:
		t.Fatalf("Steer returned without reaching transcript fsync: %v", err)
	}
	select {
	case err := <-result:
		t.Fatalf("Steer returned before transcript fsync: %v", err)
	default:
	}
	close(fs.allowSync)
	if err := <-result; err != nil {
		t.Fatalf("Steer: %v", err)
	}
	if !fs.controllerWasUnlocked {
		t.Fatal("controller mutex was held at the transcript durability boundary")
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if len(runtime.history) != 1 || runtime.history[0].Message.Text() != "persist me" || runtime.history[0].StableTurnID == "" {
		t.Fatalf("durable steering history = %#v", runtime.history)
	}
}

func TestDelegateControllerSteerAppendFailureIsNotAccepted(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	fs := &transcriptWriteFailFS{Fs: afero.NewMemMapFs()}
	runtime := attachDelegateSteerRuntime(t, c, "dlg_target", fs)
	fs.fail = true

	plans, err := c.Steer(context.Background(), rootDelegateActor("root-session"), "dlg_target", "must fail")
	if !errors.Is(err, errInjectedTranscriptWrite) {
		t.Fatalf("Steer error = %v, want injected transcript failure", err)
	}
	if len(plans.updates) != 0 || len(plans.deliveries) != 0 {
		t.Fatalf("failed Steer published plans: %#v", plans)
	}
	c.mu.Lock()
	if got := c.live["dlg_target"].pendingSteers; len(got) != 0 {
		c.mu.Unlock()
		t.Fatalf("failed Steer was accepted: %#v", got)
	}
	c.mu.Unlock()
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if len(runtime.history) != 0 {
		t.Fatalf("failed Steer entered history: %#v", runtime.history)
	}
}

func TestDelegateControllerSteerUpdatesActivityWithoutStateRevision(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	runtime := attachDelegateSteerRuntime(t, c, "dlg_target", afero.NewMemMapFs())
	before := c.Snapshot().rows[0]

	plans, err := c.Steer(context.Background(), rootDelegateActor("root-session"), "dlg_target", "activity")
	if err != nil {
		t.Fatalf("Steer: %v", err)
	}
	if len(plans.updates) != 1 {
		t.Fatalf("Steer update count = %d, want 1", len(plans.updates))
	}
	after := plans.updates[0].rows[0]
	runtime.mu.Lock()
	entryAt := runtime.history[0].Timestamp
	runtime.mu.Unlock()
	if after.revision != before.revision {
		t.Fatalf("Steer revision = %d, want unchanged %d", after.revision, before.revision)
	}
	if !after.latestActivityAt.Equal(entryAt) || !after.latestActivityAt.After(before.latestActivityAt) {
		t.Fatalf("Steer activity = %s, entry=%s before=%s", after.latestActivityAt, entryAt, before.latestActivityAt)
	}
}

func TestDelegateControllerBeginModelRequestBindsPendingEntriesOnce(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	attachDelegateSteerRuntime(t, c, "dlg_target", afero.NewMemMapFs())
	lease := delegateLease{delegateID: "dlg_target", generation: 1}
	for _, message := range []string{"first", "second"} {
		if _, err := c.Steer(context.Background(), rootDelegateActor("root-session"), "dlg_target", message); err != nil {
			t.Fatalf("Steer(%q): %v", message, err)
		}
	}

	history, err := completeDelegateModelRequest(c, lease)
	if err != nil {
		t.Fatalf("BeginModelRequest: %v", err)
	}
	if got := []string{history[0].Text(), history[1].Text()}; !reflect.DeepEqual(got, []string{"first", "second"}) {
		t.Fatalf("bound history = %#v", got)
	}
	c.mu.Lock()
	if got := c.live["dlg_target"].pendingSteers; len(got) != 0 {
		c.mu.Unlock()
		t.Fatalf("pending steers after bind = %#v", got)
	}
	c.mu.Unlock()
	if _, err := completeDelegateModelRequest(c, lease); err != nil {
		t.Fatalf("second BeginModelRequest: %v", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if got := c.live["dlg_target"].pendingSteers; len(got) != 0 {
		t.Fatalf("already-bound steers re-entered pending state: %#v", got)
	}
}

func TestDelegateControllerBeginModelRequestProjectsInFlightSteersAfterResponseOnce(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	runtime := attachDelegateSteerRuntime(t, c, "dlg_target", afero.NewMemMapFs())
	lease := delegateLease{delegateID: "dlg_target", generation: 1}
	initial := schema.NewTurn(schema.TurnUserInput, llm.User("initial"))
	initial.StableTurnID = "turn_initial"
	runtime.recordTurn(initial, initial)
	if _, err := completeDelegateModelRequest(c, lease); err != nil {
		t.Fatalf("initial BeginModelRequest: %v", err)
	}

	requestInFlight := make(chan struct{})
	releaseResponse := make(chan struct{})
	responseRecorded := make(chan struct{})
	go func() {
		close(requestInFlight)
		<-releaseResponse
		response := schema.NewTurn(schema.TurnAssistant, llm.Assistant("in-flight response"))
		response.StableTurnID = "turn_response"
		runtime.recordTurn(response, response)
		close(responseRecorded)
	}()
	<-requestInFlight

	for _, message := range []string{"first steer", "second steer"} {
		if _, err := c.Steer(context.Background(), rootDelegateActor("root-session"), "dlg_target", message); err != nil {
			t.Fatalf("Steer(%q): %v", message, err)
		}
	}
	close(releaseResponse)
	<-responseRecorded

	raw := runtime.delegateModelHistorySnapshot()
	if len(raw) != 4 {
		t.Fatalf("durable chronological history length = %d, want 4: %#v", len(raw), raw)
	}
	if got := []schema.TurnKind{raw[0].Kind, raw[1].Kind, raw[2].Kind, raw[3].Kind}; !reflect.DeepEqual(got, []schema.TurnKind{
		schema.TurnUserInput,
		schema.TurnSteering,
		schema.TurnSteering,
		schema.TurnAssistant,
	}) {
		t.Fatalf("durable chronological history kinds = %#v", got)
	}
	steerIDs := []string{raw[1].StableTurnID, raw[2].StableTurnID}
	if steerIDs[0] == "" || steerIDs[1] == "" || steerIDs[0] == steerIDs[1] {
		t.Fatalf("durable steer IDs = %#v, want distinct stable IDs", steerIDs)
	}

	request, err := completeDelegateModelRequest(c, lease)
	if err != nil {
		t.Fatalf("next BeginModelRequest: %v", err)
	}
	got := make([]string, len(request))
	for i := range request {
		got[i] = request[i].Text()
	}
	want := []string{"initial", "in-flight response", "first steer", "second steer"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("next request history = %#v, want %#v", got, want)
	}
	for _, steer := range want[2:] {
		count := 0
		for _, message := range got {
			if message == steer {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("next request contains %q %d times, want exactly once", steer, count)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if pending := c.live["dlg_target"].pendingSteers; len(pending) != 0 {
		t.Fatalf("bound in-flight steers remain pending: %#v", pending)
	}
}

func TestDelegateControllerSteerAfterRequestBindWaitsForNextRequest(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	attachDelegateSteerRuntime(t, c, "dlg_target", afero.NewMemMapFs())
	lease := delegateLease{delegateID: "dlg_target", generation: 1}
	if _, err := c.Steer(context.Background(), rootDelegateActor("root-session"), "dlg_target", "first"); err != nil {
		t.Fatalf("first Steer: %v", err)
	}
	if _, err := completeDelegateModelRequest(c, lease); err != nil {
		t.Fatalf("first BeginModelRequest: %v", err)
	}
	if _, err := c.Steer(context.Background(), rootDelegateActor("root-session"), "dlg_target", "next"); err != nil {
		t.Fatalf("second Steer: %v", err)
	}
	c.mu.Lock()
	pending := append([]delegateSteeringAdmission(nil), c.live["dlg_target"].pendingSteers...)
	c.mu.Unlock()
	if len(pending) != 1 || pending[0].entryID == "" {
		t.Fatalf("pending steers before next request = %#v", pending)
	}
	if _, err := completeDelegateModelRequest(c, lease); err != nil {
		t.Fatalf("second BeginModelRequest: %v", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.live["dlg_target"].pendingSteers) != 0 {
		t.Fatalf("next request did not bind later steer: %#v", c.live["dlg_target"].pendingSteers)
	}
}

func TestDelegateControllerModelSnapshotDefersUncompletedSteerUntilNextRequest(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	attachDelegateSteerRuntime(t, c, "dlg_target", afero.NewMemMapFs())
	lease := delegateLease{delegateID: "dlg_target", generation: 1}
	steerClaim, err := c.BeginSteerPersistence(rootDelegateActor("root-session"), lease.delegateID)
	if err != nil {
		t.Fatalf("BeginSteerPersistence: %v", err)
	}
	entry, err := steerClaim.runtime.appendDelegateSteeringDurably("in-flight steer", steerClaim.entryID)
	if err != nil {
		t.Fatalf("append steering: %v", err)
	}

	current, err := completeDelegateModelRequest(c, lease)
	if err != nil {
		t.Fatalf("current model request: %v", err)
	}
	if countMessageText(current, "in-flight steer") != 0 {
		t.Fatalf("uncompleted steer entered current request: %#v", current)
	}
	if _, err := c.CompleteSteerPersistence(steerClaim, entry); err != nil {
		t.Fatalf("CompleteSteerPersistence: %v", err)
	}
	next, err := completeDelegateModelRequest(c, lease)
	if err != nil {
		t.Fatalf("next model request: %v", err)
	}
	if got := countMessageText(next, "in-flight steer"); got != 1 {
		t.Fatalf("next request contains in-flight steer %d times, want once: %#v", got, next)
	}
}

func TestDelegateControllerModelSnapshotDefersSteerAcceptedAfterRequestBind(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	runtime := attachDelegateSteerRuntime(t, c, "dlg_target", afero.NewMemMapFs())
	lease := delegateLease{delegateID: "dlg_target", generation: 1}
	modelClaim, err := c.BeginModelRequest(lease)
	if err != nil {
		t.Fatalf("BeginModelRequest: %v", err)
	}
	if _, err := c.Steer(context.Background(), rootDelegateActor("root-session"), lease.delegateID, "late accepted steer"); err != nil {
		t.Fatalf("Steer: %v", err)
	}
	current, err := c.CompleteModelRequest(modelClaim, runtime.delegateModelHistorySnapshot(), replayScope{})
	if err != nil {
		t.Fatalf("CompleteModelRequest: %v", err)
	}
	if countMessageText(current, "late accepted steer") != 0 {
		t.Fatalf("late accepted steer entered already-bound request: %#v", current)
	}
	next, err := completeDelegateModelRequest(c, lease)
	if err != nil {
		t.Fatalf("next model request: %v", err)
	}
	if got := countMessageText(next, "late accepted steer"); got != 1 {
		t.Fatalf("next request contains late accepted steer %d times, want once: %#v", got, next)
	}
}

func TestDelegateControllerModelRequestUsesOutgoingReplayScope(t *testing.T) {
	profile, err := provider.ResolveProfileFromConfig(providercfg.Config{
		Instances: []providercfg.InstanceConfig{{Name: "ant", Type: "anthropic"}},
	}, "ant/claude-sonnet-4-5")
	if err != nil {
		t.Fatalf("resolve anthropic profile: %v", err)
	}
	runtime := newSession(t, withAdapter(&fakeAdapter{name: "ant"}), withProfile(profile), withoutGitSnapshot())
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	lease := delegateLease{delegateID: "dlg_target", generation: 1}
	runtime.delegateController = c
	c.mu.Lock()
	c.live[lease.delegateID].runtime = runtime
	c.live[lease.delegateID].binding.runtime = runtime
	c.mu.Unlock()
	prior := assistantThinkingTurn("ant", "claude-opus-4-6", "claude-opus-4-6")
	prior.StableTurnID = "turn_prior_model"
	runtime.mu.Lock()
	runtime.history = []schema.Turn{prior}
	runtime.turnHistoryBaseline = len(runtime.history)
	runtime.mu.Unlock()
	ctx := context.WithValue(context.Background(), delegateRunLeaseContextKey{}, lease)
	var timings events.RoundTimings
	_, _, history, _, _, err := runtime.prepareModelRequestWithError(ctx, 1, &timings)
	if err != nil {
		t.Fatalf("prepare delegate model request: %v", err)
	}
	if hasContentKind(history, llm.ContentThinking) {
		t.Fatalf("delegate request replayed thinking from a different anthropic model: %#v", history)
	}
	if !hasContentKind(history, llm.ContentText) {
		t.Fatalf("delegate request stripped ordinary answer text: %#v", history)
	}
}

type delegateBlockingContextStrategy struct {
	spyStrategy
	entered chan struct{}
	release chan struct{}
}

func (s *delegateBlockingContextStrategy) ManageContext(ctx context.Context, _ *[]schema.Turn, _ int, _ func(events.EventKind, events.EventData)) error {
	close(s.entered)
	select {
	case <-s.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestDelegateControllerSteerDuringContextManagementEntersNextRequestOnce(t *testing.T) {
	strategy := &delegateBlockingContextStrategy{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	cfg := SessionConfig{MaxSubagentDepth: 1, NoProjectPrompts: true, StateDir: t.TempDir()}
	cfg.testOnly.contextStrategyOverride = strategy
	runtime := newSession(t, withConfig(cfg), withoutGitSnapshot())
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	lease := delegateLease{delegateID: "dlg_target", generation: 1}
	runtime.delegateController = c
	c.mu.Lock()
	c.live[lease.delegateID].runtime = runtime
	c.live[lease.delegateID].binding.runtime = runtime
	c.mu.Unlock()

	type prepareResult struct {
		history []llm.Message
		err     error
	}
	result := make(chan prepareResult, 1)
	ctx := context.WithValue(context.Background(), delegateRunLeaseContextKey{}, lease)
	go func() {
		var timings events.RoundTimings
		_, _, history, _, _, err := runtime.prepareModelRequestWithError(ctx, 1, &timings)
		result <- prepareResult{history: history, err: err}
	}()

	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(strategy.release) }) }
	defer release()
	waitForTestSignal(t, strategy.entered, "delegate context management")
	if _, err := c.Steer(context.Background(), rootDelegateActor("root-session"), lease.delegateID, "accepted during context management"); err != nil {
		t.Fatalf("Steer: %v", err)
	}
	release()
	prepared := <-result
	if prepared.err != nil {
		t.Fatalf("prepare model request: %v", prepared.err)
	}
	if got := countMessageText(prepared.history, "accepted during context management"); got != 1 {
		t.Fatalf("next request contains context-management steer %d times, want once: %#v", got, prepared.history)
	}
}

func countMessageText(messages []llm.Message, want string) int {
	count := 0
	for _, message := range messages {
		if message.Text() == want {
			count++
		}
	}
	return count
}

func completeDelegateModelRequest(c *delegateTreeController, lease delegateLease) ([]llm.Message, error) {
	claim, err := c.BeginModelRequest(lease)
	if err != nil {
		return nil, err
	}
	return c.CompleteModelRequest(claim, claim.runtime.delegateModelHistorySnapshot(), replayScope{})
}

func TestDelegateControllerBeginToolRejectsStoppingOrStaleLease(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	lease := delegateLease{delegateID: "dlg_target", generation: 1}
	if err := c.BeginTool(delegateLease{delegateID: "dlg_target", generation: 2}); !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("BeginTool stale error = %v, want stale lease", err)
	}
	c.mu.Lock()
	_, err := c.appendLocked(delegatestore.Event{
		Kind:       delegatestore.EventDelegateSubtreeStopRequested,
		DelegateID: "dlg_target",
		SubtreeStopRequested: &delegatestore.SubtreeStopRequested{
			TargetDelegateID: "dlg_target",
		},
	})
	c.mu.Unlock()
	if err != nil {
		t.Fatalf("append stop request: %v", err)
	}
	if err := c.BeginTool(lease); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("BeginTool stopping error = %v, want target busy", err)
	}
}

func attachDelegateSteerRuntime(t *testing.T, c *delegateTreeController, delegateID string, fs afero.Fs) *Session {
	t.Helper()
	writer, err := transcript.NewWriterWithFS(fs, "/"+delegateID+".jsonl", transcript.Header{SessionID: "child-" + delegateID})
	if err != nil {
		t.Fatalf("NewWriterWithFS: %v", err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	runtime := &Session{}
	runtime.attachTranscript(writer)
	c.mu.Lock()
	live := c.live[delegateID]
	live.runtime = runtime
	live.binding.runtime = runtime
	c.mu.Unlock()
	return runtime
}

type delegateSteerBarrierFS struct {
	afero.Fs
	controller            *delegateTreeController
	blockSync             bool
	syncEntered           chan struct{}
	allowSync             chan struct{}
	controllerWasUnlocked bool
	once                  sync.Once
}

func (fs *delegateSteerBarrierFS) Create(name string) (afero.File, error) {
	file, err := fs.Fs.Create(name)
	if err != nil {
		return nil, err
	}
	return &delegateSteerBarrierFile{File: file, fs: fs}, nil
}

type delegateSteerBarrierFile struct {
	afero.File
	fs *delegateSteerBarrierFS
}

func (file *delegateSteerBarrierFile) Sync() error {
	if !file.fs.blockSync {
		return file.File.Sync()
	}
	file.fs.once.Do(func() {
		if file.fs.controller.mu.TryLock() {
			file.fs.controllerWasUnlocked = true
			file.fs.controller.mu.Unlock()
		}
		close(file.fs.syncEntered)
	})
	<-file.fs.allowSync
	return file.File.Sync()
}
