package hub

import (
	"errors"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providercfg"
)

// TestHasResolvedCredentialHeaderEmpty covers the empty-headers path.
func TestHasResolvedCredentialHeaderEmpty(t *testing.T) {
	if hasResolvedCredentialHeader(nil) {
		t.Fatalf("hasResolvedCredentialHeader(nil) should be false")
	}
	if hasResolvedCredentialHeader(map[string]string{}) {
		t.Fatalf("hasResolvedCredentialHeader(empty) should be false")
	}
}

// TestHasResolvedCredentialHeaderWithResolved covers the positive path.
func TestHasResolvedCredentialHeaderWithResolved(t *testing.T) {
	headers := map[string]string{
		"X-API-Key": "literal-key",
	}
	if !hasResolvedCredentialHeader(headers) {
		t.Fatalf("hasResolvedCredentialHeader with literal key should be true")
	}
}

// TestHasResolvedCredentialHeaderWithEmptyValue covers the empty-value path.
func TestHasResolvedCredentialHeaderWithEmptyValue(t *testing.T) {
	headers := map[string]string{
		"X-API-Key": "  ",
	}
	if hasResolvedCredentialHeader(headers) {
		t.Fatalf("hasResolvedCredentialHeader with whitespace-only value should be false")
	}
}

// TestHasResolvedAuthorizationHeaderNoMatch covers the no-Authorization-header path.
func TestHasResolvedAuthorizationHeaderNoMatch(t *testing.T) {
	headers := map[string]string{
		"X-Custom": "value",
	}
	if hasResolvedAuthorizationHeader(headers) {
		t.Fatalf("hasResolvedAuthorizationHeader without Authorization should be false")
	}
}

// TestHasResolvedAuthorizationHeaderWithMatch covers the positive path.
func TestHasResolvedAuthorizationHeaderWithMatch(t *testing.T) {
	headers := map[string]string{
		"Authorization": "Bearer token",
	}
	if !hasResolvedAuthorizationHeader(headers) {
		t.Fatalf("hasResolvedAuthorizationHeader with Bearer token should be true")
	}
}

// TestHasResolvedAuthorizationHeaderCaseInsensitive covers the case-insensitive matching.
func TestHasResolvedAuthorizationHeaderCaseInsensitive(t *testing.T) {
	headers := map[string]string{
		"authorization": "Bearer token",
	}
	if !hasResolvedAuthorizationHeader(headers) {
		t.Fatalf("hasResolvedAuthorizationHeader with lowercase authorization should be true")
	}
}

// TestHasResolvedAuthorizationHeaderEmptyValue covers the empty-value path.
func TestHasResolvedAuthorizationHeaderEmptyValue(t *testing.T) {
	headers := map[string]string{
		"Authorization": "  ",
	}
	if hasResolvedAuthorizationHeader(headers) {
		t.Fatalf("hasResolvedAuthorizationHeader with empty value should be false")
	}
}

// TestClassifyCredentialTestErrorUnsupported covers the "does not support listing models" path.
func TestClassifyCredentialTestErrorUnsupported(t *testing.T) {
	err := &llm.ConfigurationError{Message: "provider does not support listing models"}
	status, msg := classifyCredentialTestError(err)
	if status != appwire.AuthTestStatusUnsupported {
		t.Fatalf("status = %q, want %q", status, appwire.AuthTestStatusUnsupported)
	}
	if msg != credentialTestUnsupportedMessage {
		t.Fatalf("msg = %q, want %q", msg, credentialTestUnsupportedMessage)
	}
}

// TestClassifyCredentialTestErrorConfig covers the generic ConfigurationError path.
func TestClassifyCredentialTestErrorConfig(t *testing.T) {
	err := &llm.ConfigurationError{Message: "bad config"}
	status, _ := classifyCredentialTestError(err)
	if status != appwire.AuthTestStatusConfigurationFailure {
		t.Fatalf("status = %q, want %q", status, appwire.AuthTestStatusConfigurationFailure)
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

// TestCredentialRequired covers the credentialRequired function.
func TestCredentialRequired(t *testing.T) {
	// An instance that requires no credential.
	noCred := providercfg.InstanceConfig{Type: "ollama"}
	if credentialRequired(noCred) {
		t.Fatalf("credentialRequired for no-credential type should be false")
	}
	// An instance with a BaseURL for openai-compatible.
	compat := providercfg.InstanceConfig{Type: "openai-compatible", APIStyle: "chat-completions", BaseURL: "http://localhost:8080"}
	if credentialRequired(compat) {
		t.Fatalf("credentialRequired for openai-compatible with BaseURL should be false")
	}
	// An openai instance without BaseURL requires a credential.
	openai := providercfg.InstanceConfig{Type: "openai"}
	if !credentialRequired(openai) {
		t.Fatalf("credentialRequired for openai should be true")
	}
}

// TestCredentialTestResponse covers the credentialTestResponse constructor.
func TestCredentialTestResponse(t *testing.T) {
	resp := credentialTestResponse("my-provider", appwire.AuthTestStatusSuccess, "ok")
	if resp.Provider != "my-provider" || resp.Status != appwire.AuthTestStatusSuccess || resp.Message != "ok" {
		t.Fatalf("credentialTestResponse = %+v", resp)
	}
}

// TestConfiguredInstanceMissing covers the not-found path.
func TestConfiguredInstanceMissing(t *testing.T) {
	cfg := providercfg.Config{Instances: []providercfg.InstanceConfig{
		{Name: "alpha"},
	}}
	_, ok := configuredInstance(cfg, "beta")
	if ok {
		t.Fatalf("configuredInstance for missing name should return false")
	}
}

// TestConfiguredInstanceFound covers the found path.
func TestConfiguredInstanceFound(t *testing.T) {
	cfg := providercfg.Config{Instances: []providercfg.InstanceConfig{
		{Name: "alpha"},
		{Name: "beta"},
	}}
	inst, ok := configuredInstance(cfg, "beta")
	if !ok || inst.Name != "beta" {
		t.Fatalf("configuredInstance for beta = %+v, %v", inst, ok)
	}
}
