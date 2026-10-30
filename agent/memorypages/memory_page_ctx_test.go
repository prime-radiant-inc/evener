package memorypages

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
)

// TestListMemoryPagesCtxPropagatesCancellation is M4: a cancelled page read must
// return the cancellation, not a degraded filename-only index the
// unreadable-page fallback fabricated while the loop carried on to success.
func TestListMemoryPagesCtxPropagatesCancellation(t *testing.T) {
	t.Parallel()
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	defer env.Cleanup()
	if err := os.WriteFile(filepath.Join(env.RootDir, "page.md"), []byte("# page\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pages, err := ListPagesCtx(ctx, env)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ListPagesCtx(cancelled) = %d pages, %v; want context.Canceled", len(pages), err)
	}
}

// TestMemoryPageFromReadDoesNotFabricateAPageForCancellation pins the fallback
// itself: a context error is not an unreadable page, so no filename-only entry
// is fabricated from it.
func TestMemoryPageFromReadDoesNotFabricateAPageForCancellation(t *testing.T) {
	t.Parallel()
	if _, ok := PageFromRead("page.md", nil, context.Canceled, time.Time{}); ok {
		t.Fatal("PageFromRead fabricated a page entry for a context error")
	}
	if _, ok := PageFromRead("page.md", nil, context.DeadlineExceeded, time.Time{}); ok {
		t.Fatal("PageFromRead fabricated a page entry for a deadline error")
	}
}
