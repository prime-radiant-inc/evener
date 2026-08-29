package responses

import (
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func TestRequestFingerprintIsStableAcrossBuildsAndStreaming(t *testing.T) {
	res := resolved(openaiCaps)
	req := userReq("hi")
	req.PreviousResponseID = "resp_a"
	a := build(t, req, res)
	b, _ := buildBody(llm.ShapeRequest(req, res), res, true)
	req.PreviousResponseID = "resp_b"
	c := build(t, req, res)
	fa, err := RequestFingerprint(EndpointFamily(res), a)
	if err != nil || !strings.HasPrefix(fa, "cont-req-v1:") {
		t.Fatalf("fingerprint = %q err = %v", fa, err)
	}
	fb, _ := RequestFingerprint(EndpointFamily(res), b)
	fc, _ := RequestFingerprint(EndpointFamily(res), c)
	if fa != fb || fa != fc {
		t.Fatalf("stream and previous_response_id must not change the fingerprint: %s %s %s", fa, fb, fc)
	}
	other := userReq("bye")
	other.Temperature = new(0.5)
	fo, _ := RequestFingerprint(EndpointFamily(res), build(t, other, res))
	if fo == fa {
		t.Fatal("a different body must fingerprint differently")
	}
	codex := resolved(codexLiteCaps)
	codex.Transport.Auth = registry.AuthOAuthOpenAICodex
	if EndpointFamily(codex) != llm.ResponsesEndpointFamilyOpenAICodex || EndpointFamily(res) != llm.ResponsesEndpointFamilyOpenAIPublic {
		t.Fatal("endpoint family follows the transport's auth scheme")
	}
}
