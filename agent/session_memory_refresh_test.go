package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/internal/apptranscript"
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
	// reads counts the personal index reads begun.
	reads atomic.Int32
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
		if op == "index_read" && scope == "personal" {
			r.reads.Add(1)
			if r.stall.Load() {
				r.started <- scope
				<-r.release
			}
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

// latestMemoryContext returns the newest memory-context message for scope in
// the request, or "" when there is none.
func latestMemoryContext(req llm.Request, scope string) string {
	latest := ""
	for _, msg := range req.Messages {
		if msg.Name == "memory_"+scope {
			latest = msg.Text()
		}
	}
	return latest
}

// Another session's index write reaches the next turn as the changed lines
// only, never the full index again; the unchanged turn after it carries
// nothing new.
func TestMemoryRefreshDeliversIndexDeltaOncePerTurn(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := memorySeed(t, root, "personal", "opaque-kept-1\nopaque-dropped-2\n")
	var delta string
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root}), withSteps(
		func(req llm.Request) llm.Response {
			if got := memoryContextMessages(req); got != 1 {
				t.Fatalf("first turn carries %d memory contexts, want the full index", got)
			}
			return finalResponse("first")
		},
		func(req llm.Request) llm.Response {
			if got := memoryContextMessages(req); got != 2 {
				t.Fatalf("turn after the change carries %d memory contexts, want 2", got)
			}
			delta = latestMemoryContext(req, "personal")
			return finalResponse("second")
		},
		func(req llm.Request) llm.Response {
			if got := memoryContextMessages(req); got != 2 {
				t.Fatalf("unchanged turn carries %d memory contexts, want 2", got)
			}
			return finalResponse("third")
		},
	))
	turn := func() {
		t.Helper()
		if _, err := s.ProcessInput(context.Background(), "go", nil); err != nil {
			t.Fatal(err)
		}
	}
	turn()
	writeMemoryIndex(t, path, "opaque-kept-1\nopaque-added-3\n")
	turn()
	if !strings.Contains(delta, "opaque-added-3") || !strings.Contains(delta, "opaque-dropped-2") {
		t.Fatalf("delta lacks the added or removed line: %q", delta)
	}
	if strings.Contains(delta, "opaque-kept-1") {
		t.Fatalf("delta repeats the unchanged line: %q", delta)
	}
	turn()
	if got := memoryContextCount(s); got != 2 {
		t.Fatalf("history contexts=%d, want 2", got)
	}
}

// A change too large to list is summarized: the delta stays near its cap and
// carries none of the changed lines.
func TestMemoryRefreshCapsLargeIndexDelta(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := memorySeed(t, root, "personal", "opaque-small-1\n")
	var delta string
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root}), withSteps(
		func(llm.Request) llm.Response { return finalResponse("first") },
		func(req llm.Request) llm.Response {
			if got := memoryContextMessages(req); got != 2 {
				t.Fatalf("turn after the change carries %d memory contexts, want 2", got)
			}
			delta = latestMemoryContext(req, "personal")
			return finalResponse("second")
		},
	))
	if _, err := s.ProcessInput(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	var big strings.Builder
	for i := range 200 {
		fmt.Fprintf(&big, "opaque-bulk-line-%03d-%s\n", i, strings.Repeat("x", 20))
	}
	writeMemoryIndex(t, path, big.String())
	if _, err := s.ProcessInput(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if delta == "" || len(delta) > memoryIndexDeltaCap || strings.Contains(delta, "opaque-bulk-line-") {
		t.Fatalf("large delta len=%d lists its lines or is missing: %q", len(delta), delta)
	}
}

// Change blocks compare the index as projected, cut at the 8 KiB cap: a
// change past the cap, which the model never saw, delivers nothing, while a
// change inside it delivers a block.
func TestMemoryRefreshIndexDeltaComparesTheProjectedIndex(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	var filler strings.Builder
	for i := range 600 {
		fmt.Fprintf(&filler, "opaque-filler-line-%03d\n", i)
	}
	head := "opaque-head-1\n" + filler.String()
	path := memorySeed(t, root, "personal", head)
	var delta string
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root}), withSteps(
		func(req llm.Request) llm.Response {
			if got := memoryContextMessages(req); got != 1 {
				t.Fatalf("first turn carries %d memory contexts, want the full index", got)
			}
			return finalResponse("first")
		},
		func(req llm.Request) llm.Response {
			if got := memoryContextMessages(req); got != 1 {
				t.Fatalf("a change past the cap produced %d memory contexts, want 1", got)
			}
			return finalResponse("second")
		},
		func(req llm.Request) llm.Response {
			if got := memoryContextMessages(req); got != 2 {
				t.Fatalf("a change inside the cap produced %d memory contexts, want 2", got)
			}
			delta = latestMemoryContext(req, "personal")
			return finalResponse("third")
		},
	))
	turn := func() {
		t.Helper()
		if _, err := s.ProcessInput(context.Background(), "go", nil); err != nil {
			t.Fatal(err)
		}
	}
	turn()
	writeMemoryIndex(t, path, head+"opaque-past-the-cap\n")
	turn()
	writeMemoryIndex(t, path, strings.Replace(head, "opaque-head-1", "opaque-head-2", 1)+"opaque-past-the-cap\n")
	turn()
	if !strings.Contains(delta, "opaque-head-2") || strings.Contains(delta, "opaque-past-the-cap") {
		t.Fatalf("delta should carry only the change inside the cap: %q", delta)
	}
}

