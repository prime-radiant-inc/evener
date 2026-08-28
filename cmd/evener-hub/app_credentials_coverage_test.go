package hub

import (
	"errors"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// TestClassifyCredentialTestErrorConfig covers the ConfigurationError path.
// The registry answers "this endpoint has no model list" through
// ModelListing.Live instead, so a configuration error is never anything but a
// configuration failure here.
func TestClassifyCredentialTestErrorConfig(t *testing.T) {
	for _, message := range []string{"bad config", "provider does not support listing models"} {
		status, msg := classifyCredentialTestError(&llm.ConfigurationError{Message: message})
		if status != appwire.AuthTestStatusConfigurationFailure {
			t.Fatalf("status = %q for %q, want %q", status, message, appwire.AuthTestStatusConfigurationFailure)
		}
		if msg != credentialTestConfigurationMessage {
			t.Fatalf("msg = %q, want the fixed configuration message", msg)
		}
	}
}

// TestClassifyCredentialTestErrorAuthRejected covers the 401 status path via
// string matching.
func TestClassifyCredentialTestErrorAuthRejected(t *testing.T) {
	err := errors.New("HTTP 401: unauthorized")
	status, _ := classifyCredentialTestError(err)
	if status != appwire.AuthTestStatusAuthRejected {
		t.Fatalf("status = %q, want %q", status, appwire.AuthTestStatusAuthRejected)
	}
}

// TestClassifyCredentialTestErrorAuthRejected403 covers the 403 status path.
func TestClassifyCredentialTestErrorAuthRejected403(t *testing.T) {
	err := errors.New("status=403: forbidden")
	status, _ := classifyCredentialTestError(err)
	if status != appwire.AuthTestStatusAuthRejected {
		t.Fatalf("status = %q, want %q", status, appwire.AuthTestStatusAuthRejected)
	}
}

// TestClassifyCredentialTestErrorEndpointFailure covers the default path.
func TestClassifyCredentialTestErrorEndpointFailure(t *testing.T) {
	err := errors.New("HTTP 500: server error")
	status, _ := classifyCredentialTestError(err)
	if status != appwire.AuthTestStatusEndpointFailure {
		t.Fatalf("status = %q, want %q", status, appwire.AuthTestStatusEndpointFailure)
	}
}

// TestCredentialTestResponse covers the credentialTestResponse constructor.
func TestCredentialTestResponse(t *testing.T) {
	resp := credentialTestResponse("my-provider", appwire.AuthTestStatusSuccess, "ok")
	if resp.Provider != "my-provider" || resp.Status != appwire.AuthTestStatusSuccess || resp.Message != "ok" {
		t.Fatalf("credentialTestResponse = %+v", resp)
	}
}
