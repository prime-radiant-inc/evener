package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/llm"
)

// sharedWorkspaceBarrier parks every model request until it is released,
// so a delegate launched in the background is observably RUNNING while the test
// launches the next one. Entries are buffered and sent non-blocking: a second
// parked child must not deadlock behind an undrained observation channel.
type sharedWorkspaceBarrier struct {
	entered chan string
	release chan struct{}
	once    sync.Once

	mu     sync.Mutex
	parked map[string]int // calls each session has parked right now
}

func newSharedWorkspaceBarrier() *sharedWorkspaceBarrier {
	return &sharedWorkspaceBarrier{
		entered: make(chan string, 8),
		release: make(chan struct{}),
		parked:  map[string]int{},
	}
}

// adapter answers every session's calls with park.
func (a *sharedWorkspaceBarrier) adapter() *sessionScriptAdapter {
	adapter := newSessionScriptAdapter("openai")
	adapter.otherwise = a.park
	return adapter
}

func (a *sharedWorkspaceBarrier) park(ctx context.Context, request llm.Request) (llm.Response, error) {
	a.mu.Lock()
	a.parked[request.SessionID]++
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.parked[request.SessionID]--
		a.mu.Unlock()
	}()
	select {
	case a.entered <- request.SessionID:
	default:
	}
	select {
	case <-a.release:
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	}
	return llm.Response{Message: llm.Assistant("done")}, nil
}

func (a *sharedWorkspaceBarrier) releaseRuns() { a.once.Do(func() { close(a.release) }) }

// isParked reports whether sessionID has a model call parked right now.
func (a *sharedWorkspaceBarrier) isParked(sessionID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.parked[sessionID] > 0
}

// awaitRun waits for a delegate's first model request and returns the
// session that made it.
func (a *sharedWorkspaceBarrier) awaitRun(t *testing.T) string {
	t.Helper()
	var sessionID string
	// TRIPWIRE: this is a scripted in-process adapter with no real I/O; the
	// delegate normally reaches its first model request in well under a
	// second. 30s only fires on a genuine hang, not scheduler contention
	// under a loaded suite.
	awaitWithin(t, 30*time.Second, "delegate's first model request", func() {
		sessionID = <-a.entered
	})
	return sessionID
}

// TestSharedWorkspaceDelegateWarning covers the advisory's trigger table: it
// fires only when a NEW non-isolated delegate would join a RUNNING non-isolated
// sibling already working in the same directory.
func TestSharedWorkspaceDelegateWarning(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name               string
		requestedIsolation string
		existingDelegate   bool
		existingIsolation  string
		existingElsewhere  bool
		existingRunning    bool
		existingDriving    bool
		wantWarning        bool
	}{
		{name: "first shared delegate", wantWarning: false},
		{name: "second shared delegate in the same directory", existingDelegate: true, existingRunning: true, wantWarning: true},
		{name: "existing shared delegate is mid drive-down turn", existingDelegate: true, existingDriving: true, wantWarning: true},
		{name: "new delegate takes worktree isolation", requestedIsolation: "worktree", existingDelegate: true, existingRunning: true, wantWarning: false},
		{name: "existing delegate is worktree isolated", existingDelegate: true, existingIsolation: "worktree", existingRunning: true, wantWarning: false},
		{name: "existing shared delegate works elsewhere", existingDelegate: true, existingElsewhere: true, existingRunning: true, wantWarning: false},
		{name: "existing shared delegate is idle", existingDelegate: true, wantWarning: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			parentDir := t.TempDir()
			parent := newSession(t, withDir(parentDir), withoutGitSnapshot())
			if tc.existingDelegate {
				childDir := parentDir
				if tc.existingElsewhere {
					childDir = t.TempDir()
				}
				child := newSession(t, withDir(childDir), withoutGitSnapshot())
				parent.subagents.track(&subagent{
					id:               child.id,
					sess:             child,
					stableDescriptor: &delegatestore.Descriptor{Isolation: tc.existingIsolation},
					running:          tc.existingRunning,
					driving:          tc.existingDriving,
				})
			}

			warning := parent.sharedWorkspaceDelegateWarning(tc.requestedIsolation)
			if !tc.wantWarning {
				if warning != "" {
					t.Fatalf("sharedWorkspaceDelegateWarning = %q, want no advisory", warning)
				}
				return
			}
			for _, want := range []string{parentDir, `isolation="worktree"`, "shared workspace"} {
				if !strings.Contains(warning, want) {
					t.Fatalf("warning missing %q: %q", want, warning)
				}
			}
		})
	}
}