// A stalled read of a known index delivers no change block; the next
// completed read delivers the other session's change once.
func TestMemoryRefreshStalledReadDefersIndexDelta(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := memorySeed(t, root, "personal", "opaque-defer-1\n")
	r := newStalledMemoryRefresh(t, root)
	r.boundary()
	writeMemoryIndex(t, path, "opaque-defer-1\nopaque-defer-2\n")
	flight := r.stalledBoundary(t)
	if got := memoryContextCount(r.s); got != 1 {
		t.Fatalf("stalled boundary appended %d contexts, want none", got-1)
	}
	r.finish(flight)
	r.boundary()
	if got := memoryContextCount(r.s); got != 2 {
		t.Fatalf("completed boundary appended %d contexts, want one change block", got-1)
	}
	r.boundary()
	if got := memoryContextCount(r.s); got != 2 {
		t.Fatalf("unchanged boundary appended %d contexts, want none", got-2)
	}
}

func writeMemoryPage(t *testing.T, root, name, body string) string {
	t.Helper()
	path := filepath.Join(root, "memory", "personal", name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A page the session read and another session then changed or removed gets a
// notice at the next turn, naming the page but never carrying its contents. A
// page the session never read is never mentioned, and an unchanged turn after
// a notice carries nothing.
func TestMemoryRefreshNoticesChangedReadPages(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	memorySeed(t, root, "personal", "opaque-index-1\n")
	page := writeMemoryPage(t, root, "opaque-read-page.md", "opaque-page-body-1\n")
	writeMemoryPage(t, root, "opaque-unread-page.md", "opaque-unread-body-1\n")
	var notices []string
	noticeTurn := func(want int) func(llm.Request) llm.Response {
		return func(req llm.Request) llm.Response {
			if got := memoryContextMessages(req); got != want {
				t.Fatalf("turn carries %d memory contexts, want %d", got, want)
			}
			notices = append(notices, latestMemoryContext(req, "personal"))
			return finalResponse("observed")
		}
	}
	changedTurn, removedTurn, unchangedTurn := noticeTurn(2), noticeTurn(3), noticeTurn(3)
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root}), withSteps(
		func(llm.Request) llm.Response {
			return memoryCallResponse("memory_read", map[string]any{"scope": "personal", "file_path": "opaque-read-page.md"})
		},
		func(llm.Request) llm.Response { return finalResponse("read") },
		changedTurn, removedTurn, unchangedTurn,
	))
	turn := func() {
		t.Helper()
		if _, err := s.ProcessInput(context.Background(), "go", nil); err != nil {
			t.Fatal(err)
		}
	}
	turn()
	writeMemoryPage(t, root, "opaque-read-page.md", "opaque-page-body-2\n")
	writeMemoryPage(t, root, "opaque-unread-page.md", "opaque-unread-body-2\n")
	turn()
	if err := os.Remove(page); err != nil {
		t.Fatal(err)
	}
	turn()
	turn()
	for i, notice := range notices[:2] {
		if !strings.Contains(notice, "opaque-read-page.md") {
			t.Fatalf("notice %d does not name the read page: %q", i, notice)
		}
		if strings.Contains(notice, "opaque-unread-page.md") || strings.Contains(notice, "opaque-page-body") || strings.Contains(notice, "opaque-unread-body") {
			t.Fatalf("notice %d names an unread page or carries page contents: %q", i, notice)
		}
	}
}

