package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func forkWithSessionMemory(t *testing.T, seed bool) (root, parentID string, child *Session) {
	t.Helper()
	root, history, workspace := t.TempDir(), t.TempDir(), t.TempDir()
	p := newSession(t, withDir(workspace), withConfig(SessionConfig{StateDir: history, MemoryStateRoot: root}), withSteps(func(llm.Request) llm.Response { return finalResponse("done") }))
	if _, err := p.ProcessInput(context.Background(), "parent turn", nil); err != nil {
		t.Fatal(err)
	}
	if seed {
		memorySeed(t, root, filepath.Join("sessions", p.id), "opaque-parent-31\n")
	}
	p.Close()
	childID, err := AsideSession(history, p.id)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := schema.LoadSessionMeta(history, childID)
	if err != nil {
		t.Fatal(err)
	}
	c, err := RestoreSessionFromMetaWithConfig(p.client, p.profile, execenv.NewLocalExecutionEnvironment(workspace), meta, RestoreSessionConfig{StateDir: history, MemoryStateRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return root, p.id, c
}

func hasWarningContaining(evs []events.SessionEvent, substr string) bool {
	for _, ev := range evs {
		if d, ok := ev.Data.(events.WarningData); ok && ev.Kind == events.EventWarning && strings.Contains(d.Message, substr) {
			return true
		}
	}
	return false
}

func TestMemoryForkCopiesParentSessionMemory(t *testing.T) {
	t.Parallel()
	root, parentID, c := forkWithSessionMemory(t, true)
	if _, err := c.execMemoryRead(context.Background(), nil, map[string]any{"scope": "session", "file_path": "MEMORY.md"}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, "memory", "sessions", c.id, "MEMORY.md"))
	if err != nil || string(got) != "opaque-parent-31\n" {
		t.Fatalf("child copy=%q err=%v", got, err)
	}
	// Divergence: a later parent change does not reach the child, and seeding
	// again leaves the existing child directory alone.
	memorySeed(t, root, filepath.Join("sessions", parentID), "opaque-parent-later-32\n")
	c.seedForkedSessionMemory()
	got, _ = os.ReadFile(filepath.Join(root, "memory", "sessions", c.id, "MEMORY.md"))
	if string(got) != "opaque-parent-31\n" {
		t.Fatalf("child re-copied parent: %q", got)
	}
}

func TestMemoryForkWithoutParentMemoryStartsEmpty(t *testing.T) {
	t.Parallel()
	root, _, c := forkWithSessionMemory(t, false)
	seen, stop := captureEvents(c)
	if _, err := c.execMemoryWrite(context.Background(), nil, map[string]any{"scope": "session", "file_path": "MEMORY.md", "content": "opaque-child-33\n"}); err != nil {
		t.Fatal(err)
	}
	stop()
	if _, err := os.Stat(filepath.Join(root, "memory", "sessions", c.id, "MEMORY.md")); err != nil {
		t.Fatal(err)
	}
	if hasWarningContaining(*seen, "could not copy the parent session's memory") {
		t.Fatal("a missing parent scope was reported as a copy failure")
	}
}

func TestMemoryForkIgnoresSymlinkedParentSessionDir(t *testing.T) {
	t.Parallel()
	root, parentID, c := forkWithSessionMemory(t, false)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "MEMORY.md"), []byte("opaque-outside-35\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(root, "memory", "sessions")
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(base, parentID)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(base, parentID)); err != nil {
		t.Fatal(err)
	}
	c.seedForkedSessionMemory()
	if got, err := os.ReadFile(filepath.Join(base, c.id, "MEMORY.md")); err == nil {
		t.Fatalf("child copied through the symlink: %q", got)
	}
}

func TestMemoryForkCopyFailureDoesNotBlock(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permissions")
	}
	root, parentID, c := forkWithSessionMemory(t, true)
	parentDir := filepath.Join(root, "memory", "sessions", parentID)
	if err := os.Chmod(parentDir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parentDir, 0o700) })
	seen, stop := captureEvents(c)
	if _, err := c.execMemoryWrite(context.Background(), nil, map[string]any{"scope": "session", "file_path": "MEMORY.md", "content": "opaque-child-34\n"}); err != nil {
		t.Fatalf("copy failure blocked session memory: %v", err)
	}
	stop()
	if !hasWarningContaining(*seen, "could not copy the parent session's memory") {
		t.Fatal("copy failure was not reported")
	}
}
