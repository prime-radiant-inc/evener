package responses

import (
	"context"
	"testing"

	"primeradiant.com/evener/llm"
)

// TestStampsResponseIDHashFromContext proves the dispatching client's hasher
// reaches the protocol through the context rather than through the
// process-wide Protocol.Hasher field, which stays nil here.
func TestStampsResponseIDHashFromContext(t *testing.T) {
	hasher := llm.NewContinuationHasher([]byte("task-2-secret"))
	want, err := hasher.HashContinuationHandle("response_id", "resp_1")
	if err != nil {
		t.Fatalf("HashContinuationHandle: %v", err)
	}

	t.Run("complete", func(t *testing.T) {
		srv, _ := server(t, 200, responseJSON)
		p := &Protocol{Client: srv.Client()}
		ctx := llm.ContextWithContinuationHasher(context.Background(), hasher)
		resp, err := p.Complete(ctx, userReq("hi"), liveRes(srv, openaiCaps))
		if err != nil {
			t.Fatal(err)
		}
		if resp.Raw["id_hash"] != want {
			t.Fatalf("id_hash = %v, want %q", resp.Raw["id_hash"], want)
		}
	})

	t.Run("stream", func(t *testing.T) {
		srv, _ := server(t, 200, responseSSE)
		p := &Protocol{Client: srv.Client()}
		ctx := llm.ContextWithContinuationHasher(context.Background(), hasher)
		s, err := p.Stream(ctx, userReq("hi"), liveRes(srv, openaiCaps))
		if err != nil {
			t.Fatal(err)
		}
		var final *llm.Response
		for ev := range s.Events() {
			switch ev.Type {
			case llm.StreamEventFinish:
				final = ev.Response
			case llm.StreamEventError:
				t.Fatalf("stream error: %v", ev.Err)
			}
		}
		if final == nil || final.Raw["id_hash"] != want {
			t.Fatalf("final = %+v, want id_hash %q", final, want)
		}
	})

	t.Run("no hasher at all", func(t *testing.T) {
		srv, _ := server(t, 200, responseJSON)
		resp, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), userReq("hi"), liveRes(srv, openaiCaps))
		if err != nil {
			t.Fatal(err)
		}
		if _, stamped := resp.Raw["id_hash"]; stamped {
			t.Fatalf("nothing to hash with, nothing stamped: %v", resp.Raw)
		}
	})
}
