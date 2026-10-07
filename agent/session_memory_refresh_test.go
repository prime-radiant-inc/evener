package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// The memory refresh delivers a scope's full index only when the session has
// no baseline for it (start, resume, compaction) and never echoes the
// session's own index writes. These tests drive the real turn loop with a
// scripted provider and play other sessions by writing files under the
// memory root between model calls.

// memoryContextMessages counts the memory-context messages a request carries.
func memoryContextMessages(req llm.Request) int {
	n := 0
	for _, msg := range req.Messages {
		if strings.HasPrefix(msg.Name, "memory_") {
			n++
		}
	}
	return n
}

func writeMemoryIndex(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Another session's index write during a turn reaches no later model call of
// that turn.
func TestMemoryRefreshSkipsLaterRoundsOfATurn(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := memorySeed(t, root, "personal", "opaque-round-1\n")
	var first int
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root}), withSteps(
		func(req llm.Request) llm.Response {
			first = memoryContextMessages(req)
			writeMemoryIndex(t, path, "opaque-round-1\nopaque-round-2\n")
			return memoryCallResponse("memory_search", map[string]any{"scope": "personal", "pattern": "opaque-absent"})
		},
		func(req llm.Request) llm.Response {
			if got := memoryContextMessages(req); got != first {
				t.Fatalf("second round carries %d memory contexts, first carried %d", got, first)
			}
			return finalResponse("done")
		},
	))
	if _, err := s.ProcessInput(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if first != 1 || memoryContextCount(s) != 1 {
		t.Fatalf("first round contexts=%d, history contexts=%d, want 1 and 1", first, memoryContextCount(s))
	}
}

// The session's own memory_edit of MEMORY.md becomes its baseline: neither the
// rest of the turn nor the next turn carries a refresh of it.
func TestMemoryRefreshIgnoresOwnIndexEdit(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := memorySeed(t, root, "personal", "opaque-own-1\n")
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root}), withSteps(
		func(llm.Request) llm.Response {
			return memoryCallResponse("memory_read", map[string]any{"scope": "personal", "file_path": "MEMORY.md"})
		},
		func(llm.Request) llm.Response {
			return memoryCallResponse("memory_edit", map[string]any{"scope": "personal", "file_path": "MEMORY.md", "old_string": "opaque-own-1", "new_string": "opaque-own-2"})
		},
		func(req llm.Request) llm.Response {
			if got := memoryContextMessages(req); got != 1 {
				t.Fatalf("round after the edit carries %d memory contexts, want 1", got)
			}
			return finalResponse("edited")
		},
		func(req llm.Request) llm.Response {
			if got := memoryContextMessages(req); got != 1 {
				t.Fatalf("next turn carries %d memory contexts, want 1", got)
			}
			return finalResponse("next")
		},
	))
	if _, err := s.ProcessInput(context.Background(), "edit", nil); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "opaque-own-2\n" {
		t.Fatalf("index=%q err=%v, want the edit applied", got, err)
	}
	if _, err := s.ProcessInput(context.Background(), "next", nil); err != nil {
		t.Fatal(err)
	}
	if got := memoryContextCount(s); got != 1 {
		t.Fatalf("history contexts=%d, want 1", got)
	}
}

// Compaction and resume each deliver the current full index once, and the
// unchanged turn after each delivers nothing.
func TestMemoryRefreshFullIndexAfterCompactionAndResume(t *testing.T) {
	t.Parallel()
	root, history, workspace := t.TempDir(), t.TempDir(), t.TempDir()
	path := memorySeed(t, root, "personal", "opaque-full-1\n")
	s := newScriptedSummaryCompactSession(t, "memory-refresh-summary", func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("opaque-refresh-fold")}
	}, withDir(workspace), withConfig(SessionConfig{StateDir: history, MemoryStateRoot: root}))
	wantFull := func(body string) func(llm.Request) llm.Response {
		return func(req llm.Request) llm.Response {
			state, got, _ := memoryRequestIndex(t, req, "personal")
			if state != "current" || got != body {
				t.Fatalf("personal index state=%s body=%q, want current %q", state, got, body)
			}
			return finalResponse("observed")
		}
	}
	s.client.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		wantFull("opaque-full-1\n"), wantFull("opaque-full-2\n"), wantFull("opaque-full-2\n"),
	}})
	turn := func(sess *Session, wantContexts int) {
		t.Helper()
		if _, err := sess.ProcessInput(context.Background(), "go", nil); err != nil {
			t.Fatal(err)
		}
		if got := memoryContextCount(sess); got != wantContexts {
			t.Fatalf("history contexts=%d, want %d", got, wantContexts)
		}
	}
	turn(s, 1)
	writeMemoryIndex(t, path, "opaque-full-2\n")
	for range 12 {
		s.appendTurnWithTranscriptMessage(schema.TurnUserInput, llm.User("opaque-old"), llm.User("opaque-old"))
	}
	if err := s.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := memoryContextCount(s); got != 0 {
		t.Fatalf("compaction kept %d memory contexts", got)
	}
	turn(s, 1)
	turn(s, 1)
	s.Close()

	meta, err := schema.LoadSessionMeta(history, s.id)
	if err != nil {
		t.Fatal(err)
	}
	r, err := RestoreSessionFromMetaWithConfig(s.client, s.profile, execenv.NewLocalExecutionEnvironment(workspace), meta, RestoreSessionConfig{StateDir: history, MemoryStateRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	r.client.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		wantFull("opaque-full-2\n"), wantFull("opaque-full-2\n"),
	}})
	restored := memoryContextCount(r)
	turn(r, restored+1)
	turn(r, restored+1)
}

