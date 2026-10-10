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

// The memory refresh delivers a scope's full generated index only when the
// session has no baseline for it (start, resume, compaction) and never echoes
// the session's own page writes. These tests drive the real turn loop with a
// scripted provider and play other sessions by writing pages under the
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

// Another session's page write during a turn reaches no later model call of
// that turn.
func TestMemoryRefreshSkipsLaterRoundsOfATurn(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	memorySeedPage(t, root, "personal", "a.md", "opaque-round-1")
	var first int
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root}), withSteps(
		func(req llm.Request) llm.Response {
			first = memoryContextMessages(req)
			memorySeedPage(t, root, "personal", "b.md", "opaque-round-2")
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

// The session's own memory_edit of a page's description becomes its baseline:
// neither the rest of the turn nor the next turn carries a refresh of it.
func TestMemoryRefreshIgnoresOwnPageDescriptionEdit(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := memorySeedPage(t, root, "personal", "fact.md", "opaque-own-1")
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root}), withSteps(
		func(llm.Request) llm.Response {
			return memoryCallResponse("memory_read", map[string]any{"scope": "personal", "file_path": "fact.md"})
		},
		func(llm.Request) llm.Response {
			return memoryCallResponse("memory_edit", map[string]any{"scope": "personal", "file_path": "fact.md", "old_string": "opaque-own-1", "new_string": "opaque-own-2"})
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
	if got, err := os.ReadFile(path); err != nil || !strings.Contains(string(got), "description: opaque-own-2\n") {
		t.Fatalf("page=%q err=%v, want the edit applied", got, err)
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
	memorySeedPage(t, root, "personal", "fact.md", "opaque-full-1")
	s := newScriptedSummaryCompactSession(t, "memory-refresh-summary", func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("## Progress\nopaque-refresh-fold")}
	}, withDir(workspace), withConfig(SessionConfig{StateDir: history, MemoryStateRoot: root}))
	wantFull := func(marker string) func(llm.Request) llm.Response {
		return func(req llm.Request) llm.Response {
			state, got, _ := memoryRequestIndex(t, req, "personal")
			if want := memoryExpectedIndex(t, root, "personal"); state != "current" || got != want || !strings.Contains(got, marker) {
				t.Fatalf("personal index state=%s body=%q, want current %q with %s", state, got, want, marker)
			}
			return finalResponse("observed")
		}
	}
	s.client.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		wantFull("opaque-full-1"), wantFull("opaque-full-2"), wantFull("opaque-full-2"),
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
	memorySeedPage(t, root, "personal", "fact.md", "opaque-full-2")
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
		wantFull("opaque-full-2"), wantFull("opaque-full-2"),
	}})
	restored := memoryContextCount(r)
	turn(r, restored+1)
	turn(r, restored+1)
}

// A first scope with no pages projects nothing and leaves the session without
// a baseline, so a page another session adds later arrives as the full index
// at the next turn.
func TestMemoryRefreshNoPagesThenAPageDeliversFullIndex(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "memory", "personal"), 0o700); err != nil {
		t.Fatal(err)
	}
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root}), withSteps(
		func(req llm.Request) llm.Response {
			if got := memoryContextMessages(req); got != 0 {
				t.Fatalf("a scope with no pages produced %d memory contexts, want 0", got)
			}
			return finalResponse("first")
		},
		func(req llm.Request) llm.Response {
			state, body, _ := memoryRequestIndex(t, req, "personal")
			if want := memoryExpectedIndex(t, root, "personal"); state != "current" || body != want || !strings.Contains(body, "opaque-later-1") {
				t.Fatalf("personal index state=%s body=%q, want current %q", state, body, want)
			}
			return finalResponse("second")
		},
	))
	if _, err := s.ProcessInput(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	memorySeedPage(t, root, "personal", "fact.md", "opaque-later-1")
	if _, err := s.ProcessInput(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
}

// With no pages at start, the session's own memory_write of a page becomes
// its baseline: neither the next round nor the next turn echoes it back.
func TestMemoryRefreshIgnoresOwnPageWriteWithoutPages(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	const page = "---\ndescription: opaque-own-page-1\n---\n"
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root}), withSteps(
		func(llm.Request) llm.Response {
			return memoryCallResponse("memory_write", map[string]any{"scope": "personal", "file_path": "fact.md", "content": page})
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
	if got, err := os.ReadFile(filepath.Join(root, "memory", "personal", "fact.md")); err != nil || string(got) != strings.TrimSuffix(page, "---\n")+memoryOwnStamps(s)+"---\n" {
		t.Fatalf("page=%q err=%v, want the write applied", got, err)
	}
}

// The session's own memory_delete of its only page is not echoed back as a
// missing index at the next turn.
func TestMemoryRefreshIgnoresOwnPageDelete(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := memorySeedPage(t, root, "personal", "fact.md", "opaque-doomed-1")
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root}), withSteps(
		func(llm.Request) llm.Response {
			return memoryCallResponse("memory_read", map[string]any{"scope": "personal", "file_path": "fact.md"})
		},
		func(llm.Request) llm.Response {
			return memoryCallResponse("memory_delete", map[string]any{"scope": "personal", "file_path": "fact.md"})
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
		t.Fatalf("page still present after delete: %v", err)
	}
	if _, err := s.ProcessInput(context.Background(), "next", nil); err != nil {
		t.Fatal(err)
	}
}

