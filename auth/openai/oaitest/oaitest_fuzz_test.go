package oaitest

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/envvars"
)

// FuzzIsolateOpenAIAuth drives the real test-isolation helper from arbitrary
// ambient auth state. The input is hex-encoded before Setenv so every generated
// byte sequence is a valid environment value. Oracles beyond no-panic:
//
//   - every OpenAI auth variable is cleared;
//   - the returned state directory agrees with the isolated XDG state home;
//   - repeated isolation replaces, rather than reuses, the temporary state home.
func FuzzIsolateOpenAIAuth(f *testing.F) {
	f.Add([]byte(nil))
	f.Add([]byte("ambient-auth-state"))
	f.Add([]byte{0, 1, 127, 128, 255})

	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 1<<16 {
			t.Skip()
		}

		ambient := hex.EncodeToString(raw)
		authVars := []envvars.Var{
			envvars.OpenAIAPIKey,
			envvars.OpenAIBaseURL,
			envvars.OpenAIChatGPTBaseURL,
			envvars.OpenAIOrgID,
			envvars.OpenAIProjectID,
			envvars.OpenAIChatGPTClientID,
		}
		for _, v := range authVars {
			t.Setenv(v.Name, ambient)
		}
		t.Setenv(envvars.XDGStateHome.Name, filepath.Join(t.TempDir(), "ambient"))

		firstStateDir := IsolateOpenAIAuth(t)
		assertOpenAIAuthIsolated(t, authVars, firstStateDir)

		secondStateDir := IsolateOpenAIAuth(t)
		assertOpenAIAuthIsolated(t, authVars, secondStateDir)
		if secondStateDir == firstStateDir {
			t.Fatalf("repeated isolation reused state directory %q", firstStateDir)
		}
	})
}

func assertOpenAIAuthIsolated(t *testing.T, authVars []envvars.Var, stateDir string) {
	t.Helper()
	for _, v := range authVars {
		if got := os.Getenv(v.Name); got != "" {
			t.Errorf("%s = %q, want empty", v.Name, got)
		}
	}

	stateHome := os.Getenv(envvars.XDGStateHome.Name)
	if got, want := stateDir, filepath.Join(stateHome, "serf"); got != want {
		t.Errorf("state directory = %q, want %q", got, want)
	}
}