// A page the session read and then edited itself is its own baseline: the
// next turn carries no notice for it.
func TestMemoryRefreshIgnoresOwnPageEdit(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	memorySeed(t, root, "personal", "opaque-index-1\n")
	page := writeMemoryPage(t, root, "opaque-own-page.md", "opaque-own-body-1\n")
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root}), withSteps(
		func(llm.Request) llm.Response {
			return memoryCallResponse("memory_read", map[string]any{"scope": "personal", "file_path": "opaque-own-page.md"})
		},
		func(llm.Request) llm.Response {
			return memoryCallResponse("memory_edit", map[string]any{"scope": "personal", "file_path": "opaque-own-page.md", "old_string": "opaque-own-body-1", "new_string": "opaque-own-body-2"})
		},
		func(llm.Request) llm.Response { return finalResponse("edited") },
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
	if got, err := os.ReadFile(page); err != nil || string(got) != "opaque-own-body-2\n" {
		t.Fatalf("page=%q err=%v, want the edit applied", got, err)
	}
	if _, err := s.ProcessInput(context.Background(), "next", nil); err != nil {
		t.Fatal(err)
	}
}

// A stalled read delivers no page notice; the next completed read notices a
// changed page the session read, once.
func TestMemoryRefreshStalledReadDefersPageNotice(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	memorySeed(t, root, "personal", "opaque-index-1\n")
	writeMemoryPage(t, root, "opaque-deferred-page.md", "opaque-page-body-1\n")
	r := newStalledMemoryRefresh(t, root)
	r.boundary()
	if res := memoryExec(t, r.s, "memory_read", map[string]any{"scope": "personal", "file_path": "opaque-deferred-page.md"}); res.IsError {
		t.Fatal(res.Output)
	}
	writeMemoryPage(t, root, "opaque-deferred-page.md", "opaque-page-body-2\n")
	flight := r.stalledBoundary(t)
	if got := memoryContextCount(r.s); got != 1 {
		t.Fatalf("stalled boundary appended %d contexts, want none", got-1)
	}
	r.finish(flight)
	r.boundary()
	if got := memoryContextCount(r.s); got != 2 {
		t.Fatalf("completed boundary appended %d contexts, want one page notice", got-1)
	}
	r.boundary()
	if got := memoryContextCount(r.s); got != 2 {
		t.Fatalf("unchanged boundary appended %d contexts, want none", got-2)
	}
}

// lastMemoryContextText returns the newest memory-context body in history.
func lastMemoryContextText(s *Session) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, turn := range slices.Backward(s.history) {
		if turn.Kind == schema.TurnMemoryContext {
			return turn.Message.Text()
		}
	}
	return ""
}

