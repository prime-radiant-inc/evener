package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/events"
	toolpkg "primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/provenance"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

func TestDelegateResourceCaller_RegisteredNestedParentUsesStableController(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 2, 2)
	seedDelegateControllerRunning(t, c, "dlg_parent", "")
	seedDelegateControllerRunning(t, c, "dlg_child", "dlg_parent")
	parentFS := afero.NewMemMapFs()
	parent := attachRegisteredCallerRuntime(t, c, "dlg_parent", parentFS)
	child := attachRegisteredCallerRuntime(t, c, "dlg_child", afero.NewMemMapFs())

	call := executeRegisteredCallerSend(t, child, delegateLease{delegateID: "dlg_child", generation: 1}, "nested report")
	if call.IsError {
		t.Fatalf("registered nested caller send: %s", call.Output)
	}
	state := registeredCallerState(t, call)
	if state["action"] != "delivered" {
		t.Fatalf("registered nested caller state = %#v, want delivered", state)
	}
	entries := decodeTranscriptEntries(t, parentFS, "/dlg_parent.jsonl")
	if len(entries) != 1 || entries[0].Turn.Kind != schema.TurnSteering || entries[0].Turn.Message.Text() != "nested report" || entries[0].Turn.StableTurnID == "" || entries[0].Turn.SteeringKind != "agent-message" || entries[0].Turn.SteeringSource != "" {
		t.Fatalf("stable parent transcript entries = %#v, want one durable caller steering turn", entries)
	}
	if queue := parent.SteeringQueueSnapshot(); len(queue) != 0 {
		t.Fatalf("nested caller bypassed stable controller into parent queue: %#v", queue)
	}
	c.mu.Lock()
	pending := append([]delegateSteeringAdmission(nil), c.live["dlg_parent"].pendingSteers...)
	c.mu.Unlock()
	if len(pending) != 1 || pending[0].entryID != entries[0].Turn.StableTurnID {
		t.Fatalf("stable parent pending steers = %#v, want exact durable entry %q", pending, entries[0].Turn.StableTurnID)
	}
}

func TestDelegateResourceCaller_RegisteredNestedParentPreservesWatchProvenance(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 2, 2)
	seedDelegateControllerRunning(t, c, "dlg_parent", "")
	seedDelegateControllerRunning(t, c, "dlg_child", "dlg_parent")
	parent := attachRegisteredCallerRuntime(t, c, "dlg_parent", afero.NewMemMapFs())
	child := attachRegisteredCallerRuntime(t, c, "dlg_child", afero.NewMemMapFs())
	child.replaceActiveProvenance(testProvenance("watch_nested", "wg_nested"))

	call := executeRegisteredCallerSend(t, child, delegateLease{delegateID: "dlg_child", generation: 1}, "nested watch report")
	if call.IsError {
		t.Fatalf("registered nested caller send: %s", call.Output)
	}
	if _, err := completeDelegateModelRequest(c, delegateLease{delegateID: "dlg_parent", generation: 1}); err != nil {
		t.Fatalf("consume nested caller steer: %v", err)
	}
	parent.events = make(chan events.SessionEvent, 1)
	parent.emit(events.EventCommunicate, events.CommunicateData{Message: "parent consequence"})
	event := <-parent.events
	if !provenance.ContainsWatch(event.Provenance, "watch_nested", "wg_nested") {
		t.Fatalf("parent event provenance = %+v, want watch_nested/wg_nested from nested caller", event.Provenance)
	}
}

