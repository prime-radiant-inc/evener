package interactiveartifacts

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func awaitSupervisor(t *testing.T, s *Supervisor, predicate func(ServiceStatus) bool) ServiceStatus {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for {
		s.mu.Lock()
		status, changed := s.status, s.changed
		s.mu.Unlock()
		if predicate(status) {
			return status
		}
		select {
		case <-changed:
		case <-ctx.Done():
			t.Fatalf("supervisor did not reach state: %+v", status)
		}
	}
}
func TestSupervisorCircuitUsesRealFailedChildBoundaries(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private")
	owner, err := startService(root, StoreOptions{})
	requireNoError(t, err)
	waits := make(chan time.Duration)
	advance := make(chan struct{})
	s := NewSupervisor(root, SupervisorOptions{Policy: testPolicy, command: []string{os.Args[0], "-test.run=^TestArtifactServiceProcess$", "--", "artifact-process"}, Now: fixedClock, Jitter: func() float64 { return 0.5 }, Wait: func(ctx context.Context, d time.Duration) error {
		select {
		case waits <- d:
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case <-advance:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}})
	t.Cleanup(func() { requireNoError(t, s.Close()) })
	if _, err := s.Ensure(context.Background()); err == nil {
		t.Fatal("second writer started")
	}
	for _, want := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second} {
		if got := <-waits; got != want {
			t.Fatalf("backoff: got %s want %s", got, want)
		}
		advance <- struct{}{}
	}
	status := awaitSupervisor(t, s, func(st ServiceStatus) bool { return st.CircuitOpen })
	if status.ProcessesStarted != 5 || status.ProcessesReaped != 5 || status.LiveProcesses != 0 {
		t.Fatalf("failed children unreaped: %+v", status)
	}
	requireNoError(t, owner.close(context.Background()))
	ready, err := s.Retry(context.Background())
	requireNoError(t, err)
	if ready.ServiceID == "" || s.Status().ProcessesStarted != 6 {
		t.Fatal("explicit retry did not recover")
	}
}
func TestSupervisorPolicyBeforeGrantAndIndependentCanceledAcquisition(t *testing.T) {
	entered := make(chan struct{})
	resume := make(chan struct{})
	s := processSupervisor(t, filepath.Join(t.TempDir(), "private"), func(ctx context.Context) ([]NamespacePolicy, error) {
		close(entered)
		select {
		case <-resume:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return []NamespacePolicy{{NamespaceID: "deleted", RealmID: "realm", OwnerThreadID: "owner", Tombstone: true}, {NamespaceID: "namespace", RealmID: "realm", OwnerThreadID: "owner"}}, nil
	})
	canceled, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { _, err := s.Ensure(canceled); first <- err }()
	<-entered
	cancel()
	if err := <-first; err == nil {
		t.Fatal("canceled acquisition succeeded")
	}
	if s.Status().State == "ready" {
		t.Fatal("grants preceded policy replay")
	}
	close(resume)
	grant, err := s.Grant(context.Background(), testScope())
	requireNoError(t, err)
	if grant.Token == "" || s.Status().ProcessesStarted != 1 {
		t.Fatal("acquisition cancellation killed shared child")
	}
	scope := testScope()
	scope.NamespaceID = "deleted"
	_, err = s.Grant(context.Background(), scope)
	requireCode(t, err, NotFoundOrForbidden)
}
func TestSupervisorUnexpectedExitAutomaticallyRestarts(t *testing.T) {
	waits := make(chan time.Duration)
	advance := make(chan struct{})
	s := NewSupervisor(filepath.Join(t.TempDir(), "private"), SupervisorOptions{Policy: testPolicy, command: []string{os.Args[0], "-test.run=^TestArtifactServiceProcess$", "--", "artifact-process"}, Wait: func(ctx context.Context, d time.Duration) error {
		select {
		case waits <- d:
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case <-advance:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}})
	t.Cleanup(func() { requireNoError(t, s.Close()) })
	first, err := s.Grant(context.Background(), testScope())
	requireNoError(t, err)
	s.mu.Lock()
	owned := s.child
	s.mu.Unlock()
	restartStart := time.Now()
	requireNoError(t, owned.cmd.Process.Kill())
	<-waits
	if s.Status().LiveProcesses != 0 || s.Status().ProcessesReaped != 1 {
		t.Fatal("restart before reaping")
	}
	advance <- struct{}{}
	status := awaitSupervisor(t, s, func(st ServiceStatus) bool { return st.State == "ready" })
	if status.Readiness.ServiceID != first.Readiness.ServiceID || status.Readiness.ServiceRunID == first.Readiness.ServiceRunID || status.ProcessesStarted != 2 {
		t.Fatalf("restart: %+v", status)
	}
	t.Logf("observed owned-child kill/reap, injected wait acknowledgment, replacement readiness=%s", time.Since(restartStart))
	fresh, err := s.Grant(context.Background(), testScope())
	requireNoError(t, err)
	c := sdkClient(t, fresh.Readiness.Endpoint, fresh.Token)
	result, err := c.CallTool(context.Background(), &mcp.CallToolParams{Name: "artifact_list", Arguments: json.RawMessage(`{}`)})
	requireNoError(t, err)
	if result.IsError {
		t.Fatal("replacement unusable")
	}
}

func TestServiceControlEOFExitsOwnedProcess(t *testing.T) {
	wait := make(chan struct{})
	s := NewSupervisor(filepath.Join(t.TempDir(), "private"), SupervisorOptions{Policy: testPolicy, command: []string{os.Args[0], "-test.run=^TestArtifactServiceProcess$", "--", "artifact-process"}, Wait: func(ctx context.Context, _ time.Duration) error { close(wait); <-ctx.Done(); return ctx.Err() }})
	t.Cleanup(func() { requireNoError(t, s.Close()) })
	_, err := s.Ensure(context.Background())
	requireNoError(t, err)
	s.mu.Lock()
	child := s.child
	s.mu.Unlock()
	// Closing the sole parent control stream reproduces parent death without
	// addressing an arbitrary PID. The child must exit of its own accord.
	requireNoError(t, child.client.Close())
	<-child.done
	<-wait
	if !child.cmd.ProcessState.Success() || s.Status().ProcessesReaped != 1 {
		t.Fatalf("control EOF did not drain and exit: %v %+v", child.cmd.ProcessState, s.Status())
	}
}

func TestSupervisorObservedExitWithdrawsReadiness(t *testing.T) {
	for _, startup := range []bool{false, true} {
		t.Run(fmt.Sprintf("during-startup-%v", startup), func(t *testing.T) {
			reaped, releaseReap := make(chan struct{}), make(chan struct{})
			policyEntered, releasePolicy := make(chan struct{}), make(chan struct{})
			backoff := make(chan struct{})
			var reapOnce, policyOnce sync.Once
			resume := func() { reapOnce.Do(func() { close(releaseReap) }); policyOnce.Do(func() { close(releasePolicy) }) }
			s := NewSupervisor(filepath.Join(t.TempDir(), "private"), SupervisorOptions{
				command: []string{os.Args[0], "-test.run=^TestArtifactServiceProcess$", "--", "artifact-process"},
				Policy: func(context.Context) ([]NamespacePolicy, error) {
					if startup {
						close(policyEntered)
						<-releasePolicy
					}
					return nil, nil
				},
				afterReap: func() { close(reaped); <-releaseReap },
				Wait:      func(ctx context.Context, _ time.Duration) error { close(backoff); <-ctx.Done(); return ctx.Err() },
			})
			t.Cleanup(func() { resume(); requireNoError(t, s.Close()) })
			acquired := make(chan error, 1)
			go func() { _, err := s.Ensure(t.Context()); acquired <- err }()
			if startup {
				<-policyEntered
			} else {
				requireNoError(t, <-acquired)
			}
			// This is the exact child PID just started by this supervisor; no process search.
			owned, err := os.FindProcess(s.Status().PID)
			requireNoError(t, err)
			requireNoError(t, owned.Kill())
			<-reaped
			status := s.Status()
			if status.State == "ready" || status.Readiness.Endpoint != "" || status.PID != 0 || status.LiveProcesses != 0 || status.ProcessesReaped != 1 {
				t.Errorf("observed dead child remains ready: %+v", status)
			}
			if startup {
				policyOnce.Do(func() { close(releasePolicy) })
				if err := <-acquired; err == nil {
					t.Error("published readiness after observed startup exit")
				}
			} else {
				if _, err := s.Ensure(t.Context()); err == nil {
					t.Error("Ensure returned observed dead child")
				}
			}
			reapOnce.Do(func() { close(releaseReap) })
			<-backoff
		})
	}
}
