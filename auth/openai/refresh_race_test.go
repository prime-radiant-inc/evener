package openai

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// refreshResult is one ResolveRuntimeCredentials answer.
type refreshResult struct {
	creds RuntimeCredentials
	err   error
}

// blockedRefresh is a ResolveRuntimeCredentials call parked at the token
// endpoint: a test acts while the request is in flight, then releases it.
type blockedRefresh struct {
	entered     chan struct{}
	release     chan struct{}
	releaseOnce sync.Once
	result      chan refreshResult
}

// startBlockedRefresh seeds no record of its own; the caller seeds one first.
func startBlockedRefresh(t *testing.T, stateDir, instanceName string, now time.Time) *blockedRefresh {
	t.Helper()
	b := &blockedRefresh{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		result:  make(chan refreshResult, 1),
	}
	t.Cleanup(b.unblock)
	svc := newTestService(now)
	svc.refreshToken = func(context.Context, *http.Client, Config, RefreshTokenRequest) (TokenSet, error) {
		close(b.entered)
		<-b.release
		return TokenSet{
			AccessToken:  "refreshed-access",
			RefreshToken: "refreshed-refresh",
			TokenType:    "Bearer",
			Scope:        "openid profile email offline_access",
			Expiry:       now.Add(time.Hour),
		}, nil
	}
	go func() {
		creds, err := svc.ResolveRuntimeCredentials(context.Background(), stateDir, instanceName)
		b.result <- refreshResult{creds, err}
	}()
	select {
	case <-b.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the refresh never reached the token endpoint")
	}
	return b
}

func (b *blockedRefresh) unblock() {
	b.releaseOnce.Do(func() { close(b.release) })
}

func (b *blockedRefresh) finish(t *testing.T) refreshResult {
	t.Helper()
	b.unblock()
	select {
	case got := <-b.result:
		return got
	case <-time.After(10 * time.Second):
		t.Fatal("ResolveRuntimeCredentials never returned")
		return refreshResult{}
	}
}

// seedStaleOAuthRecord files a record under name that is one minute from
// expiry, so a resolution refreshes it.
func seedStaleOAuthRecord(t *testing.T, stateDir, name string, now time.Time) AuthRecord {
	t.Helper()
	stale := sampleAuthRecord()
	stale.Provider = name
	stale.AccessToken = "access-" + name
	stale.Expiry = now.Add(time.Minute)
	if err := SaveAuth(stateDir, name, stale); err != nil {
		t.Fatalf("SaveAuth(%s) error = %v", name, err)
	}
	return stale
}