func TestDelegateResourceCaller_RegisteredNestedParentProvenanceBindingBlocksStop(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 2, 2)
	seedDelegateControllerRunning(t, c, "dlg_parent", "")
	seedDelegateControllerRunning(t, c, "dlg_child", "dlg_parent")
	parent := attachRegisteredCallerRuntime(t, c, "dlg_parent", afero.NewMemMapFs())
	child := attachRegisteredCallerRuntime(t, c, "dlg_child", afero.NewMemMapFs())
	child.replaceActiveProvenance(testProvenance("watch_stop", "wg_stop"))

	call := executeRegisteredCallerSend(t, child, delegateLease{delegateID: "dlg_child", generation: 1}, "stop-racing report")
	if call.IsError {
		t.Fatalf("registered nested caller send: %s", call.Output)
	}
	parentLease := delegateLease{delegateID: "dlg_parent", generation: 1}
	claim, err := c.BeginModelRequest(parentLease)
	if err != nil {
		t.Fatalf("begin parent model request: %v", err)
	}
	snapshot := parent.delegateModelHistorySnapshot()
	beforeProvenance := make(chan struct{})
	claim.testBeforeProvenance = func() { close(beforeProvenance) }
	parent.mu.Lock()
	parentLocked := true
	defer func() {
		if parentLocked {
			parent.mu.Unlock()
		}
	}()
	completeDone := make(chan error, 1)
	go func() {
		_, err := c.CompleteModelRequest(claim, snapshot, replayScope{})
		completeDone <- err
	}()
	<-beforeProvenance

	result, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), parentLease.delegateID)
	if err != nil {
		t.Fatalf("stop parent subtree: %v", err)
	}
	if _, err := c.FinishGeneration(delegateLease{delegateID: "dlg_child", generation: 1}, delegateFinish{}); err != nil {
		t.Fatalf("finish stopped child: %v", err)
	}
	if _, err := c.FinishGeneration(parentLease, delegateFinish{}); err != nil {
		t.Fatalf("finish stopped parent: %v", err)
	}
	if _, err := c.Reconcile(emptyDelegateReconcileEvidence(c)); err != nil {
		t.Fatalf("reconcile while provenance binding is blocked: %v", err)
	}
	completedBeforeBinding := false
	select {
	case <-result.done:
		completedBeforeBinding = true
	default:
	}
	parent.mu.Unlock()
	parentLocked = false
	if err := <-completeDone; err != nil {
		t.Fatalf("complete parent model request: %v", err)
	}
	if completedBeforeBinding {
		t.Fatal("stop completed before parent provenance binding and model request claim finished")
	}
	if _, err := c.Reconcile(emptyDelegateReconcileEvidence(c)); err != nil {
		t.Fatalf("reconcile after provenance binding: %v", err)
	}
	select {
	case <-result.done:
	default:
		t.Fatal("stop remained pending after provenance binding and model request claim finished")
	}
}

func TestDelegateResourceCaller_RegisteredRootParentUsesSafeSteeringAdmission(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	root := newRegisteredCallerRoot(t, c, afero.NewMemMapFs())
	seedDelegateControllerRunning(t, c, "dlg_child", "")
	child := attachRegisteredCallerRuntime(t, c, "dlg_child", afero.NewMemMapFs())

	call := executeRegisteredCallerSend(t, child, delegateLease{delegateID: "dlg_child", generation: 1}, "root report")
	if call.IsError {
		t.Fatalf("registered root caller send: %s", call.Output)
	}
	state := registeredCallerState(t, call)
	if state["action"] != "delivered" {
		t.Fatalf("registered root caller state = %#v, want delivered", state)
	}
	queue := root.SteeringQueueSnapshot()
	if len(queue) != 1 || queue[0].Text != "root report" {
		t.Fatalf("root steering queue = %#v, want one admitted caller message", queue)
	}
	persisted, _, err := loadQueues(root.stateDir, root.id)
	if err != nil {
		t.Fatalf("load durable root queue: %v", err)
	}
	if len(persisted) != 1 || persisted[0].Text != "root report" {
		t.Fatalf("durable root steering queue = %#v, want admitted caller message", persisted)
	}
}

