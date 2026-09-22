package hub

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/synctest"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/identifier"
)

// TestSessionDeleteCanceledWhileOwnershipHeldReportsError is the Low
// regression: a single-session delete whose context is canceled while it waits
// for another holder's alias lock must report the cancellation, not answer
// success-with-skip. The fresh project-delete path already fails on ctx.Err();
// the single-session path must agree.
func TestSessionDeleteCanceledWhileOwnershipHeldReportsError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		root := t.TempDir()
		projectDir := filepath.Join(root, "project")
		if err := os.MkdirAll(projectDir, 0o755); err != nil {
			t.Fatal(err)
		}
		stateDir := filepath.Join(root, "projects", "session-delete-0123456789")
		project, err := identifier.ResolveProject(projectDir)
		if err != nil {
			t.Fatal(err)
		}
		writeSession(t, stateDir, webTestSessionID, project.CanonicalPath)
		past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
		if _, err := past.Rebuild(); err != nil {
			t.Fatal(err)
		}
		locks := hubcore.NewResumeLocks()
		web := NewWebServer(hubcore.WebConfig{
			StateDir: root, Past: past, Roster: hubcore.NewRosterWithEntries(), ResumeLocks: locks,
		})

		blocked := locks.For(webTestSessionID)
		blocked.Lock()
		release := sync.OnceFunc(blocked.Unlock)
		defer release()

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		type result struct {
			response appwire.SessionDeleteResponse
			err      error
		}
		completed := make(chan result, 1)
		go func() {
			response, err := web.sessionDelete(ctx, appwire.SessionDeleteParams{Ref: "local:" + webTestSessionID})
			completed <- result{response, err}
		}()
		synctest.Wait()
		select {
		case got := <-completed:
			t.Fatalf("canceled session delete returned before cancellation: %+v", got)
		default:
		}

		cancel()
		synctest.Wait()
		select {
		case got := <-completed:
			if got.err == nil {
				t.Errorf("canceled session delete reported success-with-skip: %+v", got.response)
			}
		default:
			t.Error("canceled session delete still waits for ownership")
		}
		if _, err := os.Stat(filepath.Join(stateDir, "sessions", webTestSessionID+".meta.json")); err != nil {
			t.Fatalf("canceled session delete removed saved data: %v", err)
		}
	})
}

// TestSessionDeleteCanceledBeforeNoPastScrubReportsError pins the no-past
// cancellation gap: a canceled request for a session already absent from the
// past index must fail, not still scrub the session's archive/favorite
// decisions before answering success.
func TestSessionDeleteCanceledBeforeNoPastScrubReportsError(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "index.db")
	past := hubcore.NewPastIndexWithDB(filepath.Join(root, "projects", "*"), dbPath)
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	archive := hubcore.NewArchiveStore(dbPath)
	favorite := hubcore.NewFavoriteStore(dbPath)
	seedProjectDeleteDecisions(t, archive, favorite, "no-project", webTestSessionID)
	web := NewWebServer(hubcore.WebConfig{
		StateDir: root, Past: past, Archive: archive, Favorite: favorite, Roster: hubcore.NewRosterWithEntries(),
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := web.sessionDelete(ctx, appwire.SessionDeleteParams{Ref: "local:" + webTestSessionID})
	if err == nil {
		t.Fatal("canceled no-past session delete reported success")
	}
	assertArchiveDecisionPresent(t, archive, "session", webTestSessionID, true)
}

// cancelAfterAcquireContext models a request canceled in the window between an
// acquired deletion-ownership reservation and the destructive cleanup it
// authorizes: the acquisition's own post-acquire check still sees an uncanceled
// request, and the next check sees it canceled.
type cancelAfterAcquireContext struct {
	context.Context
	done   chan struct{}
	closed bool
	checks int
}

func (c *cancelAfterAcquireContext) Done() <-chan struct{} { return c.done }

func (c *cancelAfterAcquireContext) Err() error {
	c.checks++
	if c.checks > 1 {
		return context.Canceled
	}
	if !c.closed {
		c.closed = true
		close(c.done)
	}
	return nil
}

// TestSessionDeleteCanceledAfterOwnershipReportsError pins the post-acquisition
// cancellation gap: once ownership is held, a canceled request must fail before
// the destructive cleanup rather than remove artifacts for a request that was
// abandoned before it could decide. Mirrors the fresh project-delete path's
// ctx.Err() check after acquireProjectDeletionCandidates.
func TestSessionDeleteCanceledAfterOwnershipReportsError(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "projects", "session-delete-0123456789")
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	writeSession(t, stateDir, webTestSessionID, project.CanonicalPath)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	// Roster is nil so ownership acquisition observes the request context only
	// through the alias reservation, leaving the post-acquire check as the next
	// observer.
	web := NewWebServer(hubcore.WebConfig{StateDir: root, Past: past, ResumeLocks: hubcore.NewResumeLocks()})
	ctx := &cancelAfterAcquireContext{Context: t.Context(), done: make(chan struct{})}

	_, err = web.sessionDelete(ctx, appwire.SessionDeleteParams{Ref: "local:" + webTestSessionID})
	if err == nil {
		t.Fatal("canceled post-acquisition session delete reported success")
	}
	if _, statErr := os.Stat(filepath.Join(stateDir, "sessions", webTestSessionID+".meta.json")); statErr != nil {
		t.Fatalf("canceled delete removed saved data: %v", statErr)
	}
}
