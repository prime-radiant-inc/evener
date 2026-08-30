package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	authopenai "primeradiant.com/evener/auth/openai"
)

// codexRecord plants a valid OAuth record for instanceName under stateDir and
// returns the file the auth layer put it in.
func codexRecord(t *testing.T, stateDir, instanceName string) string {
	t.Helper()
	now := time.Now()
	record := authopenai.AuthRecord{
		Version: 1, Provider: "openai", Source: authopenai.AuthSourceOAuth,
		ObtainedAt: now, TokenType: "Bearer", AccessToken: "at", RefreshToken: "rt",
		Expiry: now.Add(time.Hour), Email: "user@example.com",
	}
	if err := authopenai.SaveAuth(stateDir, instanceName, record); err != nil {
		t.Fatalf("SaveAuth: %v", err)
	}
	return authopenai.AuthFilePath(stateDir, instanceName)
}

// The curated OAuth instance is openai-codex (spec §9.5, §14.1): `evener
// openai status` with no --instance reads auth/openai-codex.json, and an
// auth/openai.json left over from before the cut-over belongs to an instance
// named openai, which by default is the platform API and never reads it.
func TestOpenAIStatusDefaultsToTheCodexInstance(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	os.Unsetenv("OPENAI_API_KEY")
	stateDir := t.TempDir()
	path := codexRecord(t, stateDir, "openai-codex")
	if filepath.Base(path) != "openai-codex.json" {
		t.Fatalf("record path = %q, want auth/openai-codex.json", path)
	}

	var stdout, stderr bytes.Buffer
	if err := runOpenAIStatus([]string{"--state-dir", stateDir}, &stdout, &stderr); err != nil {
		t.Fatalf("status: %v (%s)", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "state=signed-in") || !strings.Contains(stdout.String(), "source=oauth") {
		t.Fatalf("status must read auth/openai-codex.json by default:\n%s", stdout.String())
	}
}

// Logout defaults to the same instance, so the record `login` wrote is the
// record `logout` removes.
func TestOpenAILogoutDefaultsToTheCodexInstance(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	os.Unsetenv("OPENAI_API_KEY")
	stateDir := t.TempDir()
	codex := codexRecord(t, stateDir, "openai-codex")
	stale := codexRecord(t, stateDir, "openai")

	var stdout, stderr bytes.Buffer
	if err := runOpenAILogout([]string{"--state-dir", stateDir}, &stdout, &stderr); err != nil {
		t.Fatalf("logout: %v (%s)", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "deleted=true") {
		t.Fatalf("logout output: %s", stdout.String())
	}
	if _, err := os.Stat(codex); !os.IsNotExist(err) {
		t.Fatalf("auth/openai-codex.json survived logout (stat err = %v)", err)
	}
	if _, err := os.Stat(stale); err != nil {
		t.Fatalf("logout removed auth/openai.json, which belongs to the platform instance: %v", err)
	}
}

// Login names the same instance to the auth service, so the record lands at
// auth/openai-codex.json.
func TestOpenAILoginDefaultsToTheCodexInstance(t *testing.T) {
	var got string
	old := openAILoginAction
	t.Cleanup(func() { openAILoginAction = old })
	openAILoginAction = func(_ context.Context, stateDir, instanceName string, _ func(string) error, _ func(context.Context) (string, error)) (authopenai.AuthStatus, error) {
		got = instanceName
		return authopenai.AuthStatus{SignedIn: true, Source: authopenai.AuthSourceOAuth}, nil
	}

	var stdout, stderr bytes.Buffer
	if err := runOpenAILogin([]string{"--no-device", "--state-dir", t.TempDir()}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("login: %v (%s)", err, stderr.String())
	}
	if got != "openai-codex" {
		t.Fatalf("login instance = %q, want openai-codex", got)
	}
}