// A file whose name holds a newline, put there outside the tools, is one
// index line, and the session's own memory_delete of it is not echoed back
// at the next turn.
func TestMemoryRefreshIgnoresOwnDeleteOfAControlCharacterPath(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	memorySeedPage(t, root, "personal", "fact.md", "opaque-kept-1")
	path := memorySeedPage(t, root, "personal", "bad\nname.md", "opaque-doomed-2")
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root}), withSteps(
		func(req llm.Request) llm.Response {
			if _, index, _ := memoryRequestIndex(t, req, "personal"); strings.Count(index, "\n") != 2 {
				t.Fatalf("index %q is not one line per page", index)
			}
			return memoryCallResponse("memory_delete", map[string]any{"scope": "personal", "file_path": "bad\nname.md"})
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
		t.Fatalf("file still present after delete: %v", err)
	}
	if _, err := s.ProcessInput(context.Background(), "next", nil); err != nil {
		t.Fatal(err)
	}
}

// A scope whose pages are gone when compaction re-delivers it is projected
// as missing and becomes no baseline, so a page another session adds
// afterwards arrives as the full index at the next turn.
func TestMemoryRefreshNoPagesAfterCompactionThenPageDeliversFullIndex(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := memorySeedPage(t, root, "personal", "fact.md", "opaque-before-fold-1")
	s := newScriptedSummaryCompactSession(t, "memory-refresh-empty-summary", func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("## Progress\nopaque-empty-fold")}
	}, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root}))
	wantFull := func(wantState, marker string) func(llm.Request) llm.Response {
		return func(req llm.Request) llm.Response {
			state, got, _ := memoryRequestIndex(t, req, "personal")
			want := ""
			if wantState == "current" {
				want = memoryExpectedIndex(t, root, "personal")
			}
			if state != wantState || got != want || !strings.Contains(got, marker) {
				t.Fatalf("personal index state=%s body=%q, want %s %q with %q", state, got, wantState, want, marker)
			}
			return finalResponse("observed")
		}
	}
	s.client.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		wantFull("current", "opaque-before-fold-1"), wantFull("missing", ""), wantFull("current", "opaque-after-fold-2"),
	}})
	turn := func() {
		t.Helper()
		if _, err := s.ProcessInput(context.Background(), "go", nil); err != nil {
			t.Fatal(err)
		}
	}
	turn()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	for range 12 {
		s.appendTurnWithTranscriptMessage(schema.TurnUserInput, llm.User("opaque-old"), llm.User("opaque-old"))
	}
	if err := s.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	turn()
	memorySeedPage(t, root, "personal", "fact.md", "opaque-after-fold-2")
	turn()
}

// memorySeedManyPages writes count pages into scope, each stamped updated and
// with a description long enough that the rendered index outgrows the
// projection budget, and returns them as listed.
func memorySeedManyPages(t *testing.T, root, scope string, count int, updated string) []memoryPage {
	t.Helper()
	for i := range count {
		memorySeedPage(t, root, scope, fmt.Sprintf("p%03d.md", i), fmt.Sprintf("opaque-filler-%03d-%s", i, strings.Repeat("x", 40)), memorySeedUpdated(updated))
	}
	return memoryListedPages(t, root, scope)
}

