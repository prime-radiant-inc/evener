package hub

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/auth/openai/oaitest"
)

func TestAuth_LogoutWaitsForAnotherCredentialWrite(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	ctrl := newTestAuthController(t, dir, filepath.Join(dir, "state"), writeProvidersToml(t, dir, bearerInstanceToml))
	originalSet := ctrl.setCredential
	setEntered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	ctrl.setCredential = func(name, value string) error {
		close(setEntered)
		<-release
		return originalSet(name, value)
	}
	setResult := make(chan error, 1)
	go func() {
		_, err := ctrl.ApiKeySet(appwire.AuthApiKeySetParams{Provider: "work-ant", Value: "sk-work"})
		setResult <- err
	}()
	<-setEntered

	cleared := make(chan struct{})
	originalClear := ctrl.clearCredential
	ctrl.clearCredential = func(name string) error {
		close(cleared)
		return originalClear(name)
	}
	type logoutResult struct {
		response appwire.AuthLogoutResponse
		err      error
	}
	logout := make(chan logoutResult, 1)
	go func() {
		response, err := ctrl.Logout(appwire.AuthLogoutParams{Provider: "work-ant"})
		logout <- logoutResult{response: response, err: err}
	}()

	select {
	case <-cleared:
		t.Fatal("Logout entered credential removal while ApiKeySet held the credential write")
	case <-time.After(time.Second):
	}
	releaseOnce.Do(func() { close(release) })
	if err := <-setResult; err != nil {
		t.Fatalf("ApiKeySet: %v", err)
	}
	select {
	case result := <-logout:
		if result.err != nil {
			t.Fatalf("Logout: %v", result.err)
		}
		if !result.response.Removed {
			t.Fatalf("Logout Removed=%v, want true", result.response.Removed)
		}
	case <-time.After(time.Second):
		t.Fatal("Logout did not complete after the credential write released")
	}
	if value, ok := ctrl.creds.Get("work-ant"); ok || value != "" {
		t.Fatalf("credential remains after Logout: value=%q present=%v", value, ok)
	}
}
