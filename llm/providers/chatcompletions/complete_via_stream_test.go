package chatcompletions

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// chatStreamingPreparer stands in for a transport that answers only streams
// (the Codex-style RequiresStreamingComplete contract).
type chatStreamingPreparer struct{}

func (chatStreamingPreparer) Apply(_ context.Context, req *http.Request, _ registry.Resolved) error {
	req.Header.Set("Authorization", "Bearer chat-stream-token")
	return nil
}
func (chatStreamingPreparer) PrepareRequest(context.Context, *http.Request, map[string]any, llm.Request, registry.Resolved) error {
	return nil
}
func (chatStreamingPreparer) RequiresStreamingComplete() bool { return true }

var registerChatStreamingOnce sync.Once

// TestCompleteThroughStreamWhenTheTransportRequiresIt pins the streaming
// entrypoint of Complete: a transport that only answers streams is driven
// through Stream and accumulated, with the stream flag on the wire and the
// preparer's headers applied.
func TestCompleteThroughStreamWhenTheTransportRequiresIt(t *testing.T) {
	registerChatStreamingOnce.Do(func() { llm.RegisterAuthenticator("test-chat-streaming", chatStreamingPreparer{}) })
	srv, got := server(t, 200, chatSSE)
	res := liveRes(srv, nil)
	res.Transport.Auth = "test-chat-streaming"
	resp, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), userReq("hi"), res)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text() != "hello" || len(resp.ToolCalls()) != 1 {
		t.Fatalf("complete via stream = %+v", resp)
	}
	if got.body["stream"] != true {
		t.Fatalf("wire body = %v, want the stream flag", got.body)
	}
	if got.header.Get("Authorization") != "Bearer chat-stream-token" {
		t.Fatalf("wire headers = %v, want the preparer's auth header", got.header)
	}
}
