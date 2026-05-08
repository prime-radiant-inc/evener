package openai

import "testing"

func TestParseRedirectURLExtractsCodeAndState(t *testing.T) {
	code, state, err := ParseRedirectURL("http://127.0.0.1:1455/callback?code=auth-code&state=expected-state")
	if err != nil {
		t.Fatalf("ParseRedirectURL() error = %v", err)
	}

	if code != "auth-code" {
		t.Fatalf("code = %q, want %q", code, "auth-code")
	}
	if state != "expected-state" {
		t.Fatalf("state = %q, want %q", state, "expected-state")
	}
}

func TestParseRedirectURLRejectsMissingCode(t *testing.T) {
	_, _, err := ParseRedirectURL("http://127.0.0.1:1455/callback?state=expected-state")
	if err == nil {
		t.Fatal("ParseRedirectURL() error = nil, want missing code error")
	}
}

func TestParseRedirectURLRejectsInvalidURLShape(t *testing.T) {
	_, _, err := ParseRedirectURL("http://%")
	if err == nil {
		t.Fatal("ParseRedirectURL() error = nil, want invalid URL error")
	}
}

func TestValidateStateAcceptsMatchingState(t *testing.T) {
	if err := ValidateState("expected-state", "expected-state"); err != nil {
		t.Fatalf("ValidateState() error = %v", err)
	}
}

func TestValidateStateRejectsMismatchedState(t *testing.T) {
	if err := ValidateState("expected-state", "other-state"); err == nil {
		t.Fatal("ValidateState() error = nil, want mismatch error")
	}
}
