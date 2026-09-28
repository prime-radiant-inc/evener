package openai

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// A refresh token the issuer refuses for good is noted where status reads it,
// so status says to sign in again (#2479). The auth record itself is left
// byte for byte as it was.
func TestRuntimeCredentialsNotesAPermanentRefreshRefusal(t *testing.T) {
	stateDir := t.TempDir()
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	record := sampleAuthRecord()
	record.Expiry = now.Add(time.Minute)
	if err := SaveAuth(stateDir, "openai", record); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(AuthFilePath(stateDir, "openai"))
	if err != nil {
		t.Fatal(err)
	}
	svc := newTestService(now)
	svc.refreshToken = func(context.Context, *http.Client, Config, RefreshTokenRequest) (TokenSet, error) {
		return TokenSet{}, errors.New("token endpoint returned status 400: invalid_grant")
	}

	if _, err := svc.ResolveRuntimeCredentials(context.Background(), stateDir, "openai"); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("ResolveRuntimeCredentials error = %v, want ErrLoginRequired", err)
	}
	status, err := svc.Status(stateDir, "openai")
	if err != nil {
		t.Fatal(err)
	}
	if !status.NeedsLogin {
		t.Fatalf("status = %+v, want NeedsLogin after the issuer refused the refresh token", status)
	}
	after, err := os.ReadFile(AuthFilePath(stateDir, "openai"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("noting the refusal rewrote the auth record")
	}
}

// A refresh that fails for a reason that may pass (a 503) notes nothing, so
// status keeps saying the sign-in is fine.
func TestRuntimeCredentialsNotesNoRefusalForATransientFailure(t *testing.T) {
	stateDir := t.TempDir()
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	record := sampleAuthRecord()
	record.Expiry = now.Add(time.Minute)
	if err := SaveAuth(stateDir, "openai", record); err != nil {
		t.Fatal(err)
	}
	svc := newTestService(now)
	svc.refreshToken = func(context.Context, *http.Client, Config, RefreshTokenRequest) (TokenSet, error) {
		return TokenSet{}, errors.New("token endpoint returned status 503")
	}

	if _, err := svc.ResolveRuntimeCredentials(context.Background(), stateDir, "openai"); err == nil || errors.Is(err, ErrLoginRequired) {
		t.Fatalf("ResolveRuntimeCredentials error = %v, want a transient failure", err)
	}
	if RefreshRejected(stateDir, "openai", record) {
		t.Fatal("a transient refresh failure was noted as a refusal")
	}
}

