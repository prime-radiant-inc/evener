package hubcore

import (
	"sync"
	"testing"
)

// TestResumeLocksForSharesOneMutexPerSession pins that every caller resuming
// one session serializes on the same mutex, and different sessions do not.
func TestResumeLocksForSharesOneMutexPerSession(t *testing.T) {
	r := NewResumeLocks()
	s1 := r.For("s1")
	if s1 == nil {
		t.Fatal("For(s1) returned nil")
	}
	if again := r.For("s1"); again != s1 {
		t.Fatal("For(s1) returned a different mutex on a second call")
	}
	if s2 := r.For("s2"); s2 == s1 {
		t.Fatal("For(s2) returned the same mutex as For(s1)")
	}
	s1.Lock()
	if again := r.For("s1"); again != s1 {
		t.Fatal("For(s1) returned a different mutex while the first was held")
	}
	s1.Unlock()
}

// TestResumeLocksForConcurrentCallersShareOneMutex pins that concurrent first
// calls for one session still agree on a single mutex.
func TestResumeLocksForConcurrentCallersShareOneMutex(t *testing.T) {
	r := NewResumeLocks()
	start := make(chan struct{})
	results := make(chan *ResumeMutex, 20)
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			<-start
			results <- r.For("shared")
		})
	}
	close(start)
	wg.Wait()
	close(results)
	want := r.For("shared")
	for got := range results {
		if got != want {
			t.Fatalf("For(shared) returned distinct mutexes: %p and %p", want, got)
		}
	}
}
