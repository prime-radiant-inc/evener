package openai

import (
	"errors"
	"testing"
)

func TestParseRedirectURLRejectsUnparseableInput(t *testing.T) {
	// A raw control character makes url.Parse fail outright (before the
	// scheme/host emptiness checks).
	_, _, err := ParseRedirectURL("http://example.com/\x7f?code=c")
	if !errors.Is(err, ErrInvalidRedirectURL) {
		t.Fatalf("ParseRedirectURL() error = %v, want ErrInvalidRedirectURL", err)
	}
}

func TestValidateStateRejectsEmptyReturnedState(t *testing.T) {
	if err := ValidateState("expected", ""); !errors.Is(err, ErrStateMismatch) {
		t.Fatalf("ValidateState(expected, \"\") = %v, want ErrStateMismatch", err)
	}
	if err := ValidateState("", "returned"); !errors.Is(err, ErrStateMismatch) {
		t.Fatalf("ValidateState(\"\", returned) = %v, want ErrStateMismatch", err)
	}
}
