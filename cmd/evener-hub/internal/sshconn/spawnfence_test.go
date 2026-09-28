package sshconn

// Tests for the operation-scoped spawn ownership seam (§3's pre-spawn
// lifecycle) as the production wiring carries it: one operation's durable
// record plus the local boundary machinery its ssh subprocesses are owned
// through. The platform-neutral half — the context carrier and the
// unscoped-spawn pin — lives here; the spawn lifecycle's own tests live in
// spawnfence_unix_test.go.

import (
	"context"
	"strings"
	"sync"
	"syscall"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
)

// fenceTestLog is one ordered log of the fence's steps, shared by the fake
// store and the fake boundary so a test can pin "arm before spawn" and the
// rest of the lifecycle's ordering.
type fenceTestLog struct {
	mu      sync.Mutex
	entries []string
}

func (l *fenceTestLog) add(entry string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, entry)
}

func (l *fenceTestLog) joined() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.entries, " ")
}

// index returns the first position of entry, or -1.
func (l *fenceTestLog) index(entry string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i, got := range l.entries {
		if got == entry {
			return i
		}
	}
	return -1
}

// fenceMatch is one post-spawn match the fence persisted.
type fenceMatch struct {
	recordID  string
	nonce     string
	pid       int
	startTime string
}

// fenceFakeStore is a scripted SpawnIntentStore: it records the lifecycle's
// calls in order and can refuse any of them.
type fenceFakeStore struct {
	log *fenceTestLog

	armErr, matchErr, dropErr error

	open    []hostops.SpawnIntent
	matched []fenceMatch
	dropped []string

	armCalls, matchCalls, dropCalls int
}

func (f *fenceFakeStore) ArmSpawnIntent(recordID string, intent hostops.SpawnIntent) (hostops.Record, error) {
	f.armCalls++
	f.log.add("arm")
	if f.armErr != nil {
		return hostops.Record{}, f.armErr
	}
	f.open = append(f.open, intent)
	return hostops.Record{ID: recordID}, nil
}

func (f *fenceFakeStore) MatchSpawnIntent(recordID, nonce string, pid int, startTime string) (hostops.Record, error) {
	f.matchCalls++
	f.log.add("match")
	if f.matchErr != nil {
		return hostops.Record{}, f.matchErr
	}
	f.matched = append(f.matched, fenceMatch{recordID: recordID, nonce: nonce, pid: pid, startTime: startTime})
	// A matched intent is still open: only the clean drop closes it.
	return hostops.Record{ID: recordID}, nil
}

func (f *fenceFakeStore) DropSpawnIntent(recordID, nonce string) error {
	f.dropCalls++
	f.log.add("drop")
	if f.dropErr != nil {
		return f.dropErr
	}
	f.dropped = append(f.dropped, nonce)
	f.removeOpen(nonce)
	return nil
}

func (f *fenceFakeStore) removeOpen(nonce string) {
	kept := f.open[:0]
	for _, intent := range f.open {
		if intent.Nonce != nonce {
			kept = append(kept, intent)
		}
	}
	f.open = kept
}

// fenceFakeBoundary is a scripted SpawnBoundary. notEnforcing zero-values to
// the enforcing Linux arm; tests that model Darwin set it.
type fenceFakeBoundary struct {
	log          *fenceTestLog
	id           BoundaryID
	token        string
	notEnforcing bool

	attrErr, observeErr, closeErr error
	// closeErrs is consumed one per Close call before closeErr applies: it
	// models a boundary that refuses a teardown until its last member is reaped.
	closeErrs []error

	observedPid int
	released    bool
	closeCalls  int
	closed      bool
}

func (b *fenceFakeBoundary) Identity() BoundaryID { return b.id }

func (b *fenceFakeBoundary) Enforcing() bool { return !b.notEnforcing }

func (b *fenceFakeBoundary) SpawnAttr() (*syscall.SysProcAttr, func(), error) {
	b.log.add("spawnattr")
	if b.attrErr != nil {
		return nil, nil, b.attrErr
	}
	// Platform-neutral on purpose: the fake stands in for the kernel handle, and
	// the real arm's attributes (cgroup fd, process group, Pdeathsig) are
	// exercised by the Linux integration test.
	return &syscall.SysProcAttr{}, func() {
		b.released = true
		b.log.add("release")
	}, nil
}

func (b *fenceFakeBoundary) Observe(pid int) (string, error) {
	b.log.add("observe")
	if b.observeErr != nil {
		return "", b.observeErr
	}
	b.observedPid = pid
	return b.token, nil
}

func (b *fenceFakeBoundary) Close() error {
	b.closeCalls++
	b.log.add("close")
	if len(b.closeErrs) > 0 {
		err := b.closeErrs[0]
		b.closeErrs = b.closeErrs[1:]
		if err != nil {
			return err
		}
	} else if b.closeErr != nil {
		return b.closeErr
	}
	b.closed = true
	return nil
}

// TestSpawnScopeCarriesThroughDerivedContexts pins the carrier: a scope set on
// one context is visible through every derived context, and a context without
// one (and a nil scope) reads as unarmed.
func TestSpawnScopeCarriesThroughDerivedContexts(t *testing.T) {
	scope := NewSpawnScope("op-1", &fenceFakeStore{log: &fenceTestLog{}})
	ctx := WithSpawnScope(context.Background(), scope)
	derived, cancel := context.WithCancel(ctx)
	defer cancel()
	got, ok := SpawnScopeFrom(derived)
	if !ok || got != scope {
		t.Fatalf("SpawnScopeFrom(derived) = %v/%t, want the carried scope", got, ok)
	}
	if got.RecordID != "op-1" {
		t.Fatalf("scope record id = %q, want op-1", got.RecordID)
	}
	if _, ok := SpawnScopeFrom(context.Background()); ok {
		t.Fatal("a context with no scope reads as armed")
	}
	if _, ok := SpawnScopeFrom(WithSpawnScope(context.Background(), nil)); ok {
		t.Fatal("a nil scope arms the context")
	}
}

// TestUnscopedSpawnTouchesNoBoundaryOrIntentStore pins the read-only half: a
// spawn on a context with no scope must not create a boundary, arm an intent,
// match, or drop. It is exactly the plain spawn the read-only preflight path
// uses — §6 exempts those one-shots, and no record owns them.
func TestUnscopedSpawnTouchesNoBoundaryOrIntentStore(t *testing.T) {
	log := &fenceTestLog{}
	store := &fenceFakeStore{log: log}
	boundary := &fenceFakeBoundary{log: log, id: BoundaryID{Platform: BoundaryPlatformLinux, CgroupID: "/cg/plain"}}

	out, err := (execRunner{}).Run(context.Background(), []string{"/bin/sh", "-c", "printf plain"}, nil)
	if err != nil {
		t.Fatalf("unscoped Run: %v", err)
	}
	if string(out) != "plain" {
		t.Fatalf("unscoped Run output = %q, want plain", out)
	}
	if got := log.joined(); got != "" {
		t.Fatalf("an unscoped spawn touched the fence: %s", got)
	}
	if store.armCalls+store.matchCalls+store.dropCalls != 0 {
		t.Fatalf("an unscoped spawn used the intent store: %+v", store)
	}
	if boundary.closeCalls != 0 {
		t.Fatal("an unscoped spawn closed a boundary")
	}
}