// The baseline holds the whole rendered index, past the projection budget, so
// a page moving past the budget is never a change.
func TestMemoryRefreshBaselineIsTheFullRenderedIndex(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	pages := memorySeedManyPages(t, root, "personal", 200, "2026-10-01")
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root}), withSteps(
		func(llm.Request) llm.Response { return finalResponse("observed") },
	))
	if _, err := s.ProcessInput(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	baseline, known := s.memoryBaselineFor("personal")
	if want := renderMemoryIndex(pages); !known || baseline.index != want || len(want) <= memoryProjectionCap {
		t.Fatalf("baseline known=%t holds %d bytes, want the whole %d-byte rendering past the %d-byte budget", known, len(baseline.index), len(want), memoryProjectionCap)
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
	r.s = newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root, clock: r.clk, testOnly: testConfig{memoryRealBudget: true, memoryBeforeIO: func(scope, op string) error {
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
	memorySeedPage(t, root, "personal", "fact.md", "opaque-stall-1")
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

// The session's own page write while its turn-start read is still in flight
// is not echoed back by any later boundary.
func TestMemoryRefreshOwnPageWriteDuringStalledReadIsNotEchoed(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	memorySeedPage(t, root, "personal", "fact.md", "opaque-before-write-1")
	r := newStalledMemoryRefresh(t, root)
	r.boundary()
	flight := r.stalledBoundary(t)
	if res := memoryExec(t, r.s, "memory_write", map[string]any{"scope": "personal", "file_path": "fact.md", "content": "---\ndescription: opaque-own-write-2\n---\n"}); res.IsError {
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

// Another session's page changes reach the next turn as the changed index
// lines only, never the full index again; the unchanged turn after it
// carries nothing new.
func TestMemoryRefreshDeliversIndexDeltaOncePerTurn(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	memorySeedPage(t, root, "personal", "kept.md", "opaque-kept-1")
	dropped := memorySeedPage(t, root, "personal", "dropped.md", "opaque-dropped-2")
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
	if err := os.Remove(dropped); err != nil {
		t.Fatal(err)
	}
	memorySeedPage(t, root, "personal", "added.md", "opaque-added-3")
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
	memorySeedPage(t, root, "personal", "fact.md", "opaque-small-1")
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
	for i := range 200 {
		memorySeedPage(t, root, "personal", fmt.Sprintf("bulk-%03d.md", i), fmt.Sprintf("opaque-bulk-line-%03d-%s", i, strings.Repeat("x", 20)))
	}
	if _, err := s.ProcessInput(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if delta == "" || len(delta) > memoryIndexDeltaCap || strings.Contains(delta, "opaque-bulk-line-") {
		t.Fatalf("large delta len=%d lists its lines or is missing: %q", len(delta), delta)
	}
}

// Change blocks compare the whole rendered index: a page added outside the
// projected window and a description change each deliver a block, while a
// page that merely moved past the budget delivers nothing about itself.
func TestMemoryRefreshIndexDeltaComparesTheFullIndex(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	memorySeedManyPages(t, root, "personal", 200, "2026-01-01")
	var deltas []string
	deltaTurn := func(want int) func(llm.Request) llm.Response {
		return func(req llm.Request) llm.Response {
			if got := memoryContextMessages(req); got != want {
				t.Fatalf("turn carries %d memory contexts, want %d", got, want)
			}
			deltas = append(deltas, latestMemoryContext(req, "personal"))
			return finalResponse("observed")
		}
	}
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root}), withSteps(
		deltaTurn(1), deltaTurn(2), deltaTurn(3), deltaTurn(4),
	))
	turn := func() {
		t.Helper()
		if _, err := s.ProcessInput(context.Background(), "go", nil); err != nil {
			t.Fatal(err)
		}
	}
	turn()
	if display, _ := apptranscript.ParseMemoryContext(deltas[0], "memory_personal"); !display.Truncated {
		t.Fatal("fixture index fits the projection; it must outgrow it")
	}
	// The oldest page sorts last, outside the projected window.
	memorySeedPage(t, root, "personal", "old.md", "opaque-old-page", memorySeedUpdated("2025-01-01"))
	turn()
	// p000 is the newest page: stamps tie, and ties sort by path.
	memorySeedPage(t, root, "personal", "p000.md", "opaque-head-2", memorySeedUpdated("2026-01-01"))
	turn()
	// A new newest page pushes the last shown page past the budget.
	memorySeedPage(t, root, "personal", "new.md", "opaque-new-page", memorySeedUpdated("2026-06-01"))
	turn()
	if !strings.Contains(deltas[1], `+ "- [old](old.md) — opaque-old-page`) || strings.Contains(deltas[1], `- "`) {
		t.Fatalf("a page outside the window should arrive as one added line: %q", deltas[1])
	}
	if !strings.Contains(deltas[2], `- "- [p000](p000.md) — opaque-filler-000`) || !strings.Contains(deltas[2], `+ "- [p000](p000.md) — opaque-head-2`) {
		t.Fatalf("a description change should arrive as its old and new lines: %q", deltas[2])
	}
	if !strings.Contains(deltas[3], "opaque-new-page") || strings.Contains(deltas[3], `- "`) || strings.Count(deltas[3], `+ "`) != 1 {
		t.Fatalf("a page pushed past the budget should not appear as a change: %q", deltas[3])
	}
}

// A stalled read of a known index delivers no change block; the next
// completed read delivers the other session's change once.
func TestMemoryRefreshStalledReadDefersIndexDelta(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	memorySeedPage(t, root, "personal", "a.md", "opaque-defer-1")
	r := newStalledMemoryRefresh(t, root)
	r.boundary()
	memorySeedPage(t, root, "personal", "b.md", "opaque-defer-2")
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

// describedMemoryPage is a page body with a fixed description, so another
// session changing its body changes no index line: the session hears of it
// only through a page notice.
func describedMemoryPage(body string) string {
	return "---\ndescription: opaque-described-page\n---\n" + body
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
	memorySeedPage(t, root, "personal", "fact.md", "opaque-index-1")
	page := writeMemoryPage(t, root, "opaque-read-page.md", describedMemoryPage("opaque-page-body-1\n"))
	writeMemoryPage(t, root, "opaque-unread-page.md", describedMemoryPage("opaque-unread-body-1\n"))
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
	// Removing the read page also removes its index line: that turn carries a
	// change block before the notice.
	changedTurn, removedTurn, unchangedTurn := noticeTurn(2), noticeTurn(4), noticeTurn(4)
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
	writeMemoryPage(t, root, "opaque-read-page.md", describedMemoryPage("opaque-page-body-2\n"))
	writeMemoryPage(t, root, "opaque-unread-page.md", describedMemoryPage("opaque-unread-body-2\n"))
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
	memorySeedPage(t, root, "personal", "fact.md", "opaque-index-1")
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
	if got, err := os.ReadFile(page); err != nil || string(got) != "---\n"+memoryOwnStamps(s)+"---\nopaque-own-body-2\n" {
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
	memorySeedPage(t, root, "personal", "fact.md", "opaque-index-1")
	writeMemoryPage(t, root, "opaque-deferred-page.md", describedMemoryPage("opaque-page-body-1\n"))
	r := newStalledMemoryRefresh(t, root)
	r.boundary()
	if res := memoryExec(t, r.s, "memory_read", map[string]any{"scope": "personal", "file_path": "opaque-deferred-page.md"}); res.IsError {
		t.Fatal(res.Output)
	}
	writeMemoryPage(t, root, "opaque-deferred-page.md", describedMemoryPage("opaque-page-body-2\n"))
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
	memorySeedPage(t, root, "personal", "fact.md", "opaque-index-1")
	writeMemoryPage(t, root, "opaque-late-page.md", describedMemoryPage("opaque-late-body-1\n"))
	r := newStalledMemoryRefresh(t, root)
	r.boundary()
	if res := memoryExec(t, r.s, "memory_read", map[string]any{"scope": "personal", "file_path": "opaque-late-page.md"}); res.IsError {
		t.Fatal(res.Output)
	}
	writeMemoryPage(t, root, "opaque-late-page.md", describedMemoryPage("opaque-late-body-2\n"))
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

// A read the session's own page write made stale is discarded even once it
// has completed: the next boundary reads again and echoes nothing.
func TestMemoryRefreshDiscardsAStaleReadEvenWhenComplete(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	memorySeedPage(t, root, "personal", "fact.md", "opaque-stale-1")
	r := newStalledMemoryRefresh(t, root)
	r.boundary()
	flight := r.stalledBoundary(t)
	if res := memoryExec(t, r.s, "memory_write", map[string]any{"scope": "personal", "file_path": "fact.md", "content": "---\ndescription: opaque-stale-own-2\n---\n"}); res.IsError {
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
	memorySeedPage(t, root, "personal", "fact.md", "opaque-index-1")
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
	memorySeedPage(t, root, "personal", "fact.md", "opaque-index-1")
	writeMemoryPage(t, root, "opaque-kept-page.md", describedMemoryPage("opaque-kept-body-1\n"))
	s := newScriptedSummaryCompactSession(t, "memory-page-summary", func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("## Progress\nopaque-page-fold")}
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
	writeMemoryPage(t, root, "opaque-kept-page.md", describedMemoryPage("opaque-kept-body-2\n"))
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
	memorySeedPage(t, root, "personal", "fact.md", "opaque-index-1")
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
		// The index itself lists the page; only a notice would name it outside one.
		if _, index := apptranscript.ParseMemoryContext(text, "memory_personal"); !index && strings.Contains(text, "opaque-forgotten-page.md") {
			t.Fatalf("resumed session noticed a page read only before the resume: %q", text)
		}
	}
}

// A page whose record read fails, before any page is tracked, is simply left
// untracked: the session neither panics nor starts tracking it.
func TestMemoryRefreshUnreadablePageStaysUntracked(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	memorySeedPage(t, root, "personal", "fact.md", "opaque-index-1")
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
	memorySeedPage(t, root, "personal", "fact.md", "opaque-index-1")
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
	memorySeedPage(t, root, "personal", "fact.md", "opaque-index-1")
	page := writeMemoryPage(t, root, "opaque-raced-page.md", describedMemoryPage("opaque-raced-body-1\n"))
	var changed atomic.Bool
	change := func() {
		if changed.CompareAndSwap(false, true) {
			if err := os.WriteFile(page, []byte(describedMemoryPage("opaque-raced-body-2\n")), 0o600); err != nil {
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
	// A dot file is never a page, so the scope's index stays missing while the
	// session reads the file and is told it changed.
	page := writeMemoryPage(t, root, ".opaque-noticed-page.md", "opaque-noticed-body-1\n")
	s := newSession(t, withDir(workspace), withConfig(SessionConfig{StateDir: history, MemoryStateRoot: root}), withSteps(
		func(llm.Request) llm.Response {
			return memoryCallResponse("memory_read", map[string]any{"scope": "personal", "file_path": ".opaque-noticed-page.md"})
		},
		func(llm.Request) llm.Response { return finalResponse("read") },
		func(req llm.Request) llm.Response {
			if !strings.Contains(latestMemoryContext(req, "personal"), ".opaque-noticed-page.md") {
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

// A full index projection reports size only when it leaves pages out, with
// the partial sentence, and decodes as truncated. It never points at the
// gardening-memory skill, whether or not the session can load it. No
// projection carries an explicit truncated flag any more.
func TestMemoryProjectionReportsOnlyAPartialIndex(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		pages     int
		truncated bool
		withSkill bool
	}{
		{"short", 1, false, true},
		{"partial", 200, true, true},
		{"partial-without-skills", 200, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			memorySeedManyPages(t, root, "personal", tc.pages, "2026-10-01")
			var text string
			s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root}), withSteps(func(req llm.Request) llm.Response {
				text = latestMemoryContext(req, "personal")
				return finalResponse("observed")
			}))
			if !tc.withSkill {
				s.reg.Remove("use_skill")
			}
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
			if strings.Contains(text, memoryIndexPartial) != tc.truncated {
				t.Fatalf("partial sentence present=%t, want %t: %q", !tc.truncated, tc.truncated, text[:min(len(text), 400)])
			}
			if strings.Contains(text, "gardening-memory") {
				t.Fatalf("projection points at gardening-memory: %q", text[:min(len(text), 400)])
			}
		})
	}
}

// The size note never reports a long page as no bigger than the limit: sizes
// round up to the next KB.
func TestMemoryPageSizeNoteRoundsUp(t *testing.T) {
	t.Parallel()
	for size, want := range map[int]string{memoryPageSizeLimit + 1: "(5 KB)", 5 * 1024: "(5 KB)", 5*1024 + 1: "(6 KB)"} {
		for _, withSkill := range []bool{true, false} {
			if note := memoryPageSizeNote(size, withSkill); !strings.Contains(note, want) {
				t.Errorf("memoryPageSizeNote(%d, %t) = %q, want %s", size, withSkill, note, want)
			}
		}
	}
}

// memory_read of a page longer than 4096 bytes ends with the size note the
// session builds for it; a shorter page and the index never get one. The note
// points at the gardening-memory skill only while the session can use it.
func TestMemoryReadNotesALongPage(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("opaque-long-page-line\n", 230) // 5060 bytes
	for _, tc := range []struct {
		name      string
		file      string
		body      string
		withSkill bool
		noted     bool
	}{
		{"long-page", "opaque-long.md", long, true, true},
		{"long-page-without-skills", "opaque-long.md", long, false, true},
		{"short-page", "opaque-short.md", "opaque-short-page\n", true, false},
		// The index never gets a page's size note. memory_read does not
		// render the index yet, so this reads a raw root MEMORY.md.
		{"long-index", "MEMORY.md", long, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			memorySeedPage(t, root, "personal", "fact.md", "opaque-index-1")
			writeMemoryPage(t, root, tc.file, tc.body)
			s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root}))
			if !tc.withSkill {
				s.reg.Remove("use_skill")
			}
			res := memoryExec(t, s, "memory_read", map[string]any{"scope": "personal", "file_path": tc.file})
			if res.IsError {
				t.Fatal(res.Output)
			}
			out := res.Output
			note := memoryPageSizeNote(len(tc.body), tc.withSkill)
			if strings.HasSuffix(out, note) != tc.noted {
				t.Fatalf("result ends with the size note=%t, want %t: %.300s", !tc.noted, tc.noted, out[max(0, len(out)-300):])
			}
			if tc.noted && strings.Contains(note, "gardening-memory") != tc.withSkill {
				t.Fatalf("note names gardening-memory=%t, want %t: %q", !tc.withSkill, tc.withSkill, note)
			}
		})
	}
}

// The first projection of a scope that still has a hand-written MEMORY.md
// migrates it: its descriptions move into the pages and the projection is
// the generated index.
func TestMemoryProjectionMigratesAHandWrittenIndex(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	scope := filepath.Join(root, "memory", "personal")
	if err := os.MkdirAll(scope, 0o700); err != nil {
		t.Fatal(err)
	}
	writeMemoryPage(t, root, "MEMORY.md", "- [Cents](cents.md) — opaque-migrated-description\n")
	writeMemoryPage(t, root, "cents.md", "# Cents\n")
	var text string
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root}), withSteps(func(req llm.Request) llm.Response {
		text = latestMemoryContext(req, "personal")
		return finalResponse("observed")
	}))
	if _, err := s.ProcessInput(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	display, ok := apptranscript.ParseMemoryContext(text, "memory_personal")
	if !ok || !strings.Contains(display.Content, "- [Cents](cents.md) — opaque-migrated-description") {
		t.Fatalf("projection=%+v ok=%t", display, ok)
	}
	if _, err := os.Stat(filepath.Join(scope, memoryLegacyIndexBackup)); err != nil {
		t.Fatal(err)
	}
}

// A page another session deletes reaches this session as a removed line; a
// description change as the old line removed and the new one added; the tag
// header never appears in a change block.
func TestMemoryRefreshDeltaListsPageLinesOnly(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	memorySeedPage(t, root, "personal", "kept.md", "opaque-kept")
	gone := memorySeedPage(t, root, "personal", "gone.md", "opaque-gone")
	memorySeedPage(t, root, "personal", "changed.md", "opaque-before")
	var delta string
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root}), withSteps(
		func(llm.Request) llm.Response { return finalResponse("first") },
		func(req llm.Request) llm.Response {
			delta = latestMemoryContext(req, "personal")
			return finalResponse("second")
		},
	))
	if _, err := s.ProcessInput(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	memorySeedPage(t, root, "personal", "changed.md", "opaque-after", memorySeedTags("newtag"))
	if _, err := s.ProcessInput(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`- "- [gone](gone.md) — opaque-gone`, `- "- [changed](changed.md) — opaque-before`, `+ "- [changed](changed.md) — opaque-after [newtag]`} {
		if !strings.Contains(delta, want) {
			t.Fatalf("delta lacks %q: %s", want, delta)
		}
	}
	if strings.Contains(delta, "opaque-kept") || strings.Contains(delta, "Tags:") {
		t.Fatalf("delta lists an unchanged line or the header: %s", delta)
	}
}

// The session's own write of any page becomes its baseline: the next turn
// carries no change block for it.
func TestMemoryRefreshIgnoresOwnPageWriteOfAnyPage(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	memorySeedPage(t, root, "personal", "kept.md", "opaque-kept")
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root}), withSteps(
		func(llm.Request) llm.Response {
			return memoryCallResponse("memory_write", map[string]any{"scope": "personal", "file_path": "new.md", "content": "---\ndescription: opaque-own-page\n---\n"})
		},
		func(llm.Request) llm.Response { return finalResponse("wrote") },
		func(req llm.Request) llm.Response {
			if got := memoryContextMessages(req); got != 1 {
				t.Fatalf("next turn carries %d memory contexts, want only the first projection", got)
			}
			return finalResponse("next")
		},
	))
	for range 2 {
		if _, err := s.ProcessInput(context.Background(), "go", nil); err != nil {
			t.Fatal(err)
		}
	}
}

