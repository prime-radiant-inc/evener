package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/internal/apptranscript"
)

// These tests probe how the generated memory index behaves as a scope grows:
// many distinct tags, many pages, and a large dot directory inside the scope.

// sprawlMemoryPages is n pages spread evenly over tags distinct tags, each
// stamped one day after the last, so page n-1 is the newest.
func sprawlMemoryPages(n, tags int) []memoryPage {
	base := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	pages := make([]memoryPage, n)
	for i := range n {
		pages[i] = memoryPage{
			Path:           fmt.Sprintf("notes/page-%04d.md", i),
			Title:          fmt.Sprintf("Page %d", i),
			Description:    fmt.Sprintf("How the build handles case %d when the cache is cold and the runner restarts", i),
			HasDescription: true,
			Tags:           []string{fmt.Sprintf("topic-%03d", i%tags)},
			Updated:        base.AddDate(0, 0, i).Format(time.DateOnly),
		}
	}
	return pages
}

// memoryProjectedPageLines counts the page lines in a projected index.
func memoryProjectedPageLines(content string) int {
	n := 0
	for line := range strings.SplitSeq(content, "\n") {
		if strings.HasPrefix(line, "- [") {
			n++
		}
	}
	return n
}

// memorySprawlNewest is how many of the newest pages the projection must
// always show, however many tags the scope has.
const memorySprawlNewest = 20

// As a scope's tags multiply, the projection still shows its newest pages
// within the cap. The tag header and the "Not shown" line both list every
// tag, so past a few hundred tags they fill the cap on their own.
func TestProjectMemoryIndexKeepsNewestPagesAsTagsSprawl(t *testing.T) {
	t.Parallel()
	for _, tags := range []int{10, 50, 100, 200, 400, 800} {
		t.Run(fmt.Sprintf("tags=%d", tags), func(t *testing.T) {
			t.Parallel()
			pages := sprawlMemoryPages(1000, tags)
			content, _, truncated := projectMemoryIndex(pages, memoryProjectionCap)
			sorted := sortedMemoryPages(pages)
			var missing []string
			for _, p := range sorted[:memorySprawlNewest] {
				if !strings.Contains(content, memoryIndexLine(p)+"\n") {
					missing = append(missing, p.Path)
				}
			}
			header := memoryTagsHeader(sorted, true)
			t.Logf("tags=%d: %d page lines shown, len=%d, header=%d bytes, truncated=%t", tags, memoryProjectedPageLines(content), len(content), len(header), truncated)
			if len(content) > memoryProjectionCap || len(missing) > 0 {
				t.Fatalf("tags=%d: len=%d (cap %d), %d page lines shown, header %d bytes; %d of the newest %d pages missing (first %q)",
					tags, len(content), memoryProjectionCap, memoryProjectedPageLines(content), len(header), len(missing), memorySprawlNewest, memoryFirstOr(missing))
			}
		})
	}
}

func memoryFirstOr(items []string) string {
	if len(items) == 0 {
		return ""
	}
	return items[0]
}

// memoryScalePageFiller pads a scale page's body to about 1.5 KB.
var memoryScalePageFiller = strings.Repeat("A sentence of page body text that stands in for a recorded fact. ", 22)

// writeMemoryScalePages writes n Markdown pages of about 1.5 KB each into
// dir, spread over ten subdirectories and twenty tags.
func writeMemoryScalePages(tb testing.TB, dir string, n int) {
	tb.Helper()
	for i := range n {
		sub := filepath.Join(dir, fmt.Sprintf("area-%d", i%10))
		if err := os.MkdirAll(sub, 0o700); err != nil {
			tb.Fatal(err)
		}
		body := fmt.Sprintf("---\ndescription: Scale page %d records how the build behaves in case %d\ntags: [topic-%02d]\nupdated: 2026-%02d-%02d\n---\n# Scale page %d\n\n%s\n",
			i, i, i%20, 1+i%12, 1+i%28, i, memoryScalePageFiller)
		if err := os.WriteFile(filepath.Join(sub, fmt.Sprintf("page-%05d.md", i)), []byte(body), 0o600); err != nil {
			tb.Fatal(err)
		}
	}
}