// A read that missed one boundary's budget and completed before the next is
// still a genuine read: the next boundary publishes it without reading again.
func TestMemoryRefreshPublishesALateReadAtTheNextBoundary(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	memorySeed(t, root, "personal", "opaque-index-1\n")
	writeMemoryPage(t, root, "opaque-late-page.md", "opaque-late-body-1\n")
	r := newStalledMemoryRefresh(t, root)
	r.boundary()
	if res := memoryExec(t, r.s, "memory_read", map[string]any{"scope": "personal", "file_path": "opaque-late-page.md"}); res.IsError {
		t.Fatal(res.Output)
	}
	writeMemoryPage(t, root, "opaque-late-page.md", "opaque-late-body-2\n")
	flight := r.stalledBoundary(t)
	r.finish(flight)
	readsBefore := r.reads.Load()
	r.boundary()
	if got := r.reads.Load() - readsBefore; got != 0 {
		t.Fatalf("next boundary began %d new reads, want it to use the completed late read", got)
	}
	if got := memoryContextCount(r.s); got != 2 || !strings.Contains(lastMemoryContextText(r.s), "opaque-late-page.md") {
		t.Fatalf("contexts=%d last=%q, want the late read's page notice", got, lastMemoryContextText(r.s))
	}
}

// A read the session's own write made stale is discarded even once it has
// completed: the next boundary reads again and echoes nothing.
func TestMemoryRefreshDiscardsAStaleReadEvenWhenComplete(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	memorySeed(t, root, "personal", "opaque-stale-1\n")
	r := newStalledMemoryRefresh(t, root)
	r.boundary()
	flight := r.stalledBoundary(t)
	if res := memoryExec(t, r.s, "memory_write", map[string]any{"scope": "personal", "file_path": "MEMORY.md", "content": "opaque-stale-own-2\n"}); res.IsError {
		t.Fatal(res.Output)
	}
	r.finish(flight)
	readsBefore := r.reads.Load()
	r.boundary()
	if got := r.reads.Load() - readsBefore; got != 1 {
		t.Fatalf("next boundary began %d reads, want one fresh read in place of the stale one", got)
	}
	if got := memoryContextCount(r.s); got != 1 {
		t.Fatalf("stale read produced %d more contexts, want none", got-1)
	}
}

// The session tracks at most memoryReadPageLimit pages per scope, dropping the
// least recently read: a page pushed out gets no notice, a page still tracked
// does.
func TestMemoryRefreshTracksAtMostTheMostRecentlyReadPages(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	memorySeed(t, root, "personal", "opaque-index-1\n")
	r := newStalledMemoryRefresh(t, root)
	r.boundary()
	pages := memoryReadPageLimit + 1
	for i := range pages {
		name := fmt.Sprintf("opaque-capped-%02d.md", i)
		writeMemoryPage(t, root, name, "opaque-capped-body-1\n")
		if res := memoryExec(t, r.s, "memory_read", map[string]any{"scope": "personal", "file_path": name}); res.IsError {
			t.Fatal(res.Output)
		}
	}
	if got := len(r.s.memoryReadPagesFor("personal")); got != memoryReadPageLimit {
		t.Fatalf("tracked %d pages, want %d", got, memoryReadPageLimit)
	}
	writeMemoryPage(t, root, "opaque-capped-00.md", "opaque-capped-body-2\n")
	last := fmt.Sprintf("opaque-capped-%02d.md", pages-1)
	writeMemoryPage(t, root, last, "opaque-capped-body-2\n")
	r.boundary()
	notice := lastMemoryContextText(r.s)
	if !strings.Contains(notice, last) || strings.Contains(notice, "opaque-capped-00.md") {
		t.Fatalf("notice should name only the tracked page: %q", notice)
	}
}

// Page records survive compaction: a page read before it still gets a notice
// when another session changes it afterwards.
func TestMemoryRefreshPageRecordsSurviveCompaction(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	memorySeed(t, root, "personal", "opaque-index-1\n")
	writeMemoryPage(t, root, "opaque-kept-page.md", "opaque-kept-body-1\n")
	s := newScriptedSummaryCompactSession(t, "memory-page-summary", func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("opaque-page-fold")}
	}, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root}))
	var notice string
	s.client.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			return memoryCallResponse("memory_read", map[string]any{"scope": "personal", "file_path": "opaque-kept-page.md"})
		},
		func(llm.Request) llm.Response { return finalResponse("read") },
		func(llm.Request) llm.Response { return finalResponse("after fold") },
		func(req llm.Request) llm.Response {
			notice = latestMemoryContext(req, "personal")
			return finalResponse("noticed")
		},
	}})
	turn := func() {
		t.Helper()
		if _, err := s.ProcessInput(context.Background(), "go", nil); err != nil {
			t.Fatal(err)
		}
	}
	turn()
	for range 12 {
		s.appendTurnWithTranscriptMessage(schema.TurnUserInput, llm.User("opaque-old"), llm.User("opaque-old"))
	}
	if err := s.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	turn()
	writeMemoryPage(t, root, "opaque-kept-page.md", "opaque-kept-body-2\n")
	turn()
	if !strings.Contains(notice, "opaque-kept-page.md") {
		t.Fatalf("no notice for a page read before compaction: %q", notice)
	}
}

