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
