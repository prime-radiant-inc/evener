package openai

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

// A runtime refresh can be in flight in a child process while the hub renames
// the instance: the refresh loads the record under the old name, the rename
// moves it to the new name and deletes the old one, and the refresh's write
// then lands. The write must not recreate the old-name record (#1045).
func TestRefreshDoesNotResurrectARecordRenamedWhileItWasInFlight(t *testing.T) {
	stateDir := t.TempDir()
	now := time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)
	stale := sampleAuthRecord()
	stale.Provider = "work"
	stale.AccessToken = "access-work"
	stale.Expiry = now.Add(time.Minute) // near expiry, so a refresh runs
	if err := SaveAuth(stateDir, "work", stale); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}

	refreshing := make(chan struct{})
	releaseRefresh := make(chan struct{})
	svc := newTestService(now)
	svc.refreshToken = func(context.Context, *http.Client, Config, RefreshTokenRequest) (TokenSet, error) {
		close(refreshing)
		<-releaseRefresh
		return TokenSet{
			AccessToken:  "refreshed-access",
			RefreshToken: "refreshed-refresh",
			TokenType:    "Bearer",
			Scope:        stale.Scope,
			Expiry:       now.Add(time.Hour),
		}, nil
	}

	type result struct {
		creds RuntimeCredentials
		err   error
	}
	resolved := make(chan result, 1)
	go func() {
		creds, err := svc.ResolveRuntimeCredentials(context.Background(), stateDir, "work")
		resolved <- result{creds, err}
	}()
	select {
	case <-refreshing:
	case <-time.After(10 * time.Second):
		t.Fatal("the refresh never reached the token endpoint")
	}

	// The rename, as the hub performs it: read the old record, write it under
	// the new name and delete the old one in one locked section.
	lock, err := LockAuthStore(context.Background(), stateDir)
	if err != nil {
		t.Fatalf("LockAuthStore() error = %v", err)
	}
	moved, err := LoadAuth(stateDir, "work")
	if err != nil {
		lock.Release()
		t.Fatalf("LoadAuth(work) error = %v", err)
	}
	moved.Provider = "personal"
	if err := lock.Save("personal", moved); err != nil {
		lock.Release()
		t.Fatalf("Save(personal) error = %v", err)
	}
	if _, err := lock.Delete("work"); err != nil {
		lock.Release()
		t.Fatalf("Delete(work) error = %v", err)
	}
	lock.Release()

	close(releaseRefresh)
	var got result
	select {
	case got = <-resolved:
	case <-time.After(10 * time.Second):
		t.Fatal("ResolveRuntimeCredentials never returned")
	}

	if !errors.Is(got.err, ErrLoginRequired) {
		t.Fatalf("ResolveRuntimeCredentials() = %+v, %v; want ErrLoginRequired for the renamed name", got.creds, got.err)
	}
	if _, err := LoadAuth(stateDir, "work"); !errors.Is(err, ErrAuthNotFound) {
		t.Fatalf("the refresh recreated the record under the renamed-away name (LoadAuth = %v)", err)
	}
	kept, err := LoadAuth(stateDir, "personal")
	if err != nil {
		t.Fatalf("LoadAuth(personal) error = %v", err)
	}
	if kept.AccessToken != moved.AccessToken || kept.RefreshToken != moved.RefreshToken {
		t.Fatalf("the renamed record was rewritten by the refresh: %+v", kept)
	}
}

// Two processes can refresh the same instance: the one whose request lands
// last must not put its record over one another writer saved while its request
// was in flight. It reuses the record on disk when that one is usable.
func TestRefreshDoesNotOverwriteARecordSavedWhileItWasInFlight(t *testing.T) {
	stateDir := t.TempDir()
	now := time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)
	stale := sampleAuthRecord()
	stale.AccessToken = "stale-access"
	stale.Expiry = now.Add(time.Minute)
	if err := SaveAuth(stateDir, "openai", stale); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}

	newer := sampleAuthRecord()
	newer.AccessToken = "other-process-access"
	newer.RefreshToken = "other-process-refresh"
	newer.ObtainedAt = now.Add(time.Second)
	newer.Expiry = now.Add(2 * time.Hour)

	refreshing := make(chan struct{})
	releaseRefresh := make(chan struct{})
	svc := newTestService(now)
	svc.refreshToken = func(context.Context, *http.Client, Config, RefreshTokenRequest) (TokenSet, error) {
		close(refreshing)
		<-releaseRefresh
		return TokenSet{
			AccessToken:  "refreshed-access",
			RefreshToken: "refreshed-refresh",
			TokenType:    "Bearer",
			Scope:        stale.Scope,
			Expiry:       now.Add(time.Hour),
		}, nil
	}

	type result struct {
		creds RuntimeCredentials
		err   error
	}
	resolved := make(chan result, 1)
	go func() {
		creds, err := svc.ResolveRuntimeCredentials(context.Background(), stateDir, "openai")
		resolved <- result{creds, err}
	}()
	select {
	case <-refreshing:
	case <-time.After(10 * time.Second):
		t.Fatal("the refresh never reached the token endpoint")
	}

	// Another process's refresh finishes first and saves a usable record.
	if err := SaveAuth(stateDir, "openai", newer); err != nil {
		t.Fatalf("SaveAuth(newer) error = %v", err)
	}
	close(releaseRefresh)

	var got result
	select {
	case got = <-resolved:
	case <-time.After(10 * time.Second):
		t.Fatal("ResolveRuntimeCredentials never returned")
	}
	if got.err != nil {
		t.Fatalf("ResolveRuntimeCredentials() error = %v", got.err)
	}
	if got.creds.BearerToken != newer.AccessToken {
		t.Fatalf("BearerToken = %q, want the usable record already on disk (%q)", got.creds.BearerToken, newer.AccessToken)
	}
	stored, err := LoadAuth(stateDir, "openai")
	if err != nil {
		t.Fatalf("LoadAuth() error = %v", err)
	}
	if stored.AccessToken != newer.AccessToken || stored.RefreshToken != newer.RefreshToken {
		t.Fatalf("the refresh overwrote a newer record: %+v", stored)
	}
}