// An own page write while the model was last told the scope is unavailable
// does not make the rendering a baseline: the model never saw the index, so
// the next completed boundary delivers it in full, other pages included.
func TestMemoryRefreshOwnPageWriteAfterUnavailableDeliversFullIndex(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	memorySeedPage(t, root, "personal", "other.md", "opaque-other-page")
	r := newStalledMemoryRefresh(t, root)
	flight := r.stalledBoundary(t)
	if got := memoryContextCount(r.s); got != 1 {
		t.Fatalf("stalled first boundary appended %d contexts, want the unavailable projection", got)
	}
	if res := memoryExec(t, r.s, "memory_write", map[string]any{"scope": "personal", "file_path": "mine.md", "content": "---\ndescription: opaque-mine-page\n---\n"}); res.IsError {
		t.Fatal(res.Output)
	}
	r.finish(flight)
	r.boundary()
	r.boundary()
	text := lastMemoryContextText(r.s)
	display, ok := apptranscript.ParseMemoryContext(text, "memory_personal")
	if got := memoryContextCount(r.s); got != 2 || !ok || display.State != "current" || !strings.Contains(display.Content, "opaque-other-page") {
		t.Fatalf("contexts=%d last=%q, want the full index after the unavailable one", got, text)
	}
}

// The session's own page write patches its baseline with that page's line
// only: pages another session added or deleted since the last boundary still
// reach the next turn as change lines, and the own page is not echoed.
func TestMemoryRefreshOwnPageWriteKeepsOtherSessionsChanges(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	memorySeedPage(t, root, "personal", "kept.md", "opaque-kept")
	gone := memorySeedPage(t, root, "personal", "gone.md", "opaque-gone")
	var delta string
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root}), withSteps(
		func(llm.Request) llm.Response { return finalResponse("first") },
		func(llm.Request) llm.Response {
			// Another session changes the scope after this turn's boundary.
			memorySeedPage(t, root, "personal", "other.md", "opaque-other")
			if err := os.Remove(gone); err != nil {
				t.Fatal(err)
			}
			return memoryCallResponse("memory_write", map[string]any{"scope": "personal", "file_path": "mine.md", "content": "---\ndescription: opaque-mine\n---\n"})
		},
		func(llm.Request) llm.Response { return finalResponse("wrote") },
		func(req llm.Request) llm.Response {
			delta = latestMemoryContext(req, "personal")
			return finalResponse("next")
		},
	))
	for range 3 {
		if _, err := s.ProcessInput(context.Background(), "go", nil); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []string{`+ "- [other](other.md) — opaque-other`, `- "- [gone](gone.md) — opaque-gone`} {
		if !strings.Contains(delta, want) {
			t.Fatalf("delta lacks %q: %s", want, delta)
		}
	}
	if strings.Contains(delta, "opaque-mine") || strings.Contains(delta, "opaque-kept") {
		t.Fatalf("delta echoes the own page or an unchanged line: %s", delta)
	}
}

