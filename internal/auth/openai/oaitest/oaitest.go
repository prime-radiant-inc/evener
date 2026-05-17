// Package oaitest provides shared helpers to isolate OpenAI auth state
// in unit tests. Tests that read OPENAI_API_KEY, OPENAI_BASE_URL, the
// XDG state dir, etc. must call IsolateOpenAIAuth(t) at the top of the
// test to shield against env leakage from the developer's shell or
// from a sibling test that didn't clean up.
package oaitest

import (
	"path/filepath"
	"testing"
)

// IsolateOpenAIAuth clears every env var that the OpenAI provider /
// auth layer reads, and points XDG_STATE_HOME at a fresh temp dir so
// LoadAuth returns ErrAuthNotFound. Returns the resolved auth state
// directory ("$XDG_STATE_HOME/serf/auth") so tests that want to plant
// a record can do so without re-deriving the path.
//
// Safe to call multiple times in one test; t.Setenv handles cleanup.
func IsolateOpenAIAuth(t *testing.T) string {
	t.Helper()
	for _, key := range []string{
		"OPENAI_API_KEY",
		"OPENAI_BASE_URL",
		"OPENAI_CHATGPT_BASE_URL",
		"OPENAI_ORG_ID",
		"OPENAI_PROJECT_ID",
		"OPENAI_CHATGPT_CLIENT_ID",
	} {
		t.Setenv(key, "")
	}
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	return filepath.Join(stateHome, "serf", "auth")
}
