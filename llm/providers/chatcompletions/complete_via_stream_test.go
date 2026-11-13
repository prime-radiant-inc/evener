package chatcompletions

import (
	"context"
	"testing"

	"primeradiant.com/evener/llm/providers/internal/providertest"
)

// TestCompleteThroughStreamWhenTheTransportRequiresIt pins the streaming
// entrypoint of Complete: a transport that only answers streams is driven
// through Stream and accumulated, with the stream flag on the wire and the
// preparer's headers applied.
func TestCompleteThroughStreamWhenTheTransportRequiresIt(t *testing.T) {
	providertest.RegisterStreamingRequiredAuth(t, "test-chat-streaming", providertest.StreamingRequiredAuth{Authorization: "Bearer chat-stream-token"})
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