// A resumed session starts with no page records: a page read only before the
// resume gets no notice after it.
func TestMemoryRefreshPageRecordsDoNotSurviveResume(t *testing.T) {
	t.Parallel()
	root, history, workspace := t.TempDir(), t.TempDir(), t.TempDir()
	memorySeed(t, root, "personal", "opaque-index-1\n")
	writeMemoryPage(t, root, "opaque-forgotten-page.md", "opaque-forgotten-body-1\n")
	s := newSession(t, withDir(workspace), withConfig(SessionConfig{StateDir: history, MemoryStateRoot: root}), withSteps(
		func(llm.Request) llm.Response {
			return memoryCallResponse("memory_read", map[string]any{"scope": "personal", "file_path": "opaque-forgotten-page.md"})
		},
		func(llm.Request) llm.Response { return finalResponse("read") },
	))
	if _, err := s.ProcessInput(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
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
	writeMemoryPage(t, root, "opaque-forgotten-page.md", "opaque-forgotten-body-2\n")
	var texts []string
	r.client.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			for _, msg := range req.Messages {
				if strings.HasPrefix(msg.Name, "memory_") {
					texts = append(texts, msg.Text())
				}
			}
			return finalResponse("resumed")
		},
	}})
	if _, err := r.ProcessInput(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	for _, text := range texts {
		if strings.Contains(text, "opaque-forgotten-page.md") {
			t.Fatalf("resumed session noticed a page read only before the resume: %q", text)
		}
	}
}

// A page whose record read fails, before any page is tracked, is simply left
// untracked: the session neither panics nor starts tracking it.
func TestMemoryRefreshUnreadablePageStaysUntracked(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	memorySeed(t, root, "personal", "opaque-index-1\n")
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root}))
	s.recordMemoryContent("personal", "opaque-unreadable.md", nil, os.ErrPermission, true)
	if got := s.memoryReadPagesFor("personal"); len(got) != 0 {
		t.Fatalf("tracked %v, want nothing", got)
	}
}

// Once Close has begun, a page notice is not appended, like every other
// memory-context append.
func TestMemoryRefreshPageNoticeNotAppendedAfterClose(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	memorySeed(t, root, "personal", "opaque-index-1\n")
	writeMemoryPage(t, root, "opaque-closing-page.md", "opaque-closing-body-1\n")
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root}))
	if res := memoryExec(t, s, "memory_read", map[string]any{"scope": "personal", "file_path": "opaque-closing-page.md"}); res.IsError {
		t.Fatal(res.Output)
	}
	before := memoryContextCount(s)
	s.Close()
	s.publishMemoryPageChanges("personal", map[string]memoryPageRecord{"opaque-closing-page.md": {absent: true}})
	if got := memoryContextCount(s); got != before {
		t.Fatalf("closed session appended %d page notices", got-before)
	}
}

