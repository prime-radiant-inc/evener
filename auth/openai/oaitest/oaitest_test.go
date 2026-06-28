package oaitest

import (
	"reflect"
	"testing"
	"time"

	authopenai "primeradiant.com/serf/auth/openai"
)

func TestIsolateOpenAIAuthReturnsStateDirForStorage(t *testing.T) {
	stateDir := IsolateOpenAIAuth(t)

	// F2: the path returned by IsolateOpenAIAuth must equal what DefaultStateDir()
	// computes from XDG_STATE_HOME — verifies env setup and returned path stay consistent.
	if got := authopenai.DefaultStateDir(); got != stateDir {
		t.Fatalf("DefaultStateDir() = %q, want %q (returned by IsolateOpenAIAuth)", got, stateDir)
	}

	record := authopenai.AuthRecord{
		Version:      1,
		Provider:     "openai",
		Source:       "oauth",
		ObtainedAt:   time.Date(2026, 5, 7, 23, 0, 0, 0, time.UTC),
		TokenType:    "Bearer",
		Scope:        "openid profile email offline_access",
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
		Expiry:       time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC),
		Email:        "user@example.com",
	}

	if err := authopenai.SaveAuth(stateDir, "openai", record); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}
	loaded, err := authopenai.LoadAuth(stateDir, "openai")
	if err != nil {
		t.Fatalf("LoadAuth() error = %v", err)
	}
	// F1: assert all fields survive the round-trip, not just AccessToken.
	if !reflect.DeepEqual(loaded, record) {
		t.Fatalf("LoadAuth() round-trip mismatch:\n  got  %+v\n  want %+v", loaded, record)
	}
}
