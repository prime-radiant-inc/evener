//go:build unix

package agent

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/clock"
)

// memoryRaceRounds is how many rounds of racing writes one run of the
// concurrency test plays.
const memoryRaceRounds = 100

// memoryWithoutStamps is a page's bytes with the updated and by stamps
// removed from its frontmatter, so a stamped page compares equal to the
// content a tool wrote.
func memoryWithoutStamps(raw string) string {
	block, body, ok := splitMemoryFrontmatter(raw)
	if !ok {
		return raw
	}
	var kept []string
	for line := range strings.Lines(block) {
		if !strings.HasPrefix(line, "updated:") && !strings.HasPrefix(line, "by:") {
			kept = append(kept, line)
		}
	}
	return "---\n" + strings.Join(kept, "") + "---\n" + body
}

// memoryRacePage is the content a racing write gives a page.
func memoryRacePage(who string, round int, step string) string {
	return fmt.Sprintf("---\ndescription: %s wrote round %d %s\n---\n%s-%d-%s\n", who, round, step, who, round, step)
}

// memoryPageState is a page's content, stamps aside, or "" when it is absent.
func memoryPageState(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return memoryWithoutStamps(string(raw))
}

// Two sessions writing one scope at once, with a third rendering its index
// throughout. Each round, A writes shared.md and doomed.md and edits its own
// page; B writes shared.md twice and writes then deletes doomed.md. When a
// round ends, each page must hold one session's last operation on it:
// shared.md is A's write or B's second write, doomed.md is A's write or
// absent. B's first shared.md write or its doomed.md write surviving means a
// stamp wrote back content its session had read before a later write. The
// observer must never render a "(no description)" or "(frontmatter
// unreadable)" line: every page written here has a description.
func TestMemoryConcurrentSessionsKeepEachPageLastWrite(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	scope := filepath.Join(root, "memory", "personal")
	memorySeedPage(t, root, "personal", "a-own.md", "A's own page")
	open := func() *Session {
		return newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root}))
	}
	a, b, observer := open(), open(), open()
	ctx := context.Background()
	// Each call reports whether the tool succeeded; every call here should.
	write := func(s *Session, file, content string) bool {
		_, err := s.execMemoryWrite(ctx, nil, map[string]any{"scope": "personal", "file_path": file, "content": content})
		return err == nil
	}
	del := func(s *Session, file string) bool {
		_, err := s.execMemoryDelete(ctx, nil, map[string]any{"scope": "personal", "file_path": file})
		return err == nil
	}
	edit := func(s *Session, file, old, replacement string) bool {
		_, err := s.execMemoryEdit(ctx, nil, map[string]any{"scope": "personal", "file_path": file, "old_string": old, "new_string": replacement})
		return err == nil
	}
	if !edit(a, "a-own.md", "updated: 2026-10-01\n---\n", "updated: 2026-10-01\n---\nbody-v0\n") {
		t.Fatal("A could not seed its own page's body")
	}

	var writersDone atomic.Bool
	var observed sync.WaitGroup
	var renders, badRenders atomic.Int64
	var firstBad atomic.Value
	observed.Go(func() {
		for !writersDone.Load() {
			p := observer.readMemoryIndex("personal")
			renders.Add(1)
			if p.Status != "current" && p.Status != "missing" || strings.Contains(p.Content, memoryNoDescription) || strings.Contains(p.Content, memoryFrontmatterUnreadable) {
				badRenders.Add(1)
				firstBad.CompareAndSwap(nil, fmt.Sprintf("status=%s content=%q", p.Status, p.Content))
			}
		}
	})

	var lostWrites, resurrected int
	var firstViolation string
	for round := range memoryRaceRounds {
		start := make(chan struct{})
		var aOK, bOK [3]bool
		var wg sync.WaitGroup
		wg.Go(func() {
			<-start
			aOK[0] = write(a, "shared.md", memoryRacePage("A", round, "only"))
			aOK[1] = write(a, "doomed.md", memoryRacePage("A", round, "only"))
			aOK[2] = edit(a, "a-own.md", fmt.Sprintf("body-v%d\n", round), fmt.Sprintf("body-v%d\n", round+1))
		})
		wg.Go(func() {
			<-start
			bOK[0] = write(b, "shared.md", memoryRacePage("B", round, "first"))
			bOK[1] = write(b, "shared.md", memoryRacePage("B", round, "second"))
			bOK[2] = write(b, "doomed.md", memoryRacePage("B", round, "doomed")) && del(b, "doomed.md")
		})
		close(start)
		wg.Wait()
		if aOK != [3]bool{true, true, true} || bOK != [3]bool{true, true, true} {
			t.Fatalf("round %d: a tool call failed (A=%v B=%v)", round, aOK, bOK)
		}
		shared := memoryPageState(t, filepath.Join(scope, "shared.md"))
		if shared != memoryRacePage("A", round, "only") && shared != memoryRacePage("B", round, "second") {
			lostWrites++
			if firstViolation == "" {
				firstViolation = fmt.Sprintf("round %d: shared.md=%q, want A's write or B's second write", round, shared)
			}
		}
		doomed := memoryPageState(t, filepath.Join(scope, "doomed.md"))
		if doomed != "" && doomed != memoryRacePage("A", round, "only") {
			resurrected++
			if firstViolation == "" {
				firstViolation = fmt.Sprintf("round %d: doomed.md=%q, want A's write or absent", round, doomed)
			}
		}
	}
	writersDone.Store(true)
	observed.Wait()

	own, err := os.ReadFile(filepath.Join(scope, "a-own.md"))
	ownOK := err == nil && strings.HasSuffix(string(own), fmt.Sprintf("---\nbody-v%d\n", memoryRaceRounds))
	t.Logf("%d rounds: %d lost writes, %d resurrected pages, %d of %d renders bad, own page ok=%t", memoryRaceRounds, lostWrites, resurrected, badRenders.Load(), renders.Load(), ownOK)
	if lostWrites > 0 || resurrected > 0 || badRenders.Load() > 0 || !ownOK {
		bad, _ := firstBad.Load().(string)
		t.Fatalf("%d rounds: %d lost writes, %d resurrected pages, %d of %d renders bad; own page=%q err=%v\nfirst page violation: %s\nfirst bad render: %.400s",
			memoryRaceRounds, lostWrites, resurrected, badRenders.Load(), renders.Load(), own, err, firstViolation, bad)
	}
}

