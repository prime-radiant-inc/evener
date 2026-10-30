package hub

import "testing"

// A session id never contains ':', so a ref with more than one colon names no
// session and must not parse into a host plus a colon-bearing session id.
func TestNavigationRefRejectsColonInSessionID(t *testing.T) {
	for _, id := range []string{"local:a:b", "host:a:b:c"} {
		if ref, err := navigationRef(id); err == nil {
			t.Fatalf("navigationRef(%q) = %+v, want error", id, ref)
		}
	}
}

func TestNavigationRefAcceptsBareAndQualifiedIDs(t *testing.T) {
	for id, want := range map[string]string{"abc": "local:abc", "local:abc": "local:abc", "host-1:abc": "host-1:abc"} {
		ref, err := navigationRef(id)
		if err != nil || ref.String() != want {
			t.Fatalf("navigationRef(%q) = %q, %v; want %q", id, ref.String(), err, want)
		}
	}
}