// The note names the refused token. When another process refreshed the same
// record first and saved the token that replaced it, the note says nothing
// about the record now on disk, so status never cries sign-in for a healthy
// login.
func TestRefreshRefusalNamesOnlyTheRefusedToken(t *testing.T) {
	stateDir := t.TempDir()
	refused := sampleAuthRecord()
	if err := RecordRefreshRejection(stateDir, "openai", refused, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !RefreshRejected(stateDir, "openai", refused) {
		t.Fatal("the refused token is not reported refused")
	}
	rotated := refused
	rotated.RefreshToken = "rotated-refresh-token"
	if RefreshRejected(stateDir, "openai", rotated) {
		t.Fatal("a record holding another token is reported refused")
	}
	if RefreshRejected(stateDir, "work", refused) {
		t.Fatal("another instance is reported refused")
	}
}

// Saving a record clears the note: a login, or a refresh that worked, even
// from an issuer that keeps the same refresh token.
func TestSaveAuthClearsARefreshRefusal(t *testing.T) {
	stateDir := t.TempDir()
	record := sampleAuthRecord()
	if err := RecordRefreshRejection(stateDir, "openai", record, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := SaveAuth(stateDir, "openai", record); err != nil {
		t.Fatal(err)
	}
	if RefreshRejected(stateDir, "openai", record) {
		t.Fatal("the note outlived a save of the same record")
	}
}

// A refresh that works after a refusal clears the note, so a refusal the
// issuer later takes back does not leave the instance asking to sign in.
func TestRuntimeCredentialsRefreshClearsAnEarlierRefusal(t *testing.T) {
	stateDir := t.TempDir()
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	record := sampleAuthRecord()
	record.Expiry = now.Add(time.Minute)
	if err := SaveAuth(stateDir, "openai", record); err != nil {
		t.Fatal(err)
	}
	if err := RecordRefreshRejection(stateDir, "openai", record, now); err != nil {
		t.Fatal(err)
	}
	svc := newTestService(now)
	svc.refreshToken = func(context.Context, *http.Client, Config, RefreshTokenRequest) (TokenSet, error) {
		// An issuer that does not rotate: no new refresh token.
		return TokenSet{AccessToken: "fresh-access-token", TokenType: "Bearer", Expiry: now.Add(time.Hour)}, nil
	}

	if _, err := svc.ResolveRuntimeCredentials(context.Background(), stateDir, "openai"); err != nil {
		t.Fatal(err)
	}
	status, err := svc.Status(stateDir, "openai")
	if err != nil {
		t.Fatal(err)
	}
	if status.NeedsLogin {
		t.Fatalf("status = %+v, want no NeedsLogin once a refresh worked", status)
	}
}

// Signing out clears the note with the record.
func TestDeleteAuthClearsARefreshRefusal(t *testing.T) {
	stateDir := t.TempDir()
	record := sampleAuthRecord()
	if err := SaveAuth(stateDir, "openai", record); err != nil {
		t.Fatal(err)
	}
	if err := RecordRefreshRejection(stateDir, "openai", record, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := DeleteAuth(stateDir, "openai"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(refreshRejectionPath(stateDir, "openai")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the note survived signing out: %v", err)
	}
}

// The note is private like the record, never carries the token, and can never
// be read as an instance's auth record: its name does not end in ".json".
func TestRefreshRefusalNoteIsPrivateAndNotARecord(t *testing.T) {
	stateDir := t.TempDir()
	if err := RecordRefreshRejection(stateDir, "openai", AuthRecord{RefreshToken: "secret-refresh-token"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	path := refreshRejectionPath(stateDir, "openai")
	if filepath.Ext(path) == ".json" {
		t.Fatalf("note %s ends in .json", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Windows records no permission bits (TestAuthStorageSaveUsesOwnerOnlyPermissions).
	if got := info.Mode().Perm(); runtime.GOOS != "windows" && got != 0o600 {
		t.Fatalf("note permissions = %#o, want %#o", got, 0o600)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "secret-refresh-token") {
		t.Fatal("the note carries the refresh token itself")
	}
}

// A failed delete (anything but the file already being gone) must not lose
// the note: the record is still there, refused, and the caller sees the
// error, so a retry (or the next failed refresh) still has the signal.
func TestDeleteAuthKeepsTheNoteWhenTheDeleteFails(t *testing.T) {
	stateDir := t.TempDir()
	record := sampleAuthRecord()
	// Force os.Remove to fail with something other than ErrNotExist: a
	// non-empty directory sitting where the record's file would be.
	path := AuthFilePath(stateDir, "openai")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "child"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RecordRefreshRejection(stateDir, "openai", record, time.Now()); err != nil {
		t.Fatal(err)
	}

	if _, err := DeleteAuth(stateDir, "openai"); err == nil {
		t.Fatal("DeleteAuth over a non-empty directory unexpectedly succeeded")
	}
	if !RefreshRejected(stateDir, "openai", record) {
		t.Fatal("the note was cleared even though the delete failed")
	}
}

// An issuer that does not rotate refresh tokens can still race two concurrent
// refreshes to a split result: one succeeds and saves a new record (a fresh
// access token and ObtainedAt, even though the refresh token itself is
// unchanged), the other is permanently refused for the same, now-superseded
// refresh token and notes it. Because the token alone would still match, the
// note also has to compare the record's ObtainedAt, so a note taken from the
// stale (pre-refresh) record does not misreport the now-current one.
func TestRefreshRefusalDoesNotMisreportARecordANonRotatingRefreshAlreadyReplaced(t *testing.T) {
	stateDir := t.TempDir()
	stale := sampleAuthRecord()
	stale.ObtainedAt = time.Now().Add(-time.Hour)
	if err := RecordRefreshRejection(stateDir, "openai", stale, time.Now()); err != nil {
		t.Fatal(err)
	}

	current := stale
	current.ObtainedAt = time.Now()
	current.AccessToken = "fresh-access-token"
	if RefreshRejected(stateDir, "openai", current) {
		t.Fatal("a record a concurrent successful refresh already replaced is reported refused")
	}
	// The stale record itself, as read at the moment of the refusal, is still
	// reported refused: the guard only distinguishes it from what replaced it.
	if !RefreshRejected(stateDir, "openai", stale) {
		t.Fatal("the exact record the refusal was about is not reported refused")
	}
}