// A record whose timestamps carry a non-local fixed offset decodes to a fresh
// *time.Location on every read, so comparing the two decodes by Go value calls
// an unchanged record changed. The refresh must still land (#1045 review).
func TestRefreshOfAnUnchangedFixedOffsetRecordLands(t *testing.T) {
	stateDir := t.TempDir()
	now := time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)
	ist := time.FixedZone("IST", 5*3600+1800)

	stale := sampleAuthRecord()
	stale.ObtainedAt = stale.ObtainedAt.In(ist)
	stale.Expiry = now.Add(time.Minute).In(ist)
	if err := SaveAuth(stateDir, "openai", stale); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}
	onDisk, err := os.ReadFile(AuthFilePath(stateDir, "openai"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !strings.Contains(string(onDisk), "+05:30") {
		t.Fatalf("the fixture does not carry a fixed offset: %s", onDisk)
	}

	svc := newTestService(now)
	svc.refreshToken = func(context.Context, *http.Client, Config, RefreshTokenRequest) (TokenSet, error) {
		return TokenSet{
			AccessToken:  "refreshed-access",
			RefreshToken: "refreshed-refresh",
			TokenType:    "Bearer",
			Scope:        stale.Scope,
			Expiry:       now.Add(time.Hour),
		}, nil
	}

	creds, err := svc.ResolveRuntimeCredentials(context.Background(), stateDir, "openai")
	if err != nil {
		t.Fatalf("ResolveRuntimeCredentials() error = %v, want the unchanged record's refresh to land", err)
	}
	if creds.BearerToken != "refreshed-access" {
		t.Fatalf("BearerToken = %q, want the refreshed token", creds.BearerToken)
	}
	stored, err := LoadAuth(stateDir, "openai")
	if err != nil {
		t.Fatalf("LoadAuth() error = %v", err)
	}
	if stored.AccessToken != "refreshed-access" || stored.RefreshToken != "refreshed-refresh" {
		t.Fatalf("the refreshed record did not land: %+v", stored)
	}
}

// A runtime refresh can be in flight in a child process while the hub renames
// the instance: the refresh loads the record under the old name, the rename
// moves it to the new name and deletes the old one, and the refresh's write
// then lands. The old name must not be recreated, and the newly issued
// credentials must land on the record the rename moved, or the rotated refresh
// token is lost and the renamed instance needs another sign-in (#1045).
func TestRefreshFollowsARecordRenamedWhileItWasInFlight(t *testing.T) {
	stateDir := t.TempDir()
	now := time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)
	seedStaleOAuthRecord(t, stateDir, "work", now)
	refresh := startBlockedRefresh(t, stateDir, "work", now)

	// The rename, as the hub performs it: read the old record, write it under
	// the new name and delete the old one in one locked section.
	lock, err := LockAuthStore(stateDir)
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

	got := refresh.finish(t)
	if got.err != nil {
		t.Fatalf("ResolveRuntimeCredentials() error = %v, want the refresh to follow the rename", got.err)
	}
	if got.creds.BearerToken != "refreshed-access" {
		t.Fatalf("BearerToken = %q, want the refreshed token", got.creds.BearerToken)
	}
	// The account has to travel with the credentials: by the time the caller
	// sets a header, the instance name it asked for files no record.
	if got.creds.AccountID != "acct_123" {
		t.Fatalf("AccountID = %q, want the account of the record the refresh redeemed", got.creds.AccountID)
	}
	if _, err := LoadAuth(stateDir, "work"); !errors.Is(err, ErrAuthNotFound) {
		t.Fatalf("the refresh recreated the record under the renamed-away name (LoadAuth = %v)", err)
	}
	kept, err := LoadAuth(stateDir, "personal")
	if err != nil {
		t.Fatalf("LoadAuth(personal) error = %v", err)
	}
	if kept.AccessToken != "refreshed-access" || kept.RefreshToken != "refreshed-refresh" {
		t.Fatalf("the moved record did not receive the rotated token: %+v", kept)
	}
	if kept.Provider != "personal" {
		t.Fatalf("Provider = %q, want the destination instance's name", kept.Provider)
	}
}

// The account id a request authenticates as must come from the same record as
// its bearer, not from a second read of the record by instance name: a refresh
// that followed a rename leaves the name the caller used filed with nothing.
// A record written before the account id was persisted on its own still names
// its account in the id token.
func TestRuntimeCredentialsCarryTheRecordsAccount(t *testing.T) {
	now := time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)
	t.Run("persisted account id", func(t *testing.T) {
		stateDir := t.TempDir()
		fresh := sampleAuthRecord()
		fresh.Expiry = now.Add(time.Hour)
		if err := SaveAuth(stateDir, "openai", fresh); err != nil {
			t.Fatalf("SaveAuth() error = %v", err)
		}
		creds, err := newTestService(now).ResolveRuntimeCredentials(context.Background(), stateDir, "openai")
		if err != nil {
			t.Fatalf("ResolveRuntimeCredentials() error = %v", err)
		}
		if creds.AccountID != fresh.AccountID {
			t.Fatalf("AccountID = %q, want %q", creds.AccountID, fresh.AccountID)
		}
	})
	t.Run("id token claims of a record written before the field", func(t *testing.T) {
		stateDir := t.TempDir()
		legacy := sampleAuthRecord()
		legacy.AccountID = ""
		legacy.IDToken = testJWT(t, map[string]any{"chatgpt_account_id": "acct_legacy"})
		legacy.Expiry = now.Add(time.Hour)
		if err := SaveAuth(stateDir, "openai", legacy); err != nil {
			t.Fatalf("SaveAuth() error = %v", err)
		}
		creds, err := newTestService(now).ResolveRuntimeCredentials(context.Background(), stateDir, "openai")
		if err != nil {
			t.Fatalf("ResolveRuntimeCredentials() error = %v", err)
		}
		if creds.AccountID != "acct_legacy" {
			t.Fatalf("AccountID = %q, want the id token's account claim", creds.AccountID)
		}
	})
}