// A first empty index projects nothing and leaves the session without a
// baseline, so content another session adds later arrives as the full index
// at the next turn.
func TestMemoryRefreshEmptyIndexThenContentDeliversFullIndex(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := memorySeed(t, root, "personal", "")
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root}), withSteps(
		func(req llm.Request) llm.Response {
			if got := memoryContextMessages(req); got != 0 {
				t.Fatalf("empty index produced %d memory contexts, want 0", got)
			}
			return finalResponse("first")
		},
		func(req llm.Request) llm.Response {
			state, body, _ := memoryRequestIndex(t, req, "personal")
			if state != "current" || body != "opaque-later-1\n" {
				t.Fatalf("personal index state=%s body=%q, want current %q", state, body, "opaque-later-1\n")
			}
			return finalResponse("second")
		},
	))
	if _, err := s.ProcessInput(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	writeMemoryIndex(t, path, "opaque-later-1\n")
	if _, err := s.ProcessInput(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
}

// With no index at start, the session's own memory_write of MEMORY.md becomes
// its baseline: neither the next round nor the next turn echoes it back.
func TestMemoryRefreshIgnoresOwnIndexWriteWithoutIndex(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root}), withSteps(
		func(llm.Request) llm.Response {
			return memoryCallResponse("memory_write", map[string]any{"scope": "personal", "file_path": "MEMORY.md", "content": "opaque-own-index-1\n"})
		},
		func(req llm.Request) llm.Response {
			if got := memoryContextMessages(req); got != 0 {
				t.Fatalf("round after the write carries %d memory contexts, want 0", got)
			}
			return finalResponse("written")
		},
		func(req llm.Request) llm.Response {
			if got := memoryContextMessages(req); got != 0 {
				t.Fatalf("next turn carries %d memory contexts, want 0", got)
			}
			return finalResponse("next")
		},
	))
	if _, err := s.ProcessInput(context.Background(), "write", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ProcessInput(context.Background(), "next", nil); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(root, "memory", "personal", "MEMORY.md")); err != nil || string(got) != "opaque-own-index-1\n" {
		t.Fatalf("index=%q err=%v, want the write applied", got, err)
	}
}

// The session's own memory_delete of its index is not echoed back as a
// missing index at the next turn.
func TestMemoryRefreshIgnoresOwnIndexDelete(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := memorySeed(t, root, "personal", "opaque-doomed-1\n")
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root}), withSteps(
		func(llm.Request) llm.Response {
			return memoryCallResponse("memory_read", map[string]any{"scope": "personal", "file_path": "MEMORY.md"})
		},
		func(llm.Request) llm.Response {
			return memoryCallResponse("memory_delete", map[string]any{"scope": "personal", "file_path": "MEMORY.md"})
		},
		func(llm.Request) llm.Response { return finalResponse("deleted") },
		func(req llm.Request) llm.Response {
			if got := memoryContextMessages(req); got != 1 {
				t.Fatalf("next turn carries %d memory contexts, want only the first full index", got)
			}
			return finalResponse("next")
		},
	))
	if _, err := s.ProcessInput(context.Background(), "delete", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("index still present after delete: %v", err)
	}
	if _, err := s.ProcessInput(context.Background(), "next", nil); err != nil {
		t.Fatal(err)
	}
}

// An index that is empty when compaction re-delivers it becomes no baseline,
// so content another session adds afterwards arrives as the full index at the
// next turn.
func TestMemoryRefreshEmptyIndexAfterCompactionThenContentDeliversFullIndex(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := memorySeed(t, root, "personal", "opaque-before-fold-1\n")
	s := newScriptedSummaryCompactSession(t, "memory-refresh-empty-summary", func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("opaque-empty-fold")}
	}, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root}))
	wantFull := func(body string) func(llm.Request) llm.Response {
		return func(req llm.Request) llm.Response {
			state, got, _ := memoryRequestIndex(t, req, "personal")
			if state != "current" || got != body {
				t.Fatalf("personal index state=%s body=%q, want current %q", state, got, body)
			}
			return finalResponse("observed")
		}
	}
	s.client.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		wantFull("opaque-before-fold-1\n"), wantFull(""), wantFull("opaque-after-fold-2\n"),
	}})
	turn := func() {
		t.Helper()
		if _, err := s.ProcessInput(context.Background(), "go", nil); err != nil {
			t.Fatal(err)
		}
	}
	turn()
	writeMemoryIndex(t, path, "")
	for range 12 {
		s.appendTurnWithTranscriptMessage(schema.TurnUserInput, llm.User("opaque-old"), llm.User("opaque-old"))
	}
	if err := s.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	turn()
	writeMemoryIndex(t, path, "opaque-after-fold-2\n")
	turn()
}

