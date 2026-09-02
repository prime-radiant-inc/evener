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

// sharedWorkspaceBarrierAdapter parks every model request until it is released,
// so a delegate launched in the background is observably RUNNING while the test
// launches the next one. Entries are buffered and sent non-blocking: a second
// parked child must not deadlock behind an undrained observation channel.
type sharedWorkspaceBarrierAdapter struct {
	entered chan string
	release chan struct{}
	once    sync.Once
}

func newSharedWorkspaceBarrierAdapter() *sharedWorkspaceBarrierAdapter {
	return &sharedWorkspaceBarrierAdapter{
		entered: make(chan string, 8),
		release: make(chan struct{}),
	}
}

func (a *sharedWorkspaceBarrierAdapter) Name() string { return "openai" }

func (a *sharedWorkspaceBarrierAdapter) Complete(ctx context.Context, request llm.Request) (llm.Response, error) {
	select {
	case a.entered <- request.SessionID:
	default:
	}
	select {
	case <-a.release:
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	}
	return llm.Response{Provider: "openai", Model: request.Model, Message: llm.Assistant("done")}, nil
}

func (a *sharedWorkspaceBarrierAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func (a *sharedWorkspaceBarrierAdapter) releaseRuns() { a.once.Do(func() { close(a.release) }) }

func (a *sharedWorkspaceBarrierAdapter) awaitRun(t *testing.T) {
	t.Helper()
	// TRIPWIRE: this is a scripted in-process adapter with no real I/O; the
	// delegate normally reaches its first model request in well under a
	// second. 30s only fires on a genuine hang, not scheduler contention
	// under a loaded suite.
	awaitWithin(t, 30*time.Second, "delegate's first model request", func() {
		<-a.entered
	})
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
	root, client, _ := newDelegateResourceBootstrapSession(t)
	adapter := newSharedWorkspaceBarrierAdapter()
	client.Register(adapter)
	t.Cleanup(adapter.releaseRuns)

	first := root.createDelegate(context.Background(), delegateArgs{Task: "hold the shared workspace"})
	if first.Err != nil || first.DelegateID == "" {
		t.Fatalf("first delegate did not launch: %+v", first)
	}
	if len(first.Warnings) != 0 {
		t.Fatalf("first shared delegate warned: %q", first.Warnings)
	}
	adapter.awaitRun(t)

	second := root.createDelegate(context.Background(), delegateArgs{Task: "join the shared workspace"})
	if second.Err != nil || second.DelegateID == "" {
		t.Fatalf("advisory blocked delegate launch: %+v", second)
	}
	if len(second.Warnings) != 1 {
		t.Fatalf("second delegate warnings = %q, want exactly one advisory", second.Warnings)
	}
	for _, want := range []string{root.currentEnv().WorkingDirectory(), `isolation="worktree"`, "shared workspace"} {
		if !strings.Contains(second.Warnings[0], want) {
			t.Fatalf("warning missing %q: %q", want, second.Warnings[0])
		}
	}
	adapter.awaitRun(t)

	// The tool projection must surface the advisory to the model, once.
	raw, err := stableDelegateCreateTool(context.Background(), root, map[string]any{"prompt": "third in the shared workspace"}, 8192)
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
