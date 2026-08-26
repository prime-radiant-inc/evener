package agent

import (
	"errors"
	"testing"

	"primeradiant.com/evener/agent/internal/jobstore"
)

func TestLifecyclePublicationOrdersAppendForwardLiveBeforeEven(t *testing.T) {
	steps := make(chan string, 8)
	jm := &jobManager{
		beginActivityPublication: func() (*activityPublication, error) {
			steps <- "odd"
			return &activityPublication{RootID: "root", Revision: 1}, nil
		},
		commitActivityPublication: func(*activityPublication) error { steps <- "even"; return nil },
		abortActivityPublication:  func(*activityPublication) { steps <- "abort" },
	}
	e := jobstore.Event{Kind: jobstore.EventJobStarted}
	if err := jm.publishLifecycleEvent(&e, func() error { steps <- "append"; return nil }, func() error { steps <- "copy"; return nil }, func() error { steps <- "live"; return nil }); err != nil {
		t.Fatal(err)
	}
	want := []string{"odd", "append", "copy", "live", "even"}
	for _, expected := range want {
		if got := <-steps; got != expected {
			t.Fatalf("step = %q, want %q", got, expected)
		}
	}
}

func TestLifecyclePublicationKeepsAppendFailureInsideTransaction(t *testing.T) {
	steps := make(chan string, 8)
	wantErr := errors.New("append")
	jm := &jobManager{
		beginActivityPublication:  func() (*activityPublication, error) { steps <- "odd"; return &activityPublication{}, nil },
		commitActivityPublication: func(*activityPublication) error { steps <- "even"; return nil },
		abortActivityPublication:  func(*activityPublication) { steps <- "abort" },
	}
	if err := jm.publishLifecycleEvent(&jobstore.Event{}, func() error { steps <- "append"; return wantErr }, func() error { steps <- "copy"; return nil }, func() error { steps <- "live"; return nil }); !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	for _, expected := range []string{"odd", "append", "abort"} {
		if got := <-steps; got != expected {
			t.Fatalf("step = %q, want %q", got, expected)
		}
	}
}
