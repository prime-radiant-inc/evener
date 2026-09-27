package hub

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/auth/openai/oaitest"
)

func TestAuth_LogoutWaitsForAnotherCredentialWrite(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	// The channels below carry operation progress; this deadline is only a
	// tripwire if a broken lock path leaves a goroutine stuck forever.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	dir := t.TempDir()
	ctrl := newTestAuthController(t, dir, filepath.Join(dir, "state"), writeProvidersToml(t, dir, bearerInstanceToml))
	originalResolve := resolveEndpointFingerprintKey
	var resolveCalls atomic.Int32
	logoutStarted := make(chan struct{})
	resolveEndpointFingerprintKey = func(stateDir string) ([]byte, error) {
		key, err := originalResolve(stateDir)
		// ApiKeySet resolves once before taking credMu; Logout resolves once
		// before trying to take it. The second resolution proves Logout has
		// started without waiting on a wall-clock interval.
		if resolveCalls.Add(1) == 2 {
			close(logoutStarted)
		}
		return key, err
	}
	originalSet := ctrl.setCredential
	setEntered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	ctrl.setCredential = func(name, value string) error {
		close(setEntered)
		<-release
		return originalSet(name, value)
	}
	setResult := make(chan error, 1)
	setDone := make(chan struct{})
	cleared := make(chan struct{})
	originalClear := ctrl.clearCredential
	ctrl.clearCredential = func(name string) error {
		select {
		case <-release:
		default:
			t.Error("Logout entered credential removal before ApiKeySet released its credential write")
		}
		close(cleared)
		return originalClear(name)
	}
	type logoutResult struct {
		response appwire.AuthLogoutResponse
		err      error
	}
	logout := make(chan logoutResult, 1)
	logoutDone := make(chan struct{})
	setLaunched := false
	logoutLaunched := false
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		waitForCompletion := func(done <-chan struct{}, operation string) bool {
			timer := time.NewTimer(time.Second)
			defer timer.Stop()
			select {
			case <-done:
				return true
			case <-timer.C:
				t.Errorf("%s did not complete during test cleanup", operation)
				return false
			}
		}
		setComplete := true
		logoutComplete := true
		if setLaunched {
			setComplete = waitForCompletion(setDone, "ApiKeySet")
		}
		if logoutLaunched {
			logoutComplete = waitForCompletion(logoutDone, "Logout")
		}
		if !setComplete || !logoutComplete {
			t.Errorf("leaving endpoint resolver seam installed because an auth operation is still running")
			return
		}
		resolveEndpointFingerprintKey = originalResolve
	})
	setLaunched = true
	go func() {
		defer close(setDone)
		_, err := ctrl.ApiKeySet(appwire.AuthApiKeySetParams{Provider: "work-ant", Value: "sk-work"})
		setResult <- err
	}()
	select {
	case <-setEntered:
	case <-ctx.Done():
		t.Fatal("ApiKeySet did not enter its credential write")
	}
	logoutLaunched = true
	go func() {
		defer close(logoutDone)
		response, err := ctrl.Logout(appwire.AuthLogoutParams{Provider: "work-ant"})
		logout <- logoutResult{response: response, err: err}
	}()

	select {
	case <-logoutStarted:
	case <-ctx.Done():
		t.Fatal("Logout did not start before ApiKeySet was released")
	}
	// Keep the write held during a bounded negative observation window. This
	// tripwire catches a Logout that reaches clearCredential without credMu;
	// the release channel, not the timer, synchronizes the valid path.
	select {
	case <-cleared:
		t.Fatal("Logout entered credential removal while ApiKeySet held the credential write")
	case <-time.After(time.Second):
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-setResult:
		if err != nil {
			t.Fatalf("ApiKeySet: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("ApiKeySet did not complete after its credential write was released")
	}
	select {
	case result := <-logout:
		if result.err != nil {
			t.Fatalf("Logout: %v", result.err)
		}
		if !result.response.Removed {
			t.Fatalf("Logout Removed=%v, want true", result.response.Removed)
		}
	case <-ctx.Done():
		t.Fatal("Logout did not complete after the credential write released")
	}
	if value, ok := ctrl.creds.Get("work-ant"); ok || value != "" {
		t.Fatalf("credential remains after Logout: value=%q present=%v", value, ok)
	}
}
