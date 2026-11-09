package hubcore

import (
	"path/filepath"
	"testing"
)

func TestHubIdentityPersistsAcrossReopen(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "index.db")
	id, err := NewHubIdentityStore(path).HubID()
	if err != nil || id == "" {
		t.Fatalf("HubID = %q, %v", id, err)
	}
	got, err := NewHubIdentityStore(path).HubID()
	if err != nil || got != id {
		t.Fatalf("reopened ID = %q, %v, want %q", got, err, id)
	}
}

func TestHubIdentityConcurrentFirstOpen(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "index.db")
	type result struct {
		id  string
		err error
	}
	results := make(chan result, 8)
	start := make(chan struct{})
	for range 8 {
		go func() {
			<-start
			id, err := NewHubIdentityStore(path).HubID()
			results <- result{id, err}
		}()
	}
	close(start)
	var want string
	for range 8 {
		got := <-results
		if got.err != nil || got.id == "" {
			t.Fatalf("HubID = %q, %v", got.id, got.err)
		}
		if want == "" {
			want = got.id
		}
		if got.id != want {
			t.Fatalf("concurrent HubID = %q, want %q", got.id, want)
		}
	}
}
