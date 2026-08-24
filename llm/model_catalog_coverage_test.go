package llm

import (
	"testing"
)

// TestModelCatalogResolveAliasNil covers the nil catalog path (lines 95-97).
func TestModelCatalogResolveAliasNil(t *testing.T) {
	var c *ModelCatalog
	target, ambiguous := c.ResolveAlias("alias")
	if target != nil || ambiguous {
		t.Fatal("nil catalog should return nil, false")
	}
}

// TestModelCatalogResolveAliasExactID covers the exact-ID-is-not-alias path
// (lines 99-102).
func TestModelCatalogResolveAliasExactID(t *testing.T) {
	c := &ModelCatalog{
		Models: []ModelInfo{
			{ID: "exact-model"},
		},
	}
	target, ambiguous := c.ResolveAlias("exact-model")
	if target != nil || ambiguous {
		t.Fatalf("exact ID should return nil, false, got %v, %t", target, ambiguous)
	}
}

// TestModelCatalogResolveAliasAmbiguous covers the ambiguous alias path
// (lines 109-112).
func TestModelCatalogResolveAliasAmbiguous(t *testing.T) {
	c := &ModelCatalog{
		Models: []ModelInfo{
			{ID: "model-a", Aliases: []string{"shared-alias"}},
			{ID: "model-b", Aliases: []string{"shared-alias"}},
		},
	}
	target, ambiguous := c.ResolveAlias("shared-alias")
	if target != nil || !ambiguous {
		t.Fatalf("ambiguous alias should return nil, true, got %v, %t", target, ambiguous)
	}
}

// TestClassifyAPIAttemptOutcomeTransportFail covers the transport error path
// in ClassifyAPIAttemptOutcome (line 87-88).
func TestClassifyAPIAttemptOutcomeTransportFail(t *testing.T) {
	got := ClassifyAPIAttemptOutcome(APIAttemptContextOwnership{}, 0, simpleError("transport fail"), nil, nil)
	if got != "transport_failure" {
		t.Fatalf("got %q, want transport_failure", got)
	}
}

// TestAttemptOwnedTimeoutResponseHeader covers the ResponseHeader path (line 97).
func TestAttemptOwnedTimeoutResponseHeader(t *testing.T) {
	if !attemptOwnedTimeout(APIAttemptContextOwnership{TimeoutSource: APITimeoutResponseHeader}, nil, simpleError("transport fail")) {
		t.Fatal("ResponseHeader with transport error should be owned")
	}
	if attemptOwnedTimeout(APIAttemptContextOwnership{TimeoutSource: APITimeoutResponseHeader}, nil, nil) {
		t.Fatal("ResponseHeader without transport error should not be owned")
	}
}

// TestAttemptOwnedTimeoutSSERead covers the SSERead path (line 98-99).
func TestAttemptOwnedTimeoutSSERead(t *testing.T) {
	if !attemptOwnedTimeout(APIAttemptContextOwnership{TimeoutSource: APITimeoutSSERead}, simpleError("decode fail"), nil) {
		t.Fatal("SSERead with decode error should be owned")
	}
	if !attemptOwnedTimeout(APIAttemptContextOwnership{TimeoutSource: APITimeoutSSERead}, nil, simpleError("transport fail")) {
		t.Fatal("SSERead with transport error should be owned")
	}
}

type simpleError string

func (e simpleError) Error() string { return string(e) }
