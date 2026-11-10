package llm

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestRequestSentObserverFiresOnlyWhenTheAdapterIsCalled pins the observer's
// meaning: it reports a request handed to the provider, and nothing that stops
// short of that (middleware refusing it, a stopped context) reports one.
func TestRequestSentObserverFiresOnlyWhenTheAdapterIsCalled(t *testing.T) {
	t.Parallel()
	refusal := errors.New("refused before the provider")
	for _, tc := range []struct {
		name     string
		refuse   bool
		stopped  bool
		wantSent bool
	}{
		{name: "sent", wantSent: true},
		{name: "refused by middleware", refuse: true},
		{name: "stopped before dispatch", stopped: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := NewClient()
			c.Register(&fakeAdapter{name: "openai"})
			if tc.refuse {
				c.Use(MiddlewareFunc{Complete: func(context.Context, Request, CompleteFunc) (Response, error) {
					return Response{}, refusal
				}})
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if tc.stopped {
				cancel()
			}
			sent := 0
			ctx = WithRequestSentObserver(ctx, func() { sent++ })
			_, _ = c.Complete(ctx, Request{Model: "m", Messages: []Message{User("hi")}})
			if got := sent > 0; got != tc.wantSent {
				t.Fatalf("request reported sent = %t, want %t", got, tc.wantSent)
			}
		})
	}
}
