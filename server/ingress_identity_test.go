package server

import (
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

func TestAppReadTargetAtomicSnapshot(t *testing.T) {
	srv := NewServer(ServerConfig{})
	old, err := PrepareAppIdentityForRef("local", "old", "local:stable", "")
	if err != nil {
		t.Fatal(err)
	}
	srv.ReplaceAppIdentity(old, nil)
	params := appwire.ThreadReadParams{Ref: "local:stable", Subscribe: true}
	// Exercise the actual shared resolver with a caller-held write lock: this
	// deterministically verifies it requires no nested lock-taking helpers.
	srv.mu.Lock()
	result := make(chan [2]string, 1)
	go func() { thread, target := srv.appReadTargetLocked(params); result <- [2]string{thread, target} }()
	var before [2]string
	select {
	case before = <-result:
		srv.mu.Unlock()
	case <-time.After(5 * time.Second):
		srv.mu.Unlock()
		t.Fatal("locked resolver attempted nested locking")
	}
	if before != [2]string{"old", "local:stable"} {
		t.Fatalf("locked snapshot=%q", before)
	}
	next, err := PrepareAppIdentityForRef("local", "new", "local:stable", "")
	if err != nil {
		t.Fatal(err)
	}
	srv.ReplaceAppIdentity(next, nil)
	thread, target := srv.appReadTarget(params)
	if thread != "new" || target != before[1] {
		t.Fatalf("same-owner snapshot=(%q,%q), previous=%q", thread, target, before)
	}
	// The saved ingress target is a value: replacement cannot reinterpret it as
	// a stale bare thread ID. This is what the former two-call composition lost.
	if before[1] != "local:stable" {
		t.Fatalf("snapshot owner changed=%q", before[1])
	}
}

func TestAppReadTargetPreservesResolutionSemantics(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		params                 appwire.ThreadReadParams
		root, status, ref      string
		child                  bool
		wantThread, wantTarget string
	}{
		{name: "stable", params: appwire.ThreadReadParams{Ref: "local:stable"}, root: "current", ref: "local:stable", wantThread: "current", wantTarget: "local:stable"},
		{name: "explicit id precedes invalid ref", params: appwire.ThreadReadParams{ThreadID: "current", Ref: "invalid"}, root: "current", ref: "local:stable", wantThread: "current", wantTarget: "local:stable"},
		{name: "explicit child precedes root ref", params: appwire.ThreadReadParams{ThreadID: "child", Ref: "local:stable"}, root: "current", ref: "local:stable", child: true, wantThread: "child", wantTarget: "child"},
		{name: "child ref", params: appwire.ThreadReadParams{Ref: "local:child"}, root: "current", ref: "local:stable", child: true, wantThread: "child", wantTarget: "child"},
		{name: "missing child", params: appwire.ThreadReadParams{ThreadID: "child"}, root: "current", ref: "local:stable"},
		{name: "invalid ref", params: appwire.ThreadReadParams{Ref: "invalid"}, root: "current", ref: "local:stable"},
		{name: "foreign ref", params: appwire.ThreadReadParams{Ref: "remote:current"}, root: "current", ref: "local:stable"},
		{name: "implicit root", root: "current", ref: "local:stable", wantThread: "current", wantTarget: "local:stable"},
		{name: "status fallback", status: "status-root", wantThread: "status-root", wantTarget: "status-root"},
		{name: "explicit status fallback", params: appwire.ThreadReadParams{ThreadID: "status-root"}, status: "status-root", ref: "local:stable", wantThread: "status-root", wantTarget: "status-root"},
		{name: "empty root"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := NewServer(ServerConfig{})
			srv.mu.Lock()
			srv.appThreadID, srv.appRef, srv.status.SessionID = tc.root, tc.ref, tc.status
			if tc.child {
				srv.appDescendants["child"] = &appDescendantProjection{}
			}
			srv.mu.Unlock()
			thread, target := srv.appReadTarget(tc.params)
			if thread != tc.wantThread || target != tc.wantTarget {
				t.Fatalf("target=(%q,%q), want (%q,%q)", thread, target, tc.wantThread, tc.wantTarget)
			}
			if got := srv.appThreadIDForRead(tc.params); got != thread {
				t.Fatalf("read resolver diverged=%q, want %q", got, thread)
			}
		})
	}
}
