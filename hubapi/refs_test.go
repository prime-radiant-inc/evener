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
	for _, raw := range []string{"", "local/", "local:../x", "local:with space"} {
		if _, err := hubapi.ParseRef(raw); err == nil {
			t.Fatalf("ParseRef(%q) succeeded", raw)
		}
	}
}

func TestRefPathEscaped(t *testing.T) {
	ref := hubapi.LocalRef("01ABC")
	if got := ref.PathEscaped(); got != "local:01ABC" {
		t.Fatalf("PathEscaped=%q", got)
	}
}
