package interactiveartifacts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestArtifactServiceProcess(t *testing.T) {
	mode := os.Args[len(os.Args)-1]
	if mode != "artifact-process" && mode != "artifact-process-barrier" {
		return
	}
	options := StoreOptions{}
	if mode == "artifact-process-barrier" {
		syscall.CloseOnExec(5)
		syscall.CloseOnExec(6)
		events := os.NewFile(5, "commit-events")
		resume := os.NewFile(6, "commit-resume")
		var once sync.Once
		options.hooks.afterCommit = func() {
			once.Do(func() { _, _ = events.Write([]byte{1}); var signal [1]byte; _, _ = io.ReadFull(resume, signal[:]) })
		}
	}
	if err := runInheritedService(options); err != nil {
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
	s := processSupervisor(t, filepath.Join(t.TempDir(), "private"), func(context.Context) ([]NamespacePolicy, error) {
		policies := make([]NamespacePolicy, 100)
		for i := range policies {
			policies[i] = NamespacePolicy{NamespaceID: fmt.Sprintf("namespace-%d", i), RealmID: "realm", OwnerThreadID: fmt.Sprintf("owner-%d", i)}
		}
		return policies, nil
	})
	if s.Status().ProcessesStarted != 0 {
		t.Fatal("eager service launch")
	}
	start := time.Now()
	var wg sync.WaitGroup
	durations := make(chan time.Duration, 100)
	for i := range 100 {
		wg.Go(func() {
			before := time.Now()
			scope := testScope()
			scope.PrincipalID = fmt.Sprintf("principal-%d", i)
			scope.NamespaceID = fmt.Sprintf("namespace-%d", i)
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
			encoded, _ := json.Marshal(list.StructuredContent)
			var page ListResult
			if err := json.Unmarshal(encoded, &page); err != nil || len(page.Artifacts) != 1 {
				t.Errorf("namespace isolation: %v %s", err, encoded)
				return
			}
			durations <- time.Since(before)
		})
	}
	wg.Wait()
	close(durations)
	var total time.Duration
	var count int
	var latencies []time.Duration
	for d := range durations {
		latencies = append(latencies, d)
		total += d
		count++
	}
	status := s.Status()
	if count != 100 || status.ProcessesStarted != 1 || status.LiveProcesses != 1 {
		t.Fatalf("100-client evidence: completed=%d status=%+v", count, status)
	}
	t.Logf("actual direct clients=%d backend children=%d artifact shims=0 wall=%s mean acquisition+initialize+publish+list=%s pid=%d", count, status.LiveProcesses, time.Since(start), total/time.Duration(max(count, 1)), status.PID)
	slices.Sort(latencies)
	if len(latencies) == 100 {
		t.Logf("acquire+initialize+publish+list p50=%s p95=%s p99=%s", latencies[49], latencies[94], latencies[98])
	}
	if rss, err := exec.CommandContext(ctx, "ps", "-o", "rss=", "-p", strconv.Itoa(status.PID)).Output(); err == nil {
		t.Logf("owned backend RSS KiB=%s", strings.TrimSpace(string(rss)))
	}
	if fds, err := exec.CommandContext(ctx, "lsof", "-a", "-p", strconv.Itoa(status.PID), "-F", "f").Output(); err == nil {
		count := 0
		for line := range strings.SplitSeq(string(fds), "\n") {
			if len(line) > 1 && line[0] == 'f' {
				if _, err := strconv.Atoi(line[1:]); err == nil {
					count++
				}
			}
		}
		t.Logf("owned backend numeric file descriptors=%d", count)
	}
	metrics, err := s.Metrics(ctx)
	requireNoError(t, err)
	t.Logf("owned backend admission=%+v", metrics)
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
	if !bytes.Equal(a, b) {
		t.Fatalf("receipt changed: %s %s", a, b)
	}
}

// This opt-in fixture accepts only a freshly built real repository executable;
// default process tests above invoke the same production service entry directly.
func TestArtifactInstalledCommandPath(t *testing.T) {
	binary := os.Getenv("EVENER_ARTIFACT_TEST_BINARY")
	if binary == "" {
		t.Skip("set EVENER_ARTIFACT_TEST_BINARY to a real go build ./cmd/evener output")
	}
	s := NewSupervisor(filepath.Join(t.TempDir(), "private"), SupervisorOptions{Policy: testPolicy, command: []string{binary, "hub", "artifact-service"}})
	t.Cleanup(func() { requireNoError(t, s.Close()) })
	lease, err := s.Grant(context.Background(), testScope())
	requireNoError(t, err)
	c := sdkClient(t, lease.Readiness.Endpoint, lease.Token)
	result, err := c.CallTool(context.Background(), &mcp.CallToolParams{Name: "artifact_publish", Arguments: json.RawMessage(createJSON("installed"))})
	requireNoError(t, err)
	if result.IsError || s.Status().ProcessesStarted != 1 {
		t.Fatal("installed command did not serve the real store")
	}
	t.Logf("real installed command path readiness=%+v", lease.Readiness)
}
