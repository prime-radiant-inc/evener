package responses

import (
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// TestRequestFingerprintIsStableAcrossCompleteAndStream is spec §13's
// continuation row against the real builder: the body a Complete turn sends
// and the body a Stream turn sends fingerprint identically on both endpoint
// families, however the anchor and the streaming flag differ. A field the
// builder emits only when streaming would silently split every session's
// anchor from its continuation, so it is pinned here rather than on a
// hand-written body.
func TestRequestFingerprintIsStableAcrossCompleteAndStream(t *testing.T) {
	cases := []struct {
		name   string
		caps   func(*registry.Caps)
		auth   string
		family llm.ResponsesEndpointFamily
	}{
		{"public", openaiCaps, registry.AuthBearer, llm.ResponsesEndpointFamilyOpenAIPublic},
		{"codex", codexLiteCaps, registry.AuthOAuthOpenAICodex, llm.ResponsesEndpointFamilyOpenAICodex},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := resolved(tc.caps)
			res.Transport.Auth = tc.auth
			family := llm.ResponsesEndpointFamilyFor(res)
			if family != tc.family {
				t.Fatalf("endpoint family follows the transport's auth scheme: %s", family)
			}
			req := userReq("hi")
			req.PreviousResponseID = "resp_a"
			buffered, err := buildBody(llm.ShapeRequest(req, res), res, false)
			if err != nil {
				t.Fatalf("complete body: %v", err)
			}
			req.PreviousResponseID = "resp_b"
			streamed, err := buildBody(llm.ShapeRequest(req, res), res, true)
			if err != nil {
				t.Fatalf("stream body: %v", err)
			}
			if streamed["stream"] != true || buffered["stream"] != nil {
				t.Fatalf("only the streaming body carries stream: %v %v", buffered["stream"], streamed["stream"])
			}
			complete, err := llm.ResponsesRequestFingerprint(family, buffered)
			if err != nil {
				t.Fatal(err)
			}
			stream, err := llm.ResponsesRequestFingerprint(family, streamed)
			if err != nil {
				t.Fatal(err)
			}
			if complete != stream {
				t.Fatalf("stream and previous_response_id must not change the fingerprint: %s vs %s", complete, stream)
			}
			other := userReq("bye")
			other.Temperature = new(0.5)
			differs, err := llm.ResponsesRequestFingerprint(family, build(t, other, res))
			if err != nil {
				t.Fatal(err)
			}
			if differs == complete {
				t.Fatal("a different body must fingerprint differently")
			}
		})
	}
}
