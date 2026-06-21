// Package oaitest provides shared helpers to isolate OpenAI auth state
// in unit tests. Tests that read OPENAI_API_KEY, OPENAI_BASE_URL, the
// XDG state dir, etc. must call IsolateOpenAIAuth(t) at the top of the
// test to shield against env leakage from the developer's shell or
// from a sibling test that didn't clean up.
package oaitest

import (
	"path/filepath"
	"testing"

	"primeradiant.com/serf/envvars"
)

// IsolateOpenAIAuth clears every env var that the OpenAI provider /
// auth layer reads, and points XDG_STATE_HOME at a fresh temp dir so
// LoadAuth returns ErrAuthNotFound. Returns the resolved Serf state
// directory ("$XDG_STATE_HOME/serf") so tests can pass it directly to
// openai.LoadAuth or openai.SaveAuth.
//
// Safe to call multiple times in one test; t.Setenv handles cleanup.
func IsolateOpenAIAuth(t *testing.T) string {
	t.Helper()
	for _, v := range []envvars.Var{
		envvars.OpenAIAPIKey,
		envvars.OpenAIBaseURL,
		envvars.OpenAIChatGPTBaseURL,
		envvars.OpenAIOrgID,
		envvars.OpenAIProjectID,
		envvars.OpenAIChatGPTClientID,
	} {
		t.Setenv(v.Name, "")
	}
	stateHome := t.TempDir()
	t.Setenv(envvars.XDGStateHome.Name, stateHome)
	return filepath.Join(stateHome, "serf")
}