func TestDelegateResourceCaller_RegisteredRootParentRejectsQueuePersistenceFailure(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	root := newRegisteredCallerRoot(t, c, afero.NewMemMapFs())
	seedDelegateControllerRunning(t, c, "dlg_child", "")
	child := attachRegisteredCallerRuntime(t, c, "dlg_child", afero.NewMemMapFs())
	if err := os.WriteFile(filepath.Join(root.stateDir, queuePersistSubdir), []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("block root queue persistence: %v", err)
	}

	call := executeRegisteredCallerSend(t, child, delegateLease{delegateID: "dlg_child", generation: 1}, "must be durable")
	if !call.IsError || !strings.Contains(call.Output, "persist") {
		t.Fatalf("registered root caller persistence failure = error:%v output:%q, want persistence error", call.IsError, call.Output)
	}
	if queue := root.SteeringQueueSnapshot(); len(queue) != 0 {
		t.Fatalf("root steering queue after rejected caller send = %#v, want empty", queue)
	}
}

func TestDelegateResourceCaller_RegisteredRejectsInvalidLifecycleAndAuthorization(t *testing.T) {
	t.Run("root has no controlling caller", func(t *testing.T) {
		c, _ := newDelegateControllerTestHarness(t, 1, 1)
		root := newRegisteredCallerRoot(t, c, afero.NewMemMapFs())
		call := executeRegisteredCallerSendWithoutLease(t, root, "invalid root callback")
		if !call.IsError || !strings.Contains(call.Output, "caller is only available from a delegate") {
			t.Fatalf("root caller result = error:%v output:%q, want contextual-route rejection", call.IsError, call.Output)
		}
	})

	t.Run("idle stable parent rejects nested caller", func(t *testing.T) {
		c, _ := newDelegateControllerTestHarness(t, 2, 2)
		seedDelegateControllerIdle(t, c, "dlg_parent", "")
		seedDelegateControllerRunning(t, c, "dlg_child", "dlg_parent")
		child := attachRegisteredCallerRuntime(t, c, "dlg_child", afero.NewMemMapFs())
		call := executeRegisteredCallerSend(t, child, delegateLease{delegateID: "dlg_child", generation: 1}, "late report")
		if !call.IsError || !strings.Contains(call.Output, "target_busy") {
			t.Fatalf("idle-parent caller result = error:%v output:%q, want target_busy", call.IsError, call.Output)
		}
	})

	t.Run("stale delegate lease rejects caller", func(t *testing.T) {
		c, _ := newDelegateControllerTestHarness(t, 1, 1)
		_ = newRegisteredCallerRoot(t, c, afero.NewMemMapFs())
		seedDelegateControllerRunning(t, c, "dlg_child", "")
		child := attachRegisteredCallerRuntime(t, c, "dlg_child", afero.NewMemMapFs())
		call := executeRegisteredCallerSend(t, child, delegateLease{delegateID: "dlg_child", generation: 2}, "stale report")
		if !call.IsError || !strings.Contains(call.Output, "stale") {
			t.Fatalf("stale caller result = error:%v output:%q, want stale lease", call.IsError, call.Output)
		}
	})
}

func TestDelegateResourceCaller_RegisteredDoesNotAppendIntoUnfinishedRootToolRound(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	fs := newDelegateToolResultBarrierFS()
	root := newRegisteredCallerRoot(t, c, fs)
	seedDelegateControllerRunning(t, c, "dlg_child", "")
	child := attachRegisteredCallerRuntime(t, c, "dlg_child", afero.NewMemMapFs())

	fs.blockSync = true
	var releaseOnce sync.Once
	releaseSync := func() { releaseOnce.Do(func() { close(fs.allowSync) }) }
	defer releaseSync()
	toolResultDone := make(chan error, 1)
	go func() {
		msg := llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "root-call", Name: "delegate", Content: `{"status":"running"}`}}}}
		toolResultDone <- root.appendTurnWithDurableTranscriptMessage(schema.TurnToolResults, msg, msg)
	}()
	select {
	case <-fs.syncEntered:
	case err := <-toolResultDone:
		t.Fatalf("root tool-result append returned before fsync barrier: %v", err)
	}

	call := executeRegisteredCallerSend(t, child, delegateLease{delegateID: "dlg_child", generation: 1}, "arrived during root tool round")
	if call.IsError {
		t.Fatalf("registered caller send during root tool round: %s", call.Output)
	}
	queue := root.SteeringQueueSnapshot()
	if len(queue) != 1 || queue[0].Text != "arrived during root tool round" {
		t.Fatalf("root steering queue during blocked tool result = %#v", queue)
	}
	persisted, _, err := loadQueues(root.stateDir, root.id)
	if err != nil || len(persisted) != 1 || persisted[0].Text != "arrived during root tool round" {
		t.Fatalf("durable root queue during blocked tool result = %#v err=%v", persisted, err)
	}
	root.mu.Lock()
	for _, turn := range root.history {
		if turn.Kind == schema.TurnSteering {
			root.mu.Unlock()
			t.Fatalf("caller appended directly into unfinished root tool round: %#v", turn)
		}
	}
	root.mu.Unlock()

	releaseSync()
	if err := <-toolResultDone; err != nil {
		t.Fatalf("finish root tool-result append: %v", err)
	}
	entries := decodeTranscriptEntries(t, fs.Fs, "/root.jsonl")
	if len(entries) != 1 || entries[0].Turn.Kind != schema.TurnToolResults {
		t.Fatalf("root transcript before steering drain = %#v, want only completed tool result", entries)
	}
}

