package openai

import (
	"context"
	"sync"
)

// AuthStoreLock is a held exclusive cross-process lock over the OAuth records
// under one state directory. A caller that must keep one record operation
// atomic with another — an instance rename writes the record under its new name
// and deletes it under the old one — holds it across both; SaveAuth and
// DeleteAuth take it themselves.
//
// The lock is an flock on the auth directory, so it is per open file
// description: every acquire, in this process or another, contends with every
// other. It is not re-entrant, and the save and delete paths below take it, so
// a holder must use Save and Delete here rather than SaveAuth and DeleteAuth.
type AuthStoreLock interface {
	// Save writes record as instanceName's auth file. The auth directory is
	// already open (taking the lock opened it), so it need not exist-check.
	Save(instanceName string, record AuthRecord) error
	// Delete removes instanceName's auth file, reporting whether one was
	// deleted.
	Delete(instanceName string) (bool, error)
	// Release drops the lock. Call it exactly once.
	Release()
}

// LockAuthStore takes the store's cross-process lock and returns it held. The
// wait honors ctx, so a caller with a deadline returns instead of parking
// behind another process; every holder in this package holds it only across
// one or two file writes, never across a network call.
func LockAuthStore(ctx context.Context, stateDir string) (AuthStoreLock, error) {
	release, err := acquireAuthStoreLock(ctx, stateDir)
	if err != nil {
		return nil, err
	}
	return &authStoreLock{stateDir: stateDir, release: release}, nil
}

type authStoreLock struct {
	stateDir string
	release  func()
}

func (l *authStoreLock) Save(instanceName string, record AuthRecord) error {
	return saveAuthRecordLocked(l.stateDir, instanceName, record)
}

func (l *authStoreLock) Delete(instanceName string) (bool, error) {
	return deleteAuthRecordLocked(l.stateDir, instanceName)
}

func (l *authStoreLock) Release() {
	if l.release == nil {
		return
	}
	l.release()
	l.release = nil
}

// withAuthStoreLock runs fn holding the store lock.
func withAuthStoreLock(ctx context.Context, stateDir string, fn func() error) error {
	lock, err := LockAuthStore(ctx, stateDir)
	if err != nil {
		return err
	}
	defer lock.Release()
	return fn()
}

// authStoreLockWaiting, when non-nil, is called the first time one acquisition
// finds the lock held, before it waits. A test uses it to learn that a writer
// has reached the wait instead of guessing with a window; production leaves it
// nil.
var (
	authStoreLockWaitingMu sync.Mutex
	authStoreLockWaiting   func()
)

// setAuthStoreLockWaiting installs fn as the contention seam and returns a
// function that restores the previous value. Tests pair it with t.Cleanup;
// production never calls it. The seam is synchronized because a test can set it
// while a goroutine another test started is acquiring the lock.
func setAuthStoreLockWaiting(fn func()) func() {
	authStoreLockWaitingMu.Lock()
	prev := authStoreLockWaiting
	authStoreLockWaiting = fn
	authStoreLockWaitingMu.Unlock()
	return func() {
		authStoreLockWaitingMu.Lock()
		authStoreLockWaiting = prev
		authStoreLockWaitingMu.Unlock()
	}
}

// installedAuthStoreLockWaiting returns the contention seam in effect for one
// acquisition. Taking it once keeps a single call's notifications on the
// function the caller started with.
func installedAuthStoreLockWaiting() func() {
	authStoreLockWaitingMu.Lock()
	defer authStoreLockWaitingMu.Unlock()
	return authStoreLockWaiting
}