// A session that cannot write, edit and delete memory never migrates: its
// projection renders the pages as they are, with fallback descriptions, and
// neither writes a page nor moves the hand-written index.
func TestMemoryReadOnlySessionDoesNotMigrate(t *testing.T) {
	t.Parallel()
	for _, denied := range []string{"memory_write", "memory_edit", "memory_delete"} {
		t.Run(denied, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			scope := filepath.Join(root, "memory", "personal")
			if err := os.MkdirAll(scope, 0o700); err != nil {
				t.Fatal(err)
			}
			const legacy = "- [Cents](cents.md) — opaque-legacy-description\n"
			writeMemoryPage(t, root, "MEMORY.md", legacy)
			writeMemoryPage(t, root, "cents.md", "# Cents\n")
			var text string
			s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root}), withSteps(func(req llm.Request) llm.Response {
				text = latestMemoryContext(req, "personal")
				return finalResponse("observed")
			}))
			s.reg.Remove(denied)
			refreshModelFacingCaches(s)
			if _, err := s.ProcessInput(context.Background(), "go", nil); err != nil {
				t.Fatal(err)
			}
			display, ok := apptranscript.ParseMemoryContext(text, "memory_personal")
			if !ok || !strings.Contains(display.Content, "- [Cents](cents.md) — Cents (no description)") {
				t.Fatalf("projection=%+v ok=%t", display, ok)
			}
			if raw, err := os.ReadFile(filepath.Join(scope, "cents.md")); err != nil || string(raw) != "# Cents\n" {
				t.Fatalf("cents.md=%q, %v", raw, err)
			}
			if raw, err := os.ReadFile(filepath.Join(scope, "MEMORY.md")); err != nil || string(raw) != legacy {
				t.Fatalf("MEMORY.md=%q, %v", raw, err)
			}
		})
	}
}

