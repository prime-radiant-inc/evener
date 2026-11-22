package llm

import (
	"context"
	"testing"
)

// TestStampContinuationIDHash_ContextOverridesFallbackAndSkipsEmpty pins the
// shared stamping helper every protocol adapter calls: the dispatching
// client's context hasher beats the process-wide fallback, the fallback stands
// in when the context carries none, a response with no Raw map gets one, and
// an absent response, id, or hasher stamps nothing at all.
func TestStampContinuationIDHash_ContextOverridesFallbackAndSkipsEmpty(t *testing.T) {
	t.Parallel()
	ctxHasher := NewContinuationHasher([]byte("context-secret"))
	fallbackHasher := NewContinuationHasher([]byte("fallback-secret"))
	ctxHash, err := ctxHasher.HashContinuationHandle("response_id", "resp_ctx")
	if err != nil {
		t.Fatalf("HashContinuationHandle: %v", err)
	}
	fallbackHash, err := fallbackHasher.HashContinuationHandle("response_id", "resp_fallback")
	if err != nil {
		t.Fatalf("HashContinuationHandle: %v", err)
	}

	t.Run("context hasher wins over fallback", func(t *testing.T) {
		ctx := ContextWithContinuationHasher(context.Background(), ctxHasher)
		resp := Response{ID: "resp_ctx"}
		StampContinuationIDHash(ctx, &resp, fallbackHasher)
		if resp.Raw["id_hash"] != ctxHash {
			t.Fatalf("id_hash = %v, want the context hasher's %q", resp.Raw["id_hash"], ctxHash)
		}
	})

	t.Run("fallback stamps when the context has none", func(t *testing.T) {
		resp := Response{ID: "resp_fallback"}
		StampContinuationIDHash(context.Background(), &resp, fallbackHasher)
		if resp.Raw["id_hash"] != fallbackHash {
			t.Fatalf("id_hash = %v, want the fallback hasher's %q", resp.Raw["id_hash"], fallbackHash)
		}
	})

	t.Run("nil Raw is initialized before stamping", func(t *testing.T) {
		resp := Response{ID: "resp_fallback"} // Raw intentionally nil
		StampContinuationIDHash(context.Background(), &resp, fallbackHasher)
		if resp.Raw == nil {
			t.Fatal("a nil Raw map must be initialized before stamping")
		}
		if resp.Raw["id_hash"] != fallbackHash {
			t.Fatalf("id_hash = %v, want %q", resp.Raw["id_hash"], fallbackHash)
		}
	})

	t.Run("nothing to stamp", func(t *testing.T) {
		cases := []struct {
			name     string
			resp     *Response
			fallback *ContinuationHasher
		}{
			{"nil response", nil, fallbackHasher},
			{"empty id", &Response{}, fallbackHasher},
			{"no hasher anywhere", &Response{ID: "resp_ctx"}, nil},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				StampContinuationIDHash(context.Background(), tc.resp, tc.fallback)
				if tc.resp != nil {
					if _, stamped := tc.resp.Raw["id_hash"]; stamped {
						t.Fatalf("nothing to hash with, nothing stamped: %v", tc.resp.Raw)
					}
				}
			})
		}
	})
}
