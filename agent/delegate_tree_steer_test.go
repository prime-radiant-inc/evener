package agent

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/spf13/afero"

	"primeradiant.com/serf/agent/internal/delegatestore"
	"primeradiant.com/serf/agent/transcript"
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
	if fs.controllerWasUnlocked {
		t.Fatal("controller mutex was not held at the transcript durability boundary")
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

	history, err := c.BeginModelRequest(lease)
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
	if _, err := c.BeginModelRequest(lease); err != nil {
		t.Fatalf("second BeginModelRequest: %v", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if got := c.live["dlg_target"].pendingSteers; len(got) != 0 {
		t.Fatalf("already-bound steers re-entered pending state: %#v", got)
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
	if _, err := c.BeginModelRequest(lease); err != nil {
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
	if _, err := c.BeginModelRequest(lease); err != nil {
		t.Fatalf("second BeginModelRequest: %v", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.live["dlg_target"].pendingSteers) != 0 {
		t.Fatalf("next request did not bind later steer: %#v", c.live["dlg_target"].pendingSteers)
	}
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
