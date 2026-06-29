package hubapi

import "testing"

// FuzzParseRef drives the real hubapi.ParseRef seam over arbitrary strings. The
// oracle is floor "no panic" plus a parse→format→parse fixed point: any ref that
// parses cleanly must re-serialize to a non-empty String() that re-parses to an
// identical Ref. This exercises the ref regexp, the host:session split, and the
// path-traversal guard against their inverse (String).
func FuzzParseRef(f *testing.F) {
	f.Add("local:01ABCDEF")
	f.Add("host-1:session.id_2~3")
	f.Add("local:")
	f.Add(":session")
	f.Add("local:..")
	f.Add("local:a..b")
	f.Add("no-colon")
	f.Add("")
	f.Add("a:b:c")
	f.Add("local:a/b")

	f.Fuzz(func(t *testing.T, raw string) {
		ref, err := ParseRef(raw)
		if err != nil {
			return // rejected input
		}

		formatted := ref.String()
		if formatted == "" {
			t.Fatalf("ParseRef accepted %q but String() returned empty (host=%q session=%q)",
				raw, ref.HostID, ref.SessionID)
		}

		reparsed, err := ParseRef(formatted)
		if err != nil {
			t.Fatalf("re-parsing formatted ref failed: %v\n raw=%q\n formatted=%q", err, raw, formatted)
		}
		if reparsed != ref {
			t.Fatalf("ref not stable through String():\n raw=%q\n once=%#v\n twice=%#v", raw, ref, reparsed)
		}
	})
}