// On a case-insensitive filesystem a write or delete naming a page in
// another case is still the session's own: the page's listed line is
// patched, so neither the write nor the delete is echoed back.
func TestMemoryRefreshIgnoresOwnPageWriteInAnotherCase(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := memorySeedPage(t, root, "personal", "fact.md", "opaque-cased-1")
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "FACT.md")); err != nil {
		t.Skip("the filesystem is case-sensitive")
	}
	nextTurn := func(after string) func(llm.Request) llm.Response {
		return func(req llm.Request) llm.Response {
			if got := memoryContextMessages(req); got != 1 {
				t.Fatalf("turn after the %s carries %d memory contexts, want only the first full index", after, got)
			}
			return finalResponse("next")
		}
	}
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root}), withSteps(
		func(llm.Request) llm.Response {
			return memoryCallResponse("memory_read", map[string]any{"scope": "personal", "file_path": "Fact.md"})
		},
		func(llm.Request) llm.Response {
			return memoryCallResponse("memory_write", map[string]any{"scope": "personal", "file_path": "Fact.md", "content": "---\ndescription: opaque-cased-2\n---\n"})
		},
		func(llm.Request) llm.Response { return finalResponse("wrote") },
		nextTurn("write"),
		func(llm.Request) llm.Response {
			return memoryCallResponse("memory_delete", map[string]any{"scope": "personal", "file_path": "Fact.md"})
		},
		func(llm.Request) llm.Response { return finalResponse("deleted") },
		nextTurn("delete"),
	))
	for _, input := range []string{"write", "next", "delete", "next"} {
		if _, err := s.ProcessInput(context.Background(), input, nil); err != nil {
			t.Fatal(err)
		}
	}
}

