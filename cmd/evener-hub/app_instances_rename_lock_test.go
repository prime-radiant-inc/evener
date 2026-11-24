package hub

import (
	"errors"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	authopenai "primeradiant.com/evener/auth/openai"
)

// gatedAuthStoreLock is the rename's store lock with the acquisition held open
// by the test: the rename parks before it touches the record, and the test sees
// the operations it performs inside the locked section.
type gatedAuthStoreLock struct {
	stateDir string
	acquired chan struct{}
	resume   chan struct{}

	mu       sync.Mutex
	saves    []string
	deletes  []string
	released bool
}

func (l *gatedAuthStoreLock) Save(name string, record authopenai.AuthRecord) error {
	l.mu.Lock()
	l.saves = append(l.saves, name)
	l.mu.Unlock()
	return authopenai.SaveAuth(l.stateDir, name, record)
}

func (l *gatedAuthStoreLock) Delete(name string) (bool, error) {
	l.mu.Lock()
	l.deletes = append(l.deletes, name)
	l.mu.Unlock()
	return authopenai.DeleteAuth(l.stateDir, name)
}

func (l *gatedAuthStoreLock) Release() {
	l.mu.Lock()
	l.released = true
	l.mu.Unlock()
}

// TestInstances_EditRenameMovesTheOAuthRecordInsideTheStoreLock pins the hub
// half of #1045: the rename copies the record to the new name and deletes the
// old one inside the store's cross-process lock, so a runtime refresh in
// another process holds its re-check and write off until the move is done and
// cannot recreate the old name afterwards. The gate is the lock's acquisition,
// so the test knows where the rename is instead of guessing with a window.
func TestInstances_EditRenameMovesTheOAuthRecordInsideTheStoreLock(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai-codex"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := authopenai.SaveAuth(f.stateDir, "work", makeOAuthRecord("work", "")); err != nil {
		t.Fatalf("SaveAuth: %v", err)
	}

	lock := &gatedAuthStoreLock{
		stateDir: f.stateDir,
		acquired: make(chan struct{}),
		resume:   make(chan struct{}),
	}
	var acquireOnce, resumeOnce sync.Once
	resume := func() { resumeOnce.Do(func() { close(lock.resume) }) }
	defer resume()
	f.ctl.auth.lockAuthStore = func(string) (authopenai.AuthStoreLock, error) {
		acquireOnce.Do(func() { close(lock.acquired) })
		<-lock.resume
		return lock, nil
	}

	renamed := make(chan error, 1)
	go func() {
		renamed <- f.ctl.Edit(appwire.InstanceEditParams{Name: "work", NewName: "personal"})
	}()

	select {
	case <-lock.acquired:
	case <-time.After(10 * time.Second):
		t.Fatal("the rename never reached the store lock")
	}
	// Parked inside the lock, before the record is touched: nothing has been
	// copied to the new name yet.
	if _, err := authopenai.LoadAuth(f.stateDir, "personal"); !errors.Is(err, authopenai.ErrAuthNotFound) {
		t.Fatalf("the record was copied before the store lock was granted (LoadAuth = %v)", err)
	}

	resume()
	select {
	case err := <-renamed:
		if err != nil {
			t.Fatalf("Edit(rename) = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the rename never finished after the store lock was granted")
	}

	lock.mu.Lock()
	saves := append([]string(nil), lock.saves...)
	deletes := append([]string(nil), lock.deletes...)
	released := lock.released
	lock.mu.Unlock()
	if len(saves) != 1 || saves[0] != "personal" {
		t.Fatalf("saves inside the lock = %v, want exactly the record under the new name", saves)
	}
	if len(deletes) != 1 || deletes[0] != "work" {
		t.Fatalf("deletes inside the lock = %v, want exactly the old name", deletes)
	}
	if !released {
		t.Fatal("the rename never released the store lock")
	}
	moved, err := authopenai.LoadAuth(f.stateDir, "personal")
	if err != nil {
		t.Fatalf("the record did not move: %v", err)
	}
	if moved.Provider != "personal" {
		t.Fatalf("Provider = %q, want the new name", moved.Provider)
	}
	if _, err := authopenai.LoadAuth(f.stateDir, "work"); !errors.Is(err, authopenai.ErrAuthNotFound) {
		t.Fatalf("the old record was left behind (LoadAuth = %v)", err)
	}
}
