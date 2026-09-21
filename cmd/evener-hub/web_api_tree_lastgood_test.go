package hub

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// uncomparableAppSource is a registered source whose dynamic type has no
// comparable identity: a struct carrying a slice. Comparing two interface
// values that hold it panics, and a Source is free to be a value type like
// this one.
type uncomparableAppSource struct {
	*scriptedAppSource
	tags []string
}

// With no generation store the retention fence trusts the source instance, and
// a registered source whose dynamic type cannot be compared must not turn that
// trust into a panic on a tree read: the fence reports "not the same
// instance", which drops the stale retention instead of publishing rows under
// an identity it cannot verify. Pointer sources — every one this hub registers
// itself — keep comparing by address.
func TestLastGoodRegistrationOwnedHandlesAnUncomparableSource(t *testing.T) {
	reg := appsource.NewRegistry()
	value := uncomparableAppSource{
		scriptedAppSource: &scriptedAppSource{id: "alpha"},
		tags:              []string{"one"},
	}
	reg.Add(value)
	web := &WebServer{sources: reg}

	if web.lastGoodRegistrationOwned(value, 0, false) {
		t.Fatal("an uncomparable source compared as its own instance")
	}

	ptr := &scriptedAppSource{id: "beta"}
	reg.Add(ptr)
	if !web.lastGoodRegistrationOwned(ptr, 0, false) {
		t.Fatal("a registered pointer source did not compare as its own instance")
	}
	if web.lastGoodRegistrationOwned(&scriptedAppSource{id: "beta"}, 0, false) {
		t.Fatal("another instance of the registered pointer source compared as it")
	}
}

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
		id:   "remote",
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

func TestListThreadsWithFallbackInitialCallErrorsEmpty(t *testing.T) {
	s := &WebServer{cfg: hubcore.WebConfig{}}
	lister := &stubThreadLister{id: "remote", err: errors.New("down")}
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