func newRegisteredCallerRoot(t *testing.T, c *delegateTreeController, fs afero.Fs) *Session {
	t.Helper()
	writer, err := transcript.NewWriterWithFS(fs, "/root.jsonl", transcript.Header{SessionID: "root-session"})
	if err != nil {
		t.Fatalf("create root transcript: %v", err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	root := &Session{
		id:                    "root-session",
		stateDir:              t.TempDir(),
		delegateController:    c,
		delegateRootSessionID: "root-session",
		reg:                   toolpkg.NewRegistry(),
	}
	root.attachTranscript(writer)
	c.rootRuntime = root
	if err := registerStableDelegateTool(root.reg, root); err != nil {
		t.Fatalf("register root stable delegate tool: %v", err)
	}
	return root
}

func attachRegisteredCallerRuntime(t *testing.T, c *delegateTreeController, delegateID string, fs afero.Fs) *Session {
	t.Helper()
	runtime := attachDelegateSteerRuntime(t, c, delegateID, fs)
	runtime.id = "child-" + delegateID
	runtime.stateDir = t.TempDir()
	runtime.delegateController = c
	runtime.delegateRootSessionID = "root-session"
	runtime.owningDelegateID = delegateID
	runtime.reg = toolpkg.NewRegistry()
	if err := registerStableDelegateTool(runtime.reg, runtime); err != nil {
		t.Fatalf("register stable delegate tool for %s: %v", delegateID, err)
	}
	return runtime
}

func executeRegisteredCallerSend(t *testing.T, s *Session, lease delegateLease, message string) toolpkg.ExecResult {
	t.Helper()
	ctx := context.WithValue(context.Background(), delegateRunLeaseContextKey{}, lease)
	return executeRegisteredCallerSendContext(ctx, t, s, message)
}

func executeRegisteredCallerSendWithoutLease(t *testing.T, s *Session, message string) toolpkg.ExecResult {
	t.Helper()
	return executeRegisteredCallerSendContext(context.Background(), t, s, message)
}

func executeRegisteredCallerSendContext(ctx context.Context, t *testing.T, s *Session, message string) toolpkg.ExecResult {
	t.Helper()
	args, err := json.Marshal(map[string]any{"to": runtimeMessageAliasCaller, "message": message})
	if err != nil {
		t.Fatal(err)
	}
	return s.reg.ExecuteCall(ctx, s.env, llm.ToolCallData{ID: "caller-send", Name: "delegate_send", Arguments: args})
}

func registeredCallerState(t *testing.T, call toolpkg.ExecResult) map[string]any {
	t.Helper()
	var state map[string]any
	if err := json.Unmarshal(call.ToolState, &state); err != nil {
		t.Fatalf("decode caller tool state: %v; raw=%q", err, call.ToolState)
	}
	return state
}
