package main

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
)

type stubThreadLister struct {
	id    string
	resp  appwire.ThreadListResponse
	err   error
	calls int
}

func (s *stubThreadLister) ID() string { return s.id }

func (s *stubThreadLister) ListThreads(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	s.calls++
	return s.resp, s.err
}

func threadIDs(threads []appwire.Thread) []string {
	out := make([]string, 0, len(threads))
	for _, t := range threads {
		out = append(out, t.ID)
	}
	return out
}

func TestListThreadsWithFallbackRetainsLastGood(t *testing.T) {
	s := &WebServer{cfg: hubcore.WebConfig{}}
	lister := &stubThreadLister{
		id:   "codex",
		resp: appwire.ThreadListResponse{Data: []appwire.Thread{{ID: "th_1"}, {ID: "th_2"}}},
	}

	// First call succeeds and is cached.
	got := s.listThreadsWithFallback(context.Background(), lister)
	if want := []string{"th_1", "th_2"}; !equalStrings(threadIDs(got), want) {
		t.Fatalf("first list = %v, want %v", threadIDs(got), want)
	}

	// A transient error must not blank the source — last-known-good is retained.
	lister.resp = appwire.ThreadListResponse{}
	lister.err = errors.New("dial timeout")
	got = s.listThreadsWithFallback(context.Background(), lister)
	if want := []string{"th_1", "th_2"}; !equalStrings(threadIDs(got), want) {
		t.Fatalf("error list = %v, want retained %v", threadIDs(got), want)
	}

	// A successful empty list does clear the cache — a genuinely-gone source
	// ages out rather than lingering forever.
	lister.err = nil
	lister.resp = appwire.ThreadListResponse{Data: nil}
	got = s.listThreadsWithFallback(context.Background(), lister)
	if len(got) != 0 {
		t.Fatalf("empty success list = %v, want empty", threadIDs(got))
	}

	// And after clearing, an error returns empty (nothing to retain).
	lister.err = errors.New("dial timeout")
	got = s.listThreadsWithFallback(context.Background(), lister)
	if len(got) != 0 {
		t.Fatalf("error after clear = %v, want empty", threadIDs(got))
	}
}

func TestListThreadsWithFallbackFirstCallErrorsEmpty(t *testing.T) {
	s := &WebServer{cfg: hubcore.WebConfig{}}
	lister := &stubThreadLister{id: "codex", err: errors.New("down")}
	got := s.listThreadsWithFallback(context.Background(), lister)
	if len(got) != 0 {
		t.Fatalf("first-call error = %v, want empty", threadIDs(got))
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
