// Package hubtest provides fixture helpers shared by tests across
// cmd/serf-hub and its internal packages.
//
// It exists because the identifiers a hub fixture has to spell out are
// encodings, not free-form names, and the failure mode when one is wrong is
// silence: PastIndex.Rebuild refuses to index a project directory or session
// meta whose id fails validation, so a fixture seeded with a plausible-looking
// placeholder is invisible to every reader rather than rejected out loud. A
// session id is a 22-character base62 UUIDv7 payload; a project directory is
// <readable>-<10 base62>. Mint them here instead of writing them by hand.
package hubtest

import (
	"strings"
	"testing"

	"primeradiant.com/serf/identifier"
)

// projectIDSuffix is the fixed 10-character base62 tail every minted fixture
// project id carries. Real project ids derive the suffix from the canonical
// path so distinct checkouts stay distinct; a fixture's uniqueness comes from
// its readable portion instead, which the caller chooses.
const projectIDSuffix = "0123456789"

// SessionID returns a fresh session id that passes
// identifier.ValidateSessionID, for naming a seeded session meta.
func SessionID(t *testing.T) string {
	t.Helper()
	id, err := identifier.NewSessionID()
	if err != nil {
		t.Fatalf("hubtest.SessionID: mint session id: %v", err)
	}
	return id
}

// ProjectID returns a project id built from readable that passes
// identifier.ValidateProjectID, for naming a synthetic project directory under
// a fixture's projects root. Characters readable cannot carry are folded to
// hyphens; an empty result falls back to "project".
//
// Use this only for a project directory that stands alone. When the fixture has
// a real checkout on disk, use identifier.ResolveProject instead — the hub
// cross-checks a project's id against its working directory, and only the
// resolved id matches.
func ProjectID(t *testing.T, readable string) string {
	t.Helper()
	id := readableProjectPortion(readable) + "-" + projectIDSuffix
	if err := identifier.ValidateProjectID(id); err != nil {
		t.Fatalf("hubtest.ProjectID(%q) built invalid id %q: %v", readable, id, err)
	}
	return id
}

// readableProjectPortion reduces readable to the ASCII alphanumeric-and-hyphen
// alphabet a project id's readable portion allows, collapsing runs of rejected
// bytes into a single hyphen and keeping the tail that fits inside the id's
// 80-byte ceiling.
func readableProjectPortion(readable string) string {
	// 80 total, less the hyphen and the fixed base62 suffix.
	const maxReadable = 80 - 1 - len(projectIDSuffix)
	var b strings.Builder
	lastHyphen := false
	for i := 0; i < len(readable); i++ {
		c := readable[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			b.WriteByte(c)
			lastHyphen = false
		case !lastHyphen:
			b.WriteByte('-')
			lastHyphen = true
		}
	}
	trimmed := strings.Trim(b.String(), "-")
	if len(trimmed) > maxReadable {
		trimmed = strings.Trim(trimmed[len(trimmed)-maxReadable:], "-")
	}
	if trimmed == "" {
		return "project"
	}
	return trimmed
}
