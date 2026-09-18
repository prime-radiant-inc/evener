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

func TestArtifactServiceProcess(t *testing.T) {
	if len(os.Args) < 2 || os.Args[len(os.Args)-1] != "artifact-process" {
		return
	}
	if err := RunInheritedService(); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}
func processSupervisor(t *testing.T, root string, policy func(context.Context) ([]NamespacePolicy, error)) *Supervisor {
	t.Helper()
	s := NewSupervisor(root, SupervisorOptions{Policy: policy, command: []string{os.Args[0], "-test.run=^TestArtifactServiceProcess$", "--", "artifact-process"}})
	t.Cleanup(func() { requireNoError(t, s.Close()) })
	return s
}
func testPolicy(context.Context) ([]NamespacePolicy, error) {
	return []NamespacePolicy{{NamespaceID: "namespace", RealmID: "realm", OwnerThreadID: "owner"}}, nil
}
func TestSupervisorActual100ClientsAndOneOwnedProcess(t *testing.T) {
	ctx := context.Background()
	s := processSupervisor(t, filepath.Join(t.TempDir(), "private"), testPolicy)
	if s.Status().ProcessesStarted != 0 {
		t.Fatal("eager service launch")
	}
	start := time.Now()
	var wg sync.WaitGroup
	durations := make(chan time.Duration, 100)
	for i := 0; i < 100; i++ {
		wg.Go(func() {
			before := time.Now()
			scope := testScope()
			scope.PrincipalID = fmt.Sprintf("principal-%d", i)
			lease, err := s.Grant(ctx, scope)
			if err != nil {
				t.Error(err)
				return
			}
			c, err := mcp.NewClient(&mcp.Implementation{Name: "direct", Version: "1"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: lease.Readiness.Endpoint, HTTPClient: NewHTTPClient(lease.Token)}, nil)
			if err != nil {
				t.Error(err)
				return
			}
			defer c.Close()
			result, err := c.CallTool(ctx, &mcp.CallToolParams{Name: "artifact_publish", Arguments: json.RawMessage(createJSON(fmt.Sprintf("p%d", i)))})
			if err != nil || result.IsError {
				t.Errorf("publish: %v %+v", err, result)
				return
			}
			list, err := c.CallTool(ctx, &mcp.CallToolParams{Name: "artifact_list", Arguments: map[string]any{}})
			if err != nil || list.IsError {
				t.Errorf("warm list: %v", err)
				return
			}
			durations <- time.Since(before)
		})
	}
	wg.Wait()
	close(durations)
	var total time.Duration
	var count int
	for d := range durations {
		total += d
		count++
	}
	status := s.Status()
	if count != 100 || status.ProcessesStarted != 1 || status.LiveProcesses != 1 {
		t.Fatalf("100-client evidence: completed=%d status=%+v", count, status)
	}
	t.Logf("actual direct clients=%d backend children=%d artifact shims=0 wall=%s mean acquisition+initialize+publish+list=%s pid=%d", count, status.LiveProcesses, time.Since(start), total/time.Duration(max(count, 1)), status.PID)
	requireNoError(t, s.Close())
	if s.Status().LiveProcesses != 0 || s.Status().ProcessesReaped != 1 {
		t.Fatal("owned child not reaped")
	}
}
func TestSupervisorOwnerLockAndRestartReceipt(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "private")
	s := processSupervisor(t, root, testPolicy)
	lease, err := s.Grant(ctx, testScope())
	requireNoError(t, err)
	c := sdkClient(t, lease.Readiness.Endpoint, lease.Token)
	first, err := c.CallTool(ctx, &mcp.CallToolParams{Name: "artifact_publish", Arguments: json.RawMessage(createJSON("restart"))})
	requireNoError(t, err)
	other := processSupervisor(t, root, testPolicy)
	if _, err := other.Ensure(ctx); err == nil {
		t.Fatal("second supervisor opened live writer")
	}
	requireNoError(t, other.Close())
	requireNoError(t, s.Close())
	next := processSupervisor(t, root, testPolicy)
	fresh, err := next.Grant(ctx, testScope())
	requireNoError(t, err)
	if fresh.Readiness.ServiceID != lease.Readiness.ServiceID || fresh.Readiness.ServiceRunID == lease.Readiness.ServiceRunID || fresh.Token == lease.Token {
		t.Fatal("restart identity/grant")
	}
	old, err := mcp.NewClient(&mcp.Implementation{Name: "old", Version: "1"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: fresh.Readiness.Endpoint, HTTPClient: NewHTTPClient(lease.Token)}, nil)
	if err == nil {
		_ = old.Close()
		t.Fatal("old-run grant survived")
	}
	nc := sdkClient(t, fresh.Readiness.Endpoint, fresh.Token)
	retry, err := nc.CallTool(ctx, &mcp.CallToolParams{Name: "artifact_publish", Arguments: json.RawMessage(createJSON("restart"))})
	requireNoError(t, err)
	a, _ := json.Marshal(first.StructuredContent)
	b, _ := json.Marshal(retry.StructuredContent)
	if string(a) != string(b) {
		t.Fatalf("receipt changed: %s %s", a, b)
	}
}
