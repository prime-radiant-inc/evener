package interactiveartifacts

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestServiceCommitLostAcknowledgmentAndControlCancellation(t *testing.T) {
	eventRead, eventWrite, err := os.Pipe()
	requireNoError(t, err)
	resumeRead, resumeWrite, err := os.Pipe()
	requireNoError(t, err)
	t.Cleanup(func() { _ = eventRead.Close(); _ = eventWrite.Close(); _ = resumeRead.Close(); _ = resumeWrite.Close() })
	waiting := make(chan struct{})
	advance := make(chan struct{})
	s := NewSupervisor(filepath.Join(t.TempDir(), "private"), SupervisorOptions{Policy: testPolicy, command: []string{os.Args[0], "-test.run=^TestArtifactServiceProcess$", "--", "artifact-process-barrier"}, extraFiles: []*os.File{eventWrite, resumeRead}, Wait: func(ctx context.Context, _ time.Duration) error {
		close(waiting)
		select {
		case <-advance:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}})
	t.Cleanup(func() { requireNoError(t, s.Close()) })
	lease, err := s.Grant(context.Background(), testScope())
	requireNoError(t, err)
	c := sdkClient(t, lease.Readiness.Endpoint, lease.Token)
	callDone := make(chan error, 1)
	go func() {
		_, err := c.CallTool(context.Background(), &mcp.CallToolParams{Name: "artifact_publish", Arguments: json.RawMessage(createJSON("lost-ack"))})
		callDone <- err
	}()
	// The signal is emitted by the real Store after SQLite commit and before its
	// caller can construct a response. It replaces every sleep/kill timing guess.
	var signal [1]byte
	_, err = io.ReadFull(eventRead, signal[:])
	requireNoError(t, err)
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Revoke(cancelCtx, "unrelated-token"); err == nil {
		t.Fatal("canceled operation succeeded")
	}
	s.mu.Lock()
	child := s.child
	s.mu.Unlock()
	requireNoError(t, child.cmd.Process.Kill())
	if err := <-callDone; err == nil {
		t.Fatal("lost acknowledgment reported success")
	}
	<-waiting
	if s.Status().ProcessesReaped != 1 {
		t.Fatal("restart before exact owned process exit")
	}
	close(advance)
	awaitSupervisor(t, s, func(status ServiceStatus) bool { return status.State == "ready" })
	fresh, err := s.Grant(context.Background(), testScope())
	requireNoError(t, err)
	nc := sdkClient(t, fresh.Readiness.Endpoint, fresh.Token)
	result, err := nc.CallTool(context.Background(), &mcp.CallToolParams{Name: "artifact_publish", Arguments: json.RawMessage(createJSON("lost-ack"))})
	requireNoError(t, err)
	data, _ := json.Marshal(result.StructuredContent)
	var receipt MutationReceipt
	requireNoError(t, json.Unmarshal(data, &receipt))
	if result.IsError || receipt.SourceRevision != 1 || receipt.StateVersion != 1 || receipt.MutationID != "lost-ack" {
		t.Fatalf("incorrect recovered receipt: %s", data)
	}
	list, err := nc.CallTool(context.Background(), &mcp.CallToolParams{Name: "artifact_list", Arguments: json.RawMessage(`{}`)})
	requireNoError(t, err)
	data, _ = json.Marshal(list.StructuredContent)
	var page ListResult
	requireNoError(t, json.Unmarshal(data, &page))
	if len(page.Artifacts) != 1 || page.Artifacts[0].ArtifactID != receipt.ArtifactID {
		t.Fatalf("duplicate commit: %s", data)
	}
}
