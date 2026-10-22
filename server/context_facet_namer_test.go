package server

import "testing"

// TestContextFacetFixtureSuppressesAutomaticNaming proves the publication
// fixture holds the real session state that stops the asynchronous session
// namer before allocation measurement. SessionConfig.StateDir enables
// launchInitialPromptNamer in agent/session_namer.go; without an existing title
// it schedules a goroutine that calls the provider and writes session-log and
// autosave data, and ProcessInput does not wait for it. Rename records the
// authored title with NameSource="user", the state launchInitialPromptNamer
// checks (naming.set, naming.value) and shouldApplySessionNameLocked honors.
// This reads the session's own metadata rather than racing allocation counters.
func TestContextFacetFixtureSuppressesAutomaticNaming(t *testing.T) {
	fixture := newContextFacetFixture(t)
	meta := fixture.sess.Meta()
	if meta.Name != contextFacetFixtureTitle || meta.NameSource != "user" {
		t.Fatalf("fixture session title = %q (source %q), want %q (source \"user\"): an untitled session leaves automatic naming enabled, so ProcessInput's asynchronous namer can overlap AllocsPerRun",
			meta.Name, meta.NameSource, contextFacetFixtureTitle)
	}
}
