package chatcompletions

import (
	"context"
	"io"
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	"primeradiant.com/evener/llm/registry"
)

type listingRoundTripper func(*http.Request) (*http.Response, error)

func (f listingRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestListModelsDefaultIdle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, w := io.Pipe()
		defer r.Close()
		client := &http.Client{Transport: listingRoundTripper(func(req *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: r, Request: req}, nil
		})}
		done := make(chan struct{})
		defer func() { <-done }()
		go func() {
			defer close(done)
			defer w.Close()
			io.WriteString(w, `{"data":[]}`)
			time.Sleep(11 * time.Minute)
		}()
		res := registry.Resolved{Transport: registry.Transport{Auth: registry.AuthBearer}, Credential: registry.Credential{Value: "test"}}
		res.Transport.BaseURL = "https://example.invalid"
		res.Transport.ModelsEndpoint = "/models"
		start := time.Now()
		_, err := (&Protocol{Client: client}).ListModels(context.Background(), res)
		if err == nil {
			t.Fatal("stalled listing succeeded; want default idle timeout")
		}
		if elapsed := time.Since(start); elapsed != 10*time.Minute {
			t.Fatalf("elapsed %v, want 10m; err=%v", elapsed, err)
		}
	})
}
