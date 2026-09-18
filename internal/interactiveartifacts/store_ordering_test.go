package interactiveartifacts

import (
	"context"
	"crypto/sha256"
	"sync/atomic"
	"testing"
	"time"
)

func TestStoreExpiryAtCommitRollsBackReceipt(t *testing.T) {
	var expires atomic.Bool
	s, hash, _ := setupStore(t, StoreOptions{Clock: func() time.Time {
		if expires.Load() {
			return testScope().ExpiresAt
		}
		return fixedClock()
	}})
	ctx := context.Background()
	created, err := s.Publish(ctx, hash, createJSON("create"), PublicationOrigin{})
	requireNoError(t, err)
	s.hooks.beforeCommit = func() { expires.Store(true) }
	request := saveJSON(created.ArtifactID, "save", 1, 1, `{"n":1}`)
	_, err = s.SaveState(ctx, hash, request)
	requireCode(t, err, NotFoundOrForbidden)
	expires.Store(false)
	s.hooks = storeHooks{}
	if got := readState(t, s, hash, created.ArtifactID); got.StateVersion != 1 {
		t.Fatal("expired mutation committed")
	}
	result, err := s.SaveState(ctx, hash, request)
	requireNoError(t, err)
	if result.StateVersion != 2 {
		t.Fatal("expiry left a false receipt")
	}
}

func awaitSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(10 * time.Second):
		t.Fatal("operation barrier not reached")
	}
}

func TestStoreRevocationOrdersBeforePendingWrite(t *testing.T) {
	s, hash, _ := setupStore(t, StoreOptions{})
	ctx := context.Background()
	created, err := s.Publish(ctx, hash, createJSON("create"), PublicationOrigin{})
	requireNoError(t, err)
	entered, release := make(chan struct{}), make(chan struct{})
	var paused atomic.Bool
	s.hooks.beforeAdmission = func() {
		if paused.CompareAndSwap(false, true) {
			close(entered)
			<-release
		}
	}
	request := saveJSON(created.ArtifactID, "save", 1, 1, `{"n":1}`)
	done := make(chan error, 1)
	go func() { _, err := s.SaveState(ctx, hash, request); done <- err }()
	awaitSignal(t, entered)
	requireNoError(t, s.RevokeGrant(ctx, hash))
	close(release)
	requireCode(t, <-done, NotFoundOrForbidden)
	renewed := sha256.Sum256([]byte("renewed after revoke"))
	requireNoError(t, s.InstallGrant(ctx, renewed, testScope()))
	if got := readState(t, s, renewed, created.ArtifactID); got.StateVersion != 1 {
		t.Fatal("write crossed acknowledged revocation")
	}
	saved, err := s.SaveState(ctx, renewed, request)
	requireNoError(t, err)
	if saved.StateVersion != 2 {
		t.Fatal("denied write left receipt")
	}
	requireNoError(t, s.RevokeGrant(ctx, renewed))
	_, err = s.SaveState(ctx, renewed, request)
	requireCode(t, err, NotFoundOrForbidden)
	observer := sha256.Sum256([]byte("observer"))
	requireNoError(t, s.InstallGrant(ctx, observer, testScope()))
	if got := readState(t, s, observer, created.ArtifactID); got.StateVersion != 2 {
		t.Fatal("revoke undid committed data")
	}
}

func TestStoreConcurrentPublicationCheckpointOrderings(t *testing.T) {
	for _, publishWins := range []bool{true, false} {
		t.Run(map[bool]string{true: "publication wins", false: "checkpoint wins"}[publishWins], func(t *testing.T) {
			s, hash, _ := setupStore(t, StoreOptions{})
			ctx := context.Background()
			created, err := s.Publish(ctx, hash, createJSON("create"), PublicationOrigin{})
			requireNoError(t, err)
			second := sha256.Sum256([]byte("second client"))
			requireNoError(t, s.InstallGrant(ctx, second, testScope()))
			entered, release := make(chan struct{}), make(chan struct{})
			var paused atomic.Bool
			s.hooks.beforeAdmission = func() {
				if paused.CompareAndSwap(false, true) {
					close(entered)
					<-release
				}
			}
			done := make(chan error, 1)
			go func() {
				var err error
				if publishWins {
					_, err = s.SaveState(ctx, hash, saveJSON(created.ArtifactID, "save", 1, 1, `{"n":1}`))
				} else {
					_, err = s.Publish(ctx, hash, publishJSON(created.ArtifactID, "publish", 1, 1), PublicationOrigin{})
				}
				done <- err
			}()
			awaitSignal(t, entered)
			if publishWins {
				_, err = s.Publish(ctx, second, publishJSON(created.ArtifactID, "publish", 1, 1), PublicationOrigin{})
			} else {
				_, err = s.SaveState(ctx, second, saveJSON(created.ArtifactID, "save", 1, 1, `{"n":1}`))
			}
			requireNoError(t, err)
			close(release)
			if publishWins {
				requireCode(t, <-done, SourceConflict)
			} else {
				requireCode(t, <-done, StateConflict)
			}
			got := readState(t, s, hash, created.ArtifactID)
			if publishWins {
				if got.SourceRevision != 2 || got.StateVersion != 1 || string(got.State) != "{}" {
					t.Fatalf("losing checkpoint applied %+v", got)
				}
			} else {
				if got.SourceRevision != 1 || got.StateVersion != 2 || string(got.State) != `{"n":1}` {
					t.Fatalf("losing publication applied %+v", got)
				}
			}
		})
	}
}

func TestStoreTombstoneOrdersBeforePendingCreation(t *testing.T) {
	s, hash, path := setupStore(t, StoreOptions{})
	ctx := context.Background()
	entered, release := make(chan struct{}), make(chan struct{})
	var paused atomic.Bool
	s.hooks.beforeAdmission = func() {
		if paused.CompareAndSwap(false, true) {
			close(entered)
			<-release
		}
	}
	done := make(chan error, 1)
	go func() { _, err := s.Publish(ctx, hash, createJSON("queued"), PublicationOrigin{}); done <- err }()
	awaitSignal(t, entered)
	requireNoError(t, s.TombstoneNamespace(ctx, "namespace", "realm", "owner"))
	close(release)
	requireCode(t, <-done, NotFoundOrForbidden)
	requireNoError(t, s.Close())
	s = openTestStore(t, path, StoreOptions{Clock: fixedClock})
	requireCode(t, s.InstallGrant(ctx, hash, testScope()), NotFoundOrForbidden)
	var count int
	requireNoError(t, s.db.QueryRowContext(ctx, "SELECT count(*) FROM artifact_mutations").Scan(&count))
	if count != 0 {
		t.Fatal("pending creation crossed namespace tombstone")
	}
}