// A change another session makes right after memory_read returns is noticed
// at the next turn: the page's record is what the read itself saw, never a
// later re-read that could already hold the change.
func TestMemoryRefreshRecordsWhatMemoryReadSaw(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	memorySeed(t, root, "personal", "opaque-index-1\n")
	page := writeMemoryPage(t, root, "opaque-raced-page.md", "opaque-raced-body-1\n")
	var changed atomic.Bool
	change := func() {
		if changed.CompareAndSwap(false, true) {
			if err := os.WriteFile(page, []byte("opaque-raced-body-2\n"), 0o600); err != nil {
				t.Error(err)
			}
		}
	}
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root, testOnly: testConfig{memoryBeforeIO: func(scope, op string) error {
		// memory_read's seam between its read and the page's record: another
		// session's change lands here.
		if op == "record" {
			change()
		}
		return nil
	}}}))
	s.maybeAppendMemoryContext(context.Background(), true)
	if res := memoryExec(t, s, "memory_read", map[string]any{"scope": "personal", "file_path": "opaque-raced-page.md"}); res.IsError {
		t.Fatal(res.Output)
	}
	if !changed.Load() {
		t.Fatal("memory_read never reached its record seam")
	}
	before := memoryContextCount(s)
	s.maybeAppendMemoryContext(context.Background(), true)
	if got := memoryContextCount(s); got != before+1 || !strings.Contains(lastMemoryContextText(s), "opaque-raced-page.md") {
		t.Fatalf("contexts=%d last=%q, want a notice for the raced page", got-before, lastMemoryContextText(s))
	}
}

// A page notice is not an index projection: a resumed session whose only
// memory context for a scope was a page notice still suppresses its first
// missing index, as a fresh session does.
func TestMemoryRefreshResumeIgnoresPageNoticesWhenSeedingObservedScopes(t *testing.T) {
	t.Parallel()
	root, history, workspace := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "memory", "personal"), 0o700); err != nil {
		t.Fatal(err)
	}
	page := writeMemoryPage(t, root, "opaque-noticed-page.md", "opaque-noticed-body-1\n")
	s := newSession(t, withDir(workspace), withConfig(SessionConfig{StateDir: history, MemoryStateRoot: root}), withSteps(
		func(llm.Request) llm.Response {
			return memoryCallResponse("memory_read", map[string]any{"scope": "personal", "file_path": "opaque-noticed-page.md"})
		},
		func(llm.Request) llm.Response { return finalResponse("read") },
		func(req llm.Request) llm.Response {
			if !strings.Contains(latestMemoryContext(req, "personal"), "opaque-noticed-page.md") {
				t.Fatal("fixture produced no page notice")
			}
			return finalResponse("noticed")
		},
	))
	if _, err := s.ProcessInput(context.Background(), "read", nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(page, []byte("opaque-noticed-body-2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ProcessInput(context.Background(), "notice", nil); err != nil {
		t.Fatal(err)
	}
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
	before := memoryContextCount(r)
	r.maybeAppendMemoryContext(context.Background(), true)
	if got := memoryContextCount(r); got != before {
		t.Fatalf("resume projected %d memory contexts for a missing index never projected before, want none: %q", got-before, lastMemoryContextText(r))
	}
}

// A full index projection reports size only when the index was cut at the
// cap: the truncated one points at the gardening-memory skill and decodes as
// truncated; neither carries an explicit truncated flag any more.
func TestMemoryProjectionReportsOnlyATooLongIndex(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		index     string
		truncated bool
	}{
		{"short", "opaque-short-index\n", false},
		{"too-long", strings.Repeat("opaque-long-index-line\n", 600), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			memorySeed(t, root, "personal", tc.index)
			var text string
			s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root}), withSteps(func(req llm.Request) llm.Response {
				text = latestMemoryContext(req, "personal")
				return finalResponse("observed")
			}))
			if _, err := s.ProcessInput(context.Background(), "go", nil); err != nil {
				t.Fatal(err)
			}
			display, ok := apptranscript.ParseMemoryContext(text, "memory_personal")
			if !ok || display.Truncated != tc.truncated {
				t.Fatalf("decoded=%+v ok=%t, want truncated=%t", display, ok, tc.truncated)
			}
			if strings.Contains(text, ", truncated ") {
				t.Fatalf("projection still carries an explicit truncated flag: %q", text[:min(len(text), 240)])
			}
			if strings.Contains(text, "gardening-memory") != tc.truncated {
				t.Fatalf("gardening-memory pointer present=%t, want %t: %q", !tc.truncated, tc.truncated, text[:min(len(text), 240)])
			}
		})
	}
}
