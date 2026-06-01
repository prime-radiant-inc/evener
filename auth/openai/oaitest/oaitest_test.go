package oaitest

import (
	"testing"
	"time"

	authopenai "primeradiant.com/serf/auth/openai"
)

func TestIsolateOpenAIAuthReturnsStateDirForStorage(t *testing.T) {
	stateDir := IsolateOpenAIAuth(t)
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
	if loaded.AccessToken != record.AccessToken {
		t.Fatalf("loaded.AccessToken = %q, want %q", loaded.AccessToken, record.AccessToken)
	}
}