// The baseline holds the index as projected, cut at the projection's cap,
// never the whole file.
func TestMemoryRefreshBaselineIsBoundedByTheProjectionCap(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	memorySeed(t, root, "personal", strings.Repeat("opaque-long-line\n", 2000))
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root}), withSteps(
		func(llm.Request) llm.Response { return finalResponse("observed") },
	))
	if _, err := s.ProcessInput(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	baseline, known := s.memoryBaselineFor("personal")
	if !known || len(baseline.index) > 8192 {
		t.Fatalf("baseline known=%t holds %d bytes, want at most the 8192-byte projection cap", known, len(baseline.index))
	}
}

// stalledMemoryRefresh drives refresh boundaries directly against a fake
// clock, with a latch that can hold a scope's index read before its I/O.
type stalledMemoryRefresh struct {
	s       *Session
	clk     *agenttest.FakeClock
	stall   atomic.Bool
	started chan string
	release chan struct{}
}

func newStalledMemoryRefresh(t *testing.T, root string) *stalledMemoryRefresh {
	t.Helper()
	r := &stalledMemoryRefresh{clk: agenttest.NewFakeClock(), started: make(chan string, 1), release: make(chan struct{})}
	t.Cleanup(func() {
		select {
		case <-r.release:
		default:
			close(r.release)
		}
	})
	r.s = newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root, clock: r.clk, testOnly: testConfig{memoryBeforeIO: func(scope, op string) error {
		if op == "index_read" && scope == "personal" && r.stall.Load() {
			r.started <- scope
			<-r.release
		}
		return nil
	}}}))
	return r
}

// boundary runs one turn-start refresh to completion.
func (r *stalledMemoryRefresh) boundary() {
	r.s.maybeAppendMemoryContext(context.Background(), true)
}

// stalledBoundary runs a turn-start refresh whose personal read stalls past
// the boundary budget, and returns that read's flight.
func (r *stalledMemoryRefresh) stalledBoundary(t *testing.T) *memoryIndexFlight {
	t.Helper()
	r.stall.Store(true)
	done := make(chan struct{})
	go func() { r.boundary(); close(done) }()
	<-r.started
	r.clk.Advance(250 * time.Millisecond)
	<-done
	r.s.memoryMu.Lock()
	defer r.s.memoryMu.Unlock()
	return r.s.memoryIndexFlights["personal"]
}

// finish lets the stalled read complete and waits for it.
func (r *stalledMemoryRefresh) finish(flight *memoryIndexFlight) {
	r.stall.Store(false)
	close(r.release)
	if flight != nil {
		<-flight.done
	}
}

// A turn-start read that outlives the boundary budget is no observation for
// a scope the session already knows: nothing is appended, the baseline stays,
// and the next boundary over the unchanged index appends nothing.
func TestMemoryRefreshStalledReadOfKnownScopeAppendsNothing(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	memorySeed(t, root, "personal", "opaque-stall-1\n")
	r := newStalledMemoryRefresh(t, root)
	r.boundary()
	if got := memoryContextCount(r.s); got != 1 {
		t.Fatalf("first boundary contexts=%d, want the full index", got)
	}
	flight := r.stalledBoundary(t)
	if got := memoryContextCount(r.s); got != 1 {
		t.Fatalf("stalled boundary appended %d contexts, want none", got-1)
	}
	if _, known := r.s.memoryBaselineFor("personal"); !known {
		t.Fatal("stalled boundary dropped the baseline")
	}
	r.finish(flight)
	r.boundary()
	if got := memoryContextCount(r.s); got != 1 {
		t.Fatalf("boundary after the stall appended %d contexts, want none", got-1)
	}
}

// The session's own index write while its turn-start read is still in flight
// is not echoed back by any later boundary.
func TestMemoryRefreshOwnWriteDuringStalledReadIsNotEchoed(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	memorySeed(t, root, "personal", "opaque-before-write-1\n")
	r := newStalledMemoryRefresh(t, root)
	r.boundary()
	flight := r.stalledBoundary(t)
	if res := memoryExec(t, r.s, "memory_write", map[string]any{"scope": "personal", "file_path": "MEMORY.md", "content": "opaque-own-write-2\n"}); res.IsError {
		t.Fatal(res.Output)
	}
	// The next boundary meets the read the write made stale and lets it
	// finish inside its budget; a later boundary reads afresh.
	done := make(chan struct{})
	go func() { r.boundary(); close(done) }()
	r.finish(flight)
	<-done
	r.boundary()
	if got := memoryContextCount(r.s); got != 1 {
		t.Fatalf("own write during a stalled read produced %d more contexts, want none", got-1)
	}
}
