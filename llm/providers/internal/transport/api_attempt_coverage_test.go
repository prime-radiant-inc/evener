package transport

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/apilog"
)

// TestExplicitAPIAttemptErrorClassStatusCodes covers the status code branches
// in explicitAPIAttemptErrorClass that fire when the error has no declared kind.
func TestExplicitAPIAttemptErrorClassStatusCodes(t *testing.T) {
	tests := []struct {
		status int
		want   string
	}{
		{http.StatusBadRequest, llm.KindInvalidRequest.String()},
		{http.StatusUnprocessableEntity, llm.KindInvalidRequest.String()},
		{http.StatusUnauthorized, llm.KindAuthentication.String()},
		{http.StatusForbidden, llm.KindAccessDenied.String()},
		{http.StatusNotFound, llm.KindNotFound.String()},
		{http.StatusRequestTimeout, llm.KindTimeout.String()},
		{http.StatusRequestEntityTooLarge, llm.KindContextLength.String()},
		{http.StatusTooManyRequests, llm.KindRateLimit.String()},
		{http.StatusInternalServerError, llm.KindServer.String()},
		{http.StatusBadGateway, llm.KindServer.String()},
		{http.StatusServiceUnavailable, llm.KindServer.String()},
		{http.StatusGatewayTimeout, llm.KindServer.String()},
		{99, llm.KindUnknown.String()}, // unknown status
	}
	for _, tt := range tests {
		got := explicitAPIAttemptErrorClass(apilog.AttemptProviderReject, tt.status, errors.New("plain error"))
		if got != tt.want {
			t.Errorf("status %d: got %q, want %q", tt.status, got, tt.want)
		}
	}
}

// TestExplicitAPIAttemptErrorClassTimeout covers the AttemptProviderTimeout path.
func TestExplicitAPIAttemptErrorClassTimeout(t *testing.T) {
	got := explicitAPIAttemptErrorClass(apilog.AttemptProviderTimeout, 0, errors.New("timeout"))
	if got != llm.KindTimeout.String() {
		t.Fatalf("timeout: got %q, want %q", got, llm.KindTimeout.String())
	}
}

// TestExplicitAPIAttemptErrorClassNonReject covers the non-AttemptProviderReject
// path.
func TestExplicitAPIAttemptErrorClassNonReject(t *testing.T) {
	got := explicitAPIAttemptErrorClass(apilog.AttemptTransportFail, 0, errors.New("transport fail"))
	if got != llm.KindUnknown.String() {
		t.Fatalf("transport fail: got %q, want %q", got, llm.KindUnknown.String())
	}
}

// TestMergeCredentialMaterialNil covers the nil receiver path (lines 55-56).
func TestMergeCredentialMaterialNil(t *testing.T) {
	var c *APIAttemptCapture
	c.mergeCredentialMaterial(llm.APILogCredentialMaterial{})
	// No panic.
}

// TestCompleteWithCapturedEvidenceNil covers the nil receiver path (lines 88-89).
func TestCompleteWithCapturedEvidenceNil(t *testing.T) {
	var c *APIAttemptCapture
	c.completeWithCapturedEvidence(llm.APIAttemptResult{}, llm.APIAttemptContextOwnership{}, false, llm.APITimeoutNone, nil, nil)
	// No panic.
}

// TestCompleteNowFinishedAtZero covers the FinishedAt.IsZero path (lines 139-140).
func TestCompleteNowFinishedAtZero(t *testing.T) {
	sink := &responseAssociationSink{}
	attemptCtx := attemptContext("ag_finished_zero", sink)
	request, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, "https://provider.test/v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	capture := BeginAPIAttempt(context.Background(), attemptCtx, request, attemptMeta(request, nil))
	// Complete with a result that has FinishedAt zero — completeNow should set it.
	capture.Complete(llm.APIAttemptResult{
		Outcome: apilog.AttemptSuccess,
	}, llm.APITimeoutNone, nil, nil)
	// Verify the attempt was recorded.
	recorded := onlyAttempt(t, sink)
	if recorded.Outcome != apilog.AttemptSuccess {
		t.Fatalf("outcome = %q, want %q", recorded.Outcome, apilog.AttemptSuccess)
	}
}
