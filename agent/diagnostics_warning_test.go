package agent

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

// A warning built from a failure says what failed and why (#3386): the label,
// then the error's text on one line, bounded without splitting a character.
func TestWarningDataFromErrorCarriesItsCause(t *testing.T) {
	t.Parallel()
	got := warningDataFromError("inspect delegate attention", errors.New("open delegates.jsonl: permission denied"))
	if got.Message != "inspect delegate attention: open delegates.jsonl: permission denied" {
		t.Fatalf("message = %q", got.Message)
	}
	joined := warningDataFromError("launch delegate attention", errors.Join(errors.New("launch failed"), errors.New("finish failed")))
	if joined.Message != "launch delegate attention: launch failed; finish failed" {
		t.Fatalf("joined message = %q", joined.Message)
	}
	if bare := warningDataFromError("  restore delegate attention ", nil); bare.Message != "restore delegate attention" {
		t.Fatalf("nil-error message = %q", bare.Message)
	}
	if blank := warningDataFromError("restore delegate attention", errors.New("  ")); blank.Message != "restore delegate attention" {
		t.Fatalf("blank-error message = %q", blank.Message)
	}
	long := warningDataFromError("label", errors.New(strings.Repeat("€", 400)))
	if !utf8.ValidString(long.Message) || len(long.Message) > len("label: ")+warningCauseLimit {
		t.Fatalf("long cause: %d bytes, valid=%v", len(long.Message), utf8.ValidString(long.Message))
	}
}

// A provider's error can echo the user's request, so the retry warnings that
// carry one stay bare: the label, with the classification in Title and Hint.
func TestBareWarningDataFromErrorKeepsTheLabel(t *testing.T) {
	t.Parallel()
	if got := bareWarningDataFromError("Context length exceeded", errors.New("400: messages[3].content was too long: …user text…")); got.Message != "Context length exceeded" {
		t.Fatalf("bare message = %q", got.Message)
	}
}