// postWritePausingClock is the real clock, except that once armed, the first
// Now() made while the page at path already holds marker blocks until
// release closes. A tool that stamps the content it writes reads the clock
// before its write lands, so it never pauses. A stamp that reads the page
// back after the tool's write and writes it again reads the clock between
// the two, and is held there.
type postWritePausingClock struct {
	clock.Clock
	path, marker string
	armed        atomic.Bool
	paused       chan struct{}
	release      chan struct{}
}

func newPostWritePausingClock(path, marker string) *postWritePausingClock {
	return &postWritePausingClock{Clock: clock.Real(), path: path, marker: marker, paused: make(chan struct{}), release: make(chan struct{})}
}

func (c *postWritePausingClock) Now() time.Time {
	if c.armed.Load() {
		if raw, err := os.ReadFile(c.path); err == nil && strings.Contains(string(raw), c.marker) && c.armed.CompareAndSwap(true, false) {
			close(c.paused)
			<-c.release
		}
	}
	return c.Clock.Now()
}

// memoryStampRace runs A's memory_write of page with content, whose body
// holds marker, and returns once A's write has landed: either A's tool
// returned, or it read the clock after the write and is held there. Either
// way, the page on disk must already carry A's stamps, so the tool wrote
// them with the content. finish lets A's tool go on and waits for it.
func memoryStampRace(t *testing.T, root, page, content, marker string) (finish func()) {
	t.Helper()
	clk := newPostWritePausingClock(filepath.Join(root, "memory", "personal", page), marker)
	a := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root, clock: clk}))
	clk.armed.Store(true)
	done := make(chan error, 1)
	go func() {
		_, err := a.execMemoryWrite(context.Background(), nil, map[string]any{"scope": "personal", "file_path": page, "content": content})
		done <- err
	}()
	var aErr error
	returned := false
	select {
	case <-clk.paused:
	case aErr = <-done:
		returned = true
		if !clk.armed.CompareAndSwap(true, false) {
			// A late clock read paused after the tool returned; let it go.
			<-clk.paused
		}
	}
	raw, err := os.ReadFile(clk.path)
	if err != nil || !strings.Contains(string(raw), memoryYAMLField("by", a.ID())) {
		t.Errorf("A's write landed without A's stamps (A returned=%t): page=%q err=%v", returned, raw, err)
	}
	return func() {
		close(clk.release)
		if !returned {
			aErr = <-done
		}
		if aErr != nil {
			t.Fatalf("A's write: %v", aErr)
		}
	}
}

// B reads the page A just wrote and deletes it, while A's tool may still be
// running. B saw A's content, so A's write came before B's delete: the page
// must stay deleted.
func TestMemoryStampDoesNotResurrectAPageDeletedAfterItWasRead(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "memory", "personal", "fact.md")
	finish := memoryStampRace(t, root, "fact.md", "---\ndescription: A's fact\n---\nA-fact\n", "A-fact")
	b := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root}))
	read, err := b.execMemoryRead(context.Background(), nil, map[string]any{"scope": "personal", "file_path": "fact.md"})
	if err != nil || !strings.Contains(fmt.Sprint(read), "A-fact") {
		t.Fatalf("B's read=%v err=%v, want A's content", read, err)
	}
	if _, err := b.execMemoryDelete(context.Background(), nil, map[string]any{"scope": "personal", "file_path": "fact.md"}); err != nil {
		t.Fatalf("B's delete: %v", err)
	}
	finish()
	if raw, err := os.ReadFile(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("B deleted the page after reading A's write, but it exists again: %q (err=%v)", raw, err)
	}
}

// B edits the content A just wrote, while A's tool may still be running.
// B's edit applied to A's content, so it came after A's write: the page must
// keep B's edit.
func TestMemoryStampDoesNotUndoAnEditOfThePageItStamps(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "memory", "personal", "fact.md")
	finish := memoryStampRace(t, root, "fact.md", "---\ndescription: A's fact\n---\nA-fact\n", "A-fact")
	b := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root}))
	if _, err := b.execMemoryEdit(context.Background(), nil, map[string]any{"scope": "personal", "file_path": "fact.md", "old_string": "A-fact", "new_string": "B-corrected-fact"}); err != nil {
		t.Fatalf("B's edit: %v", err)
	}
	finish()
	if raw, err := os.ReadFile(path); err != nil || !strings.Contains(string(raw), "B-corrected-fact") {
		t.Fatalf("B's edit of A's write was lost: page=%q err=%v", raw, err)
	}
}