// writeMemoryScaleGitDir writes a .git directory of files small objects
// into dir, laid out like a repository's loose objects.
func writeMemoryScaleGitDir(tb testing.TB, dir string, files int) {
	tb.Helper()
	const perDir = 80
	for i := range files {
		sub := filepath.Join(dir, ".git", "objects", fmt.Sprintf("%02x", i/perDir))
		if i%perDir == 0 {
			if err := os.MkdirAll(sub, 0o700); err != nil {
				tb.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(sub, fmt.Sprintf("%038x", i)), []byte("blob"), 0o600); err != nil {
			tb.Fatal(err)
		}
	}
}

// memoryScaleGitFiles is the size of the .git directory the scale variants
// put inside the scope.
const memoryScaleGitFiles = 20000

// BenchmarkMemoryIndexRender times one rendering of a scope's index, the
// listing plus the projection, as a boundary's index read does it.
func BenchmarkMemoryIndexRender(b *testing.B) {
	for _, tc := range []struct {
		pages int
		git   bool
	}{{100, false}, {1000, false}, {5000, false}, {1000, true}, {5000, true}} {
		name := fmt.Sprintf("pages=%d", tc.pages)
		if tc.git {
			name += "/git=20k"
		}
		b.Run(name, func(b *testing.B) {
			root := b.TempDir()
			scope := filepath.Join(root, "memory", "personal")
			writeMemoryScalePages(b, scope, tc.pages)
			if tc.git {
				writeMemoryScaleGitDir(b, scope, memoryScaleGitFiles)
			}
			env, err := execenv.NewConfinedFileEnvironment(root, filepath.Join("memory", "personal"))
			if err != nil {
				b.Fatal(err)
			}
			defer env.Cleanup()
			b.ResetTimer()
			for b.Loop() {
				pages, err := listMemoryPages(env)
				if err != nil {
					b.Fatal(err)
				}
				if len(pages) != tc.pages {
					b.Fatalf("listed %d pages, want %d", len(pages), tc.pages)
				}
				projectMemoryIndex(pages, memoryProjectionCap)
			}
		})
	}
}

// latestMemoryProjection is the state of the last memory context the session
// projected for scope, or "" when it projected none.
func latestMemoryProjection(s *Session, scope string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := ""
	for _, turn := range s.history {
		if turn.Kind != schema.TurnMemoryContext {
			continue
		}
		if display, ok := apptranscript.ParseMemoryContext(turn.Message.Text(), turn.Message.Name); ok && display.Scope == scope {
			state = display.State
		}
	}
	return state
}

// Under the production 250 ms boundary budget, a 2,000-page scope the session
// has never seen is read in time and projected as current, never as
// unavailable. The .git variant puts a 20,000-file repository inside the
// scope; the listing walks it before filtering dot paths out.
func TestMemoryBoundaryProjectsALargeScopeWithinTheRealBudget(t *testing.T) {
	t.Parallel()
	for _, git := range []bool{false, true} {
		t.Run(fmt.Sprintf("git=%t", git), func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			scope := filepath.Join(root, "memory", "personal")
			writeMemoryScalePages(t, scope, 2000)
			if git {
				writeMemoryScaleGitDir(t, scope, memoryScaleGitFiles)
			}
			s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root, testOnly: testConfig{memoryRealBudget: true}}))
			start := time.Now()
			s.maybeAppendMemoryContext(context.Background(), true)
			elapsed := time.Since(start)
			state := latestMemoryProjection(s, "personal")
			t.Logf("git=%t: boundary took %s, personal projected %q", git, elapsed, state)
			if state != "current" {
				t.Fatalf("git=%t: personal projected %q after %s, want current within the %s budget", git, state, elapsed, memoryBoundaryBudget)
			}
		})
	}
}
