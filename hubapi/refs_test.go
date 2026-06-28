package hubapi_test

import (
	"testing"

	"primeradiant.com/serf/hubapi"
)

func TestLocalRefRoundTrip(t *testing.T) {
	ref := hubapi.LocalRef("01ABC")
	if ref.String() != "local:01ABC" {
		t.Fatalf("ref=%q", ref.String())
	}
	parsed, err := hubapi.ParseRef(ref.String())
	if err != nil {
		t.Fatal(err)
	}
	if parsed.HostID != "local" || parsed.SessionID != "01ABC" {
		t.Fatalf("parsed=%+v", parsed)
	}
}

func TestParseRefRejectsUnsafePathText(t *testing.T) {
	// "local:..x" passes the regex (no slash, no space) but must be rejected by
	// the explicit ".." guard in ParseRef — the regex alone cannot close this gap.
	for _, raw := range []string{"", "local/", "local:../x", "local:with space", "local:..x"} {
		if _, err := hubapi.ParseRef(raw); err == nil {
			t.Fatalf("ParseRef(%q) succeeded", raw)
		}
	}
}

func TestRefPathEscaped(t *testing.T) {
	// All characters in "01ABC" are URL-path-safe, so this is a documentation
	// test that the result is stable when no encoding is needed.
	ref := hubapi.LocalRef("01ABC")
	if got := ref.PathEscaped(); got != "local:01ABC" {
		t.Fatalf("PathEscaped=%q", got)
	}

	// A session ID containing a space (constructed directly, bypassing ParseRef)
	// requires encoding. This distinguishes PathEscaped from a bare r.String().
	ref2 := hubapi.Ref{HostID: "local", SessionID: "session id"}
	if got := ref2.PathEscaped(); got != "local:session%20id" {
		t.Fatalf("PathEscaped=%q", got)
	}
}
