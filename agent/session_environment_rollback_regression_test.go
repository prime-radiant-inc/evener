package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/spf13/afero"
	"os"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
	"strings"
	"sync"
	"testing"
)

type envFailFS struct {
	afero.Fs
	mu   sync.Mutex
	fail bool
}
type envFailFile struct {
	afero.File
	fs *envFailFS
}

func (f *envFailFS) OpenFile(n string, m int, p os.FileMode) (afero.File, error) {
	x, e := f.Fs.OpenFile(n, m, p)
	if e != nil {
		return nil, e
	}
	return &envFailFile{File: x, fs: f}, nil
}
func (f *envFailFS) Create(n string) (afero.File, error) {
	x, e := f.Fs.Create(n)
	if e != nil {
		return nil, e
	}
	return &envFailFile{File: x, fs: f}, nil
}
func (f *envFailFile) Sync() error {
	f.fs.mu.Lock()
	defer f.fs.mu.Unlock()
	if f.fs.fail {
		f.fs.fail = false
		return errors.New("environment transcript durability failure")
	}
	return f.File.Sync()
}
func TestRestoreDeferredHookWaitsForEnvironmentDurability(t *testing.T) {
	a := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{func(llm.Request) llm.Response { return finalResponse("ok") }}}
	s := restoredSessionWithResumeHook(t, a)
	defer s.Close()
	fs := &envFailFS{Fs: afero.NewOsFs(), fail: true}
	_ = s.closeAttachedTranscript()
	w, _, e := transcript.OpenWriterForSessionWithFS(fs, s.TranscriptPath(), s.ID())
	if e != nil {
		t.Fatal(e)
	}
	s.attachTranscript(w)
	if _, e = s.ProcessInput(t.Context(), "first", nil); e == nil {
		t.Fatal("first input crossed env failure")
	}
	h := sessionHistoryText(s)
	if strings.Contains(h, "RESUME_HOOK_CONTEXT") || strings.Contains(h, "RESUME_HOOK_USER_MESSAGE") {
		t.Fatalf("failed input consumed hook: %q", h)
	}
	if _, e = s.ProcessInput(t.Context(), "retry", nil); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(sessionHistoryText(s), "RESUME_HOOK_CONTEXT") {
		t.Fatal("retry omitted hook")
	}
	if len(a.Requests()) != 1 {
		t.Fatalf("requests=%d", len(a.Requests()))
	}
}
func TestPushQueueHeadReturnsDurabilityFailure(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	defer s.Close()
	id := "rollback-error"
	_, e := s.clientMutations.reserve(clientMutationRequest{Method: "turn/queue", ClientMutationID: id, Payload: []byte(`{"x":1}`), PayloadHash: func() string { h := sha256.Sum256([]byte(`{"x":1}`)); return hex.EncodeToString(h[:]) }()})
	if e != nil {
		t.Fatal(e)
	}
	e = s.clientMutations.mutate(func(n *clientMutationSnapshot) error {
		r := n.Journal[id]
		r.Method = clientMutationMethodQueue
		r.StableTurnID = "turn-rollback"
		r.ExecutionState = "claimed"
		n.Journal[id] = r
		n.PendingExecutions[id] = appwire.PendingMutation{ClientMutationID: id, Method: clientMutationMethodQueue, ExecutionState: "claimed", TurnID: "turn-rollback"}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	want := errors.New("queue rollback durability failure")
	s.clientMutations.faults.BeforeEffectSnapshotRename = func() error { return want }
	if e = s.pushQueueHead(queuedInput{ID: "queue-entry", ClientMutationID: id, StableTurnID: "turn-rollback", Text: "retry"}); !errors.Is(e, want) {
		t.Fatalf("error=%v", e)
	}
	if s.clientMutations.snapshot().PendingExecutions[id].ExecutionState != "claimed" {
		t.Fatal("state changed")
	}
}
