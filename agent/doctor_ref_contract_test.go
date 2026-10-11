package agent

import (
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/doctor"
	"primeradiant.com/evener/identifier"
)

// The doctor's transcript refs are hand-off handles to the agent-side
// transcript tools: whatever non-empty TranscriptRef a doctor.Locate result
// carries must parse with this package's ref grammar (decodeRef /
// validIDToken) and name a project token the transcript tools' filesystem
// boundary accepts (identifier.ValidateProjectID). A bucket whose directory
// name cannot satisfy that grammar — legacy pure-hash names, colon- or
// backslash-bearing names — gets NO ref at all, never a ref the tools would
// reject. Those sessions stay fully locatable (session id, bucket, paths)
// and remain addressable through the doctor's own selector grammar, bare id
// or explicit proj: ref.
func TestDoctorEmittedRefsParseWithAgentRefGrammar(t *testing.T) {
	t.Parallel()
	stateHome := newStateHome(t)
	// "a\b", kept in a variable so no string literal containing a path
	// separator is handed to filepath.Join (gocritic filepathJoin).
	backslashBucket := `a\b`
	cases := []struct {
		name    string
		bucket  string
		wantRef bool
	}{
		// A well-formed Project.ID — the class every agent ref consumer admits.
		{name: "normal", bucket: newBucketUnder(t, stateHome), wantRef: true},
		// Legacy pure-hash bucket dir: fails ValidateProjectID, so no ref.
		{name: "legacy-hex", bucket: filepath.Join(stateHome, "evener", "projects", "0123456789abcdef")},
		// Colons parse nowhere in the shared grammar (validIDToken rejects them).
		{name: "colon-named", bucket: filepath.Join(stateHome, "evener", "projects", "a:b")},
		// Backslash is a path separator on Windows; the doctor selector grammar
		// rejects it, so it is never promised as a ref either.
		{name: "backslash-named", bucket: filepath.Join(stateHome, "evener", "projects", backslashBucket)},
	}

	for _, tc := range cases {
		sid := writeDoctorFixtureSession(t, tc.bucket)
		paths, err := doctor.Locate(stateHome, sid)
		if err != nil {
			t.Fatalf("%s: bare-id Locate into %q: %v", tc.name, tc.bucket, err)
		}
		if !tc.wantRef {
			if paths.TranscriptRef != "" {
				t.Errorf("%s: bucket name %q is not consumable by the agent ref grammar; want no emitted ref, got %q",
					tc.name, filepath.Base(tc.bucket), paths.TranscriptRef)
			}
			continue
		}
		projectID, sessionID, err := decodeRef(paths.TranscriptRef)
		if err != nil {
			t.Fatalf("%s: emitted ref %q does not parse with the agent ref grammar: %v", tc.name, paths.TranscriptRef, err)
		}
		if err := identifier.ValidateProjectID(projectID); err != nil {
			t.Fatalf("%s: emitted ref names project %q, which the transcript tools reject: %v", tc.name, projectID, err)
		}
		if projectID != filepath.Base(tc.bucket) || sessionID != sid {
			t.Errorf("%s: ref = %q/%q, want project %q session %q", tc.name, projectID, sessionID, filepath.Base(tc.bucket), sid)
		}
	}
}