// TestSharedWorkspaceDelegateWarning_ClosedDelegateIsNotRunning proves the
// advisory reads live children only: a torn-down record retained as terminal
// history must not make a fresh delegate look like a collision.
func TestSharedWorkspaceDelegateWarning_ClosedDelegateIsNotRunning(t *testing.T) {
	t.Parallel()
	parentDir := t.TempDir()
	parent := newSession(t, withDir(parentDir), withoutGitSnapshot())
	child := newSession(t, withDir(parentDir), withoutGitSnapshot())
	parent.subagents.track(&subagent{id: child.id, sess: child, running: true, closed: true})

	if warning := parent.sharedWorkspaceDelegateWarning(""); warning != "" {
		t.Fatalf("closed delegate produced advisory %q", warning)
	}
}

// TestCreateDelegateSharedWorkspaceAdvisory drives the real create path: the
// second concurrently running shared delegate still launches and carries exactly
// one advisory, and the first one carries none.
func TestCreateDelegateSharedWorkspaceAdvisory(t *testing.T) {
	t.Parallel()
	root, client, _ := newDelegateResourceBootstrapSession(t)
	barrier := newSharedWorkspaceBarrier()
	client.Register(barrier.adapter())
	t.Cleanup(barrier.releaseRuns)

	first := root.createDelegate(context.Background(), delegateArgs{Task: "hold the shared workspace"})
	if first.Err != nil || first.DelegateID == "" {
		t.Fatalf("first delegate did not launch: %+v", first)
	}
	if len(first.Warnings) != 0 {
		t.Fatalf("first shared delegate warned: %q", first.Warnings)
	}
	firstSession := barrier.awaitRun(t)

	second := root.createDelegate(context.Background(), delegateArgs{Task: "join the shared workspace"})
	if second.Err != nil || second.DelegateID == "" {
		t.Fatalf("advisory blocked delegate launch: %+v", second)
	}
	// Nothing releases the barrier before cleanup, so a first delegate still
	// parked now was parked, and so running, while the second launched.
	if !barrier.isParked(firstSession) {
		t.Fatal("the first delegate's model call was not parked while the second launched")
	}
	if len(second.Warnings) != 1 {
		t.Fatalf("second delegate warnings = %q, want exactly one advisory", second.Warnings)
	}
	for _, want := range []string{root.currentEnv().WorkingDirectory(), `isolation="worktree"`, "shared workspace"} {
		if !strings.Contains(second.Warnings[0], want) {
			t.Fatalf("warning missing %q: %q", want, second.Warnings[0])
		}
	}
	barrier.awaitRun(t)

	// The tool projection must surface the advisory to the model, once.
	raw, err := delegateCreateTool(context.Background(), root, map[string]any{"prompt": "third in the shared workspace"}, 8192)
	if err != nil {
		t.Fatalf("delegate tool: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("unmarshal delegate result: %v (%s)", err, raw)
	}
	warnings, ok := decoded["warnings"].([]any)
	if !ok || len(warnings) != 1 {
		t.Fatalf("delegate result warnings = %#v, want exactly one advisory (%s)", decoded["warnings"], raw)
	}
	if text, ok := warnings[0].(string); !ok || !strings.Contains(text, "shared workspace") {
		t.Fatalf("delegate result warning = %#v, want the shared-workspace advisory", warnings[0])
	}
}