// A page whose title holds a Markdown link is still matched by its own path
// when the session rewrites it, so the rewrite replaces its line and is not
// echoed back.
func TestMemoryRefreshIgnoresOwnRewriteOfPageTitledWithALink(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	memorySeedPage(t, root, "personal", "kept.md", "opaque-kept")
	write := func(description string) func(llm.Request) llm.Response {
		return func(llm.Request) llm.Response {
			return memoryCallResponse("memory_write", map[string]any{"scope": "personal", "file_path": "a.md", "content": "---\ndescription: " + description + "\n---\n# Use [Cents](money.md) ) — here\n"})
		}
	}
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root}), withSteps(
		write("opaque-linked-1"),
		func(llm.Request) llm.Response { return finalResponse("wrote") },
		write("opaque-linked-2"),
		func(llm.Request) llm.Response { return finalResponse("rewrote") },
		func(req llm.Request) llm.Response {
			if got := memoryContextMessages(req); got != 1 {
				t.Fatalf("turn after the rewrites carries %d memory contexts, want only the first full index", got)
			}
			return finalResponse("next")
		},
	))
	for range 3 {
		if _, err := s.ProcessInput(context.Background(), "go", nil); err != nil {
			t.Fatal(err)
		}
	}
}

// On a case-insensitive filesystem a page read under one spelling and
// written under another, directories included, is one page: the write
// replaces its listed line and its read record, so the next turn carries
// neither a change block nor a page notice.
func TestMemoryRefreshOwnWriteMatchesPageReadInAnotherCase(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := memorySeedPage(t, root, "personal", "notes/fact.md", "opaque-nested-1")
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "FACT.md")); err != nil {
		t.Skip("the filesystem is case-sensitive")
	}
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root}), withSteps(
		func(llm.Request) llm.Response {
			return memoryCallResponse("memory_read", map[string]any{"scope": "personal", "file_path": "notes/fact.md"})
		},
		func(llm.Request) llm.Response {
			return memoryCallResponse("memory_write", map[string]any{"scope": "personal", "file_path": "Notes/Fact.md", "content": "---\ndescription: opaque-nested-2\n---\n"})
		},
		func(llm.Request) llm.Response { return finalResponse("wrote") },
		func(req llm.Request) llm.Response {
			if got := memoryContextMessages(req); got != 1 {
				t.Fatalf("turn after the write carries %d memory contexts, want only the first full index: %s", got, latestMemoryContext(req, "personal"))
			}
			return finalResponse("next")
		},
	))
	for range 2 {
		if _, err := s.ProcessInput(context.Background(), "go", nil); err != nil {
			t.Fatal(err)
		}
	}
}