// The same race with a sign-out instead of a rename: the record is deleted
// while the refresh is in flight. Nothing may be written back, and the caller
// is told to sign in.
func TestRefreshDoesNotResurrectARecordDeletedWhileItWasInFlight(t *testing.T) {
	stateDir := t.TempDir()
	now := time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)
	seedStaleOAuthRecord(t, stateDir, "work", now)
	refresh := startBlockedRefresh(t, stateDir, "work", now)

	if _, err := DeleteAuth(stateDir, "work"); err != nil {
		t.Fatalf("DeleteAuth(work) error = %v", err)
	}

	got := refresh.finish(t)
	if !errors.Is(got.err, ErrLoginRequired) {
		t.Fatalf("ResolveRuntimeCredentials() = %+v, %v; want ErrLoginRequired", got.creds, got.err)
	}
	if _, err := LoadAuth(stateDir, "work"); !errors.Is(err, ErrAuthNotFound) {
		t.Fatalf("the refresh recreated the signed-out record (LoadAuth = %v)", err)
	}
}

// Two other instances hold a record equal to the one being refreshed (the same
// credential copied twice). The refresh cannot tell which one the rename meant,
// so it must write to neither rather than risk clobbering a different
// instance's credentials.
func TestRefreshDoesNotGuessBetweenAmbiguousDestinations(t *testing.T) {
	stateDir := t.TempDir()
	now := time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)
	stale := seedStaleOAuthRecord(t, stateDir, "work", now)
	refresh := startBlockedRefresh(t, stateDir, "work", now)

	for _, dup := range []string{"dup-a", "dup-b"} {
		copied := stale
		copied.Provider = dup
		if err := SaveAuth(stateDir, dup, copied); err != nil {
			t.Fatalf("SaveAuth(%s) error = %v", dup, err)
		}
	}
	if _, err := DeleteAuth(stateDir, "work"); err != nil {
		t.Fatalf("DeleteAuth(work) error = %v", err)
	}

	got := refresh.finish(t)
	if !errors.Is(got.err, ErrLoginRequired) {
		t.Fatalf("ResolveRuntimeCredentials() = %+v, %v; want ErrLoginRequired", got.creds, got.err)
	}
	if _, err := LoadAuth(stateDir, "work"); !errors.Is(err, ErrAuthNotFound) {
		t.Fatalf("the refresh recreated the renamed-away record (LoadAuth = %v)", err)
	}
	for _, dup := range []string{"dup-a", "dup-b"} {
		kept, err := LoadAuth(stateDir, dup)
		if err != nil {
			t.Fatalf("LoadAuth(%s) error = %v", dup, err)
		}
		if kept.AccessToken != stale.AccessToken || kept.RefreshToken != stale.RefreshToken {
			t.Fatalf("%s was overwritten by an ambiguous refresh: %+v", dup, kept)
		}
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
	refresh := startBlockedRefresh(t, stateDir, "openai", now)

	newer := sampleAuthRecord()
	newer.AccessToken = "other-process-access"
	newer.RefreshToken = "other-process-refresh"
	newer.ObtainedAt = now.Add(time.Second)
	newer.Expiry = now.Add(2 * time.Hour)
	if err := SaveAuth(stateDir, "openai", newer); err != nil {
		t.Fatalf("SaveAuth(newer) error = %v", err)
	}

	got := refresh.finish(t)
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
