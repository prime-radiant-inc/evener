package agent

import (
	"context"
	"fmt"
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
	return forkWithSessionMemoryMeta(t, seed, nil)
}

// forkWithSessionMemoryMeta is forkWithSessionMemory with a hook that edits
// the fork's persisted meta before it is restored.
func forkWithSessionMemoryMeta(t *testing.T, seed bool, edit func(*schema.SessionMeta)) (root, parentID string, child *Session) {
	t.Helper()
	root, parentID, restore := forkWithSessionMemoryRestorer(t, seed, edit)
	return root, parentID, restore()
}

// forkWithSessionMemoryRestorer creates the fork and returns a function that
// restores it from its persisted meta, as a new process would; each call
// restores it again.
func forkWithSessionMemoryRestorer(t *testing.T, seed bool, edit func(*schema.SessionMeta)) (root, parentID string, restore func() *Session) {
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
	return root, p.id, func() *Session {
		t.Helper()
		meta, err := schema.LoadSessionMeta(history, childID)
		if err != nil {
			t.Fatal(err)
		}
		if edit != nil {
			edit(&meta)
		}
		c, err := RestoreSessionFromMetaWithConfig(p.client, p.profile, execenv.NewLocalExecutionEnvironment(workspace), meta, RestoreSessionConfig{StateDir: history, MemoryStateRoot: root})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(c.Close)
		return c
	}
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
	if res := memoryExec(t, c, "memory_read", map[string]any{"scope": "session", "file_path": "MEMORY.md"}); res.IsError {
		t.Fatal(res.Output)
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

// A delegate of the fork reads session memory before the fork's root opens
// it. The delegate copies nothing and creates nothing, so the root still gets
// its parent's memory on its own first access.
func TestMemoryForkSeedsAfterDelegateReadsFirst(t *testing.T) {
	t.Parallel()
	root, _, c := forkWithSessionMemory(t, true)
	d := newSession(t, withConfig(SessionConfig{MemoryStateRoot: root}))
	d.depth = 1
	d.delegateRootSessionID = c.id
	if res := memoryExec(t, d, "memory_read", map[string]any{"scope": "session", "file_path": "MEMORY.md"}); !res.IsError {
		t.Fatalf("delegate read before the seed=%+v", res)
	}
	if p := d.readMemoryIndex("session"); p.Status != "missing" {
		t.Fatalf("delegate index before the seed=%+v", p)
	}
	if res := memoryExec(t, c, "memory_read", map[string]any{"scope": "session", "file_path": "MEMORY.md"}); res.IsError || !strings.Contains(res.Output, "opaque-parent-31") {
		t.Fatalf("fork read after its delegate=%+v", res)
	}
	if res := memoryExec(t, d, "memory_read", map[string]any{"scope": "session", "file_path": "MEMORY.md"}); res.IsError || !strings.Contains(res.Output, "opaque-parent-31") {
		t.Fatalf("delegate read after the seed=%+v", res)
	}
}

// The copy creates its temporary root and nested directories itself, and a
// directory with several entries is copied whole.
func TestMemoryForkCopiesNestedParentSessionMemory(t *testing.T) {
	t.Parallel()
	root, parentID, c := forkWithSessionMemory(t, true)
	parentDir := filepath.Join(root, "memory", "sessions", parentID)
	want := map[string]string{
		"MEMORY.md":                        "opaque-parent-31\n",
		"notes.md":                         "opaque-parent-notes-46\n",
		filepath.Join("topics", "page.md"): "opaque-parent-page-47\n",
	}
	if err := os.MkdirAll(filepath.Join(parentDir, "topics"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range want {
		if err := os.WriteFile(filepath.Join(parentDir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	seen, stop := captureEvents(c)
	if res := memoryExec(t, c, "memory_read", map[string]any{"scope": "session", "file_path": "MEMORY.md"}); res.IsError {
		t.Fatal(res.Output)
	}
	stop()
	if hasWarningContaining(*seen, "could not copy the parent session's memory") {
		t.Fatal("a nested parent scope was reported as a copy failure")
	}
	for name, content := range want {
		got, err := os.ReadFile(filepath.Join(root, "memory", "sessions", c.id, name))
		if err != nil || string(got) != content {
			t.Fatalf("child %s=%q err=%v, want %q", name, got, err, content)
		}
	}
}

func TestMemoryForkWithoutParentMemoryStartsEmpty(t *testing.T) {
	t.Parallel()
	root, _, c := forkWithSessionMemory(t, false)
	seen, stop := captureEvents(c)
	if res := memoryExec(t, c, "memory_write", map[string]any{"scope": "session", "file_path": "MEMORY.md", "content": "opaque-child-33\n"}); res.IsError {
		t.Fatal(res.Output)
	}
	stop()
	if _, err := os.Stat(filepath.Join(root, "memory", "sessions", c.id, "MEMORY.md")); err != nil {
		t.Fatal(err)
	}
	if hasWarningContaining(*seen, "could not copy the parent session's memory") {
		t.Fatal("a missing parent scope was reported as a copy failure")
	}
}

// A fork copies its parent's session memory once, as of its first use. When
// the parent had none, the fork's scope still exists (empty) after that first
// use, so the parent's later notes never reach a fork resumed in a new process.
func TestMemoryForkCopiesOnceWhenParentWasEmpty(t *testing.T) {
	t.Parallel()
	root, parentID, restore := forkWithSessionMemoryRestorer(t, false, nil)
	c := restore()
	if res := memoryExec(t, c, "memory_read", map[string]any{"scope": "session", "file_path": "MEMORY.md"}); !res.IsError {
		t.Fatalf("empty fork read=%+v", res)
	}
	childDir := filepath.Join(root, "memory", "sessions", c.id)
	if info, err := os.Lstat(childDir); err != nil || !info.IsDir() {
		t.Fatalf("fork scope after its first use: info=%v err=%v", info, err)
	}
	c.Close()
	memorySeed(t, root, filepath.Join("sessions", parentID), "opaque-parent-later-62\n")
	resumed := restore()
	if res := memoryExec(t, resumed, "memory_read", map[string]any{"scope": "session", "file_path": "MEMORY.md"}); !res.IsError {
		t.Fatalf("resumed fork copied the parent's later notes: %+v", res)
	}
}

// A failed copy is reported once, at the fork's first use, not again by every
// process that resumes the fork.
func TestMemoryForkCopyFailureWarnsOnce(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permissions")
	}
	root, parentID, restore := forkWithSessionMemoryRestorer(t, true, nil)
	parentDir := filepath.Join(root, "memory", "sessions", parentID)
	if err := os.Chmod(parentDir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parentDir, 0o700) })
	for i, want := range []bool{true, false} {
		c := restore()
		seen, stop := captureEvents(c)
		memoryExec(t, c, "memory_read", map[string]any{"scope": "session", "file_path": "MEMORY.md"})
		stop()
		if got := hasWarningContaining(*seen, "could not copy the parent session's memory"); got != want {
			t.Fatalf("process %d warned=%t, want %t", i, got, want)
		}
		c.Close()
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
	seen, stop := captureEvents(c)
	c.seedForkedSessionMemory()
	stop()
	if got, err := os.ReadFile(filepath.Join(base, c.id, "MEMORY.md")); err == nil {
		t.Fatalf("child copied through the symlink: %q", got)
	}
	if !hasWarningContaining(*seen, "could not copy the parent session's memory") {
		t.Fatal("a symlinked parent scope was not reported")
	}
}

func TestMemoryForkWarnsOnParentScopeThatIsAFile(t *testing.T) {
	t.Parallel()
	root, parentID, c := forkWithSessionMemory(t, false)
	base := filepath.Join(root, "memory", "sessions")
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(base, parentID)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, parentID), []byte("opaque-file-43\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	seen, stop := captureEvents(c)
	c.seedForkedSessionMemory()
	stop()
	if _, err := os.Lstat(filepath.Join(base, c.id)); !os.IsNotExist(err) {
		t.Fatalf("child scope created from a file parent: %v", err)
	}
	if !hasWarningContaining(*seen, "could not copy the parent session's memory") {
		t.Fatal("a parent scope that is a file was not reported")
	}
}

// Only a readable child directory counts as an existing fork scope. A child
// path the probe cannot list is reported, and the parent is not copied.
func TestMemoryForkWarnsOnUnlistableChildScope(t *testing.T) {
	t.Parallel()
	root, _, c := forkWithSessionMemory(t, true)
	childPath := filepath.Join(root, "memory", "sessions", c.id)
	if err := os.WriteFile(childPath, []byte("opaque-child-file-48\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	seen, stop := captureEvents(c)
	c.seedForkedSessionMemory()
	stop()
	if !hasWarningContaining(*seen, "could not copy the parent session's memory") {
		t.Fatal("an unlistable child scope was not reported")
	}
	if got, err := os.ReadFile(childPath); err != nil || string(got) != "opaque-child-file-48\n" {
		t.Fatalf("child path changed: %q err=%v", got, err)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(root, "memory", "sessions", ".fork-copy-*")); len(leftovers) != 0 {
		t.Fatalf("copy ran despite the unlistable child: %v", leftovers)
	}
}

// A symlink at memory/sessions must not redirect the copy: the parent read
// and the child write both stay beneath the memory state root.
func TestMemoryForkRefusesSymlinkedSessionsDirectory(t *testing.T) {
	t.Parallel()
	root, parentID, c := forkWithSessionMemory(t, false)
	outside := filepath.Join(t.TempDir(), "sessions")
	if err := os.MkdirAll(filepath.Join(outside, parentID), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, parentID, "MEMORY.md"), []byte("opaque-outside-44\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(root, "memory", "sessions")
	if err := os.RemoveAll(base); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(base), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, base); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	seen, stop := captureEvents(c)
	c.seedForkedSessionMemory()
	stop()
	if _, err := os.Lstat(filepath.Join(outside, c.id)); !os.IsNotExist(err) {
		t.Fatalf("child received content through the symlinked sessions directory: %v", err)
	}
	if !hasWarningContaining(*seen, "could not copy the parent session's memory") {
		t.Fatal("a symlinked sessions directory was not reported")
	}
}

// The byte limit bounds what the copy reads, so a file that grows after it
// was listed still cannot exceed the remaining budget.
func TestMemoryForkReadWithinBudget(t *testing.T) {
	t.Parallel()
	if got, err := readWithinBudget(strings.NewReader("opaque"), 6); err != nil || string(got) != "opaque" {
		t.Fatalf("at budget got=%q err=%v", got, err)
	}
	if got, err := readWithinBudget(strings.NewReader("opaque!"), 6); err == nil || !strings.Contains(err.Error(), "fork copy limit") {
		t.Fatalf("over budget got=%q err=%v", got, err)
	}
}

// A delegate resumed on its own restores with depth zero, so only its
// persisted subagent flag marks it. It must not get a private session scope,
// session guidance or a fork seed.
func TestMemoryStandaloneResumedDelegateHasNoSessionScope(t *testing.T) {
	t.Parallel()
	root, _, c := forkWithSessionMemoryMeta(t, true, func(meta *schema.SessionMeta) { meta.IsSubagent = true })
	if c.depth != 0 {
		t.Fatalf("fixture depth=%d, want a standalone restore", c.depth)
	}
	res := memoryExec(t, c, "memory_write", map[string]any{"scope": "session", "file_path": "MEMORY.md", "content": "opaque-delegate-45\n"})
	if !res.IsError || !strings.Contains(res.Output, "session memory is not bound") {
		t.Fatalf("standalone delegate session write=%+v", res)
	}
	if _, err := os.Lstat(filepath.Join(root, "memory", "sessions", c.id)); !os.IsNotExist(err) {
		t.Fatalf("standalone delegate got a session scope or fork seed: %v", err)
	}
	if guidance := c.memoryGuidance(); strings.Contains(guidance, memorySessionScopeLine) || strings.Contains(guidance, memorySessionSaveTrigger) {
		t.Fatalf("standalone delegate guidance names session memory: %q", guidance)
	}
	if data, _ := c.buildPromptData(c.currentEnv()); data.SessionMemorySaves {
		t.Fatal("standalone delegate offered session saves")
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
	if res := memoryExec(t, c, "memory_write", map[string]any{"scope": "session", "file_path": "MEMORY.md", "content": "opaque-child-34\n"}); res.IsError {
		t.Fatalf("copy failure blocked session memory: %s", res.Output)
	}
	stop()
	if !hasWarningContaining(*seen, "could not copy the parent session's memory") {
		t.Fatal("copy failure was not reported")
	}
}

// assertForkCopyRefused opens session scope on the fork and checks the copy
// was reported as a failure, the scope is usable, and nothing from the parent
// directory reached it.
func assertForkCopyRefused(t *testing.T, root string, c *Session, leaked string) {
	t.Helper()
	seen, stop := captureEvents(c)
	if res := memoryExec(t, c, "memory_write", map[string]any{"scope": "session", "file_path": "MEMORY.md", "content": "opaque-child-36\n"}); res.IsError {
		t.Fatalf("refused copy blocked session memory: %s", res.Output)
	}
	stop()
	if !hasWarningContaining(*seen, "could not copy the parent session's memory") {
		t.Fatal("copy refusal was not reported")
	}
	childDir := filepath.Join(root, "memory", "sessions", c.id)
	leftovers, _ := filepath.Glob(filepath.Join(root, "memory", "sessions", ".fork-copy-*"))
	if len(leftovers) != 0 {
		t.Fatalf("refused copy left its temporary directory: %v", leftovers)
	}
	err := filepath.WalkDir(childDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if got, _ := os.ReadFile(path); strings.Contains(string(got), leaked) {
			t.Fatalf("child holds parent content at %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(childDir, "MEMORY.md")); string(got) != "opaque-child-36\n" {
		t.Fatalf("child MEMORY.md=%q", got)
	}
}

func TestMemoryForkRefusesSymlinkedFileInParentMemory(t *testing.T) {
	t.Parallel()
	root, parentID, c := forkWithSessionMemory(t, true)
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("opaque-secret-41"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(root, "memory", "sessions", parentID, "link.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	assertForkCopyRefused(t, root, c, "opaque-secret-41")
}

func TestMemoryForkRefusesSymlinkedDirectoryInParentMemory(t *testing.T) {
	t.Parallel()
	root, parentID, c := forkWithSessionMemory(t, true)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "page.md"), []byte("opaque-secret-42"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "memory", "sessions", parentID, "linked")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	assertForkCopyRefused(t, root, c, "opaque-secret-42")
}

func TestMemoryForkRefusesParentMemoryOverEntryLimit(t *testing.T) {
	t.Parallel()
	root, parentID, c := forkWithSessionMemory(t, true)
	parentDir := filepath.Join(root, "memory", "sessions", parentID)
	for i := range maxForkMemoryCopyEntries + 1 {
		if err := os.WriteFile(filepath.Join(parentDir, fmt.Sprintf("page-%d.md", i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	assertForkCopyRefused(t, root, c, "opaque-parent-31")
}

func TestMemoryForkRefusesParentMemoryOverByteLimit(t *testing.T) {
	t.Parallel()
	root, parentID, c := forkWithSessionMemory(t, true)
	big := make([]byte, maxForkMemoryCopyBytes+1)
	if err := os.WriteFile(filepath.Join(root, "memory", "sessions", parentID, "big.md"), big, 0o600); err != nil {
		t.Fatal(err)
	}
	assertForkCopyRefused(t, root, c, "opaque-parent-31")
}
