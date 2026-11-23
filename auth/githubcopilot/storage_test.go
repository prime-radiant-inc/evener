package githubcopilot

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func TestAuthFilePathIsSchemeNamespaced(t *testing.T) {
	stateDir := t.TempDir()
	got := AuthFilePath(stateDir, "work")
	want := filepath.Join(stateDir, "auth", "github-copilot", "work.json")
	if got != want {
		t.Fatalf("AuthFilePath() = %q, want %q", got, want)
	}
}

func TestAuthFilePathContainsTraversal(t *testing.T) {
	stateDir := "/var/lib/evener"
	authDir := filepath.Join(stateDir, "auth", "github-copilot")
	for _, name := range []string{"../../canary", "../escape", "a/b/c", "/abs/path", "github-copilot"} {
		got := AuthFilePath(stateDir, name)
		if filepath.Dir(got) != authDir {
			t.Errorf("AuthFilePath(%q) = %q; want a file directly under %q", name, got, authDir)
		}
	}
}

func TestAuthFilePathWithoutStateDirIsEmpty(t *testing.T) {
	if got := AuthFilePath("", "github-copilot"); got != "" {
		t.Fatalf("AuthFilePath(\"\", ...) = %q, want \"\"", got)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	stateDir := t.TempDir()
	record := sampleRecord()
	if err := SaveAuth(stateDir, record.Instance, record); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}

	loaded, err := LoadAuth(stateDir, record.Instance)
	if err != nil {
		t.Fatalf("LoadAuth() error = %v", err)
	}
	if loaded.Instance != record.Instance || loaded.Scheme != record.Scheme || loaded.Version != record.Version {
		t.Fatalf("LoadAuth() identity = instance %q scheme %q version %d", loaded.Instance, loaded.Scheme, loaded.Version)
	}
	if loaded.AccessToken != record.AccessToken {
		t.Fatal("LoadAuth() access_token differs from the saved record")
	}
	if loaded.RefreshToken != record.RefreshToken {
		t.Fatal("LoadAuth() refresh_token differs from the saved record")
	}
	if loaded.CopilotToken != record.CopilotToken {
		t.Fatal("LoadAuth() copilot_token differs from the saved record")
	}
	if !reflect.DeepEqual(loaded, record) {
		t.Fatal("LoadAuth() differs from the saved record in a non-token field")
	}
}

func TestSaveLoadRoundTripPreservesRefreshRejection(t *testing.T) {
	stateDir := t.TempDir()
	record := sampleRecord()
	record.Email = ""
	record.RefreshRejected = true
	if err := SaveAuth(stateDir, record.Instance, record); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}
	loaded, err := LoadAuth(stateDir, record.Instance)
	if err != nil {
		t.Fatalf("LoadAuth() error = %v", err)
	}
	if !loaded.RefreshRejected || loaded.Email != "" {
		t.Fatalf("LoadAuth() refresh_rejected = %v, email = %q; want true and empty", loaded.RefreshRejected, loaded.Email)
	}
}

func TestSaveAuthUsesOwnerOnlyPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not portable on windows")
	}
	stateDir := t.TempDir()
	record := sampleRecord()
	if err := SaveAuth(stateDir, record.Instance, record); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}
	info, err := os.Stat(AuthFilePath(stateDir, record.Instance))
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("file permissions = %#o, want %#o", got, 0o600)
	}
}

func TestSaveAuthRejectsCrossSchemeRecord(t *testing.T) {
	stateDir := t.TempDir()
	record := sampleRecord()
	record.Scheme = "oauth-openai"
	err := SaveAuth(stateDir, record.Instance, record)
	if !errors.Is(err, ErrAuthInvalid) {
		t.Fatalf("SaveAuth() error = %v, want ErrAuthInvalid", err)
	}
	if _, statErr := os.Stat(AuthFilePath(stateDir, record.Instance)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("cross-scheme record was written (Stat error = %v)", statErr)
	}
}

func TestSaveAuthRejectsMismatchedInstance(t *testing.T) {
	stateDir := t.TempDir()
	record := sampleRecord()
	err := SaveAuth(stateDir, "other-instance", record)
	if !errors.Is(err, ErrAuthInvalid) {
		t.Fatalf("SaveAuth() error = %v, want ErrAuthInvalid", err)
	}
}

func TestSaveAuthRejectsIncompleteRecord(t *testing.T) {
	stateDir := t.TempDir()
	record := sampleRecord()
	record.AccountID = ""
	if err := SaveAuth(stateDir, record.Instance, record); !errors.Is(err, ErrAuthInvalid) {
		t.Fatalf("SaveAuth() error = %v, want ErrAuthInvalid", err)
	}
}

func TestSaveAuthRequiresStateDir(t *testing.T) {
	record := sampleRecord()
	if err := SaveAuth("", record.Instance, record); err == nil {
		t.Fatal("SaveAuth(\"\", ...) error = nil, want an error")
	}
}

func TestSaveAuthReplacesRecordAndLeavesNoTempFiles(t *testing.T) {
	stateDir := t.TempDir()
	record := sampleRecord()
	if err := SaveAuth(stateDir, record.Instance, record); err != nil {
		t.Fatalf("SaveAuth() first save error = %v", err)
	}
	record.AccessToken = "new-access-token"
	if err := SaveAuth(stateDir, record.Instance, record); err != nil {
		t.Fatalf("SaveAuth() second save error = %v", err)
	}
	loaded, err := LoadAuth(stateDir, record.Instance)
	if err != nil {
		t.Fatalf("LoadAuth() error = %v", err)
	}
	if loaded.AccessToken != "new-access-token" {
		t.Fatal("the second save did not replace the stored access token")
	}
	matches, err := filepath.Glob(AuthFilePath(stateDir, record.Instance) + ".tmp-*")
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary files left behind: %v", matches)
	}
}

func TestLoadAuthReturnsNotFoundForMissingRecord(t *testing.T) {
	if _, err := LoadAuth(t.TempDir(), "github-copilot"); !errors.Is(err, ErrAuthNotFound) {
		t.Fatalf("LoadAuth() error = %v, want ErrAuthNotFound", err)
	}
}

func TestLoadAuthDoesNotReadOtherSchemePaths(t *testing.T) {
	stateDir := t.TempDir()
	legacyPath := filepath.Join(stateDir, "auth", "github-copilot.json")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	data, err := json.Marshal(sampleRecord())
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if err := os.WriteFile(legacyPath, data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := LoadAuth(stateDir, "github-copilot"); !errors.Is(err, ErrAuthNotFound) {
		t.Fatalf("LoadAuth() error = %v, want ErrAuthNotFound", err)
	}
}

func TestLoadAuthReturnsCorruptForMalformedJSON(t *testing.T) {
	stateDir := t.TempDir()
	path := AuthFilePath(stateDir, "github-copilot")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte("{not-json"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := LoadAuth(stateDir, "github-copilot"); !errors.Is(err, ErrAuthCorrupt) {
		t.Fatalf("LoadAuth() error = %v, want ErrAuthCorrupt", err)
	}
}

func TestLoadAuthRejectsCrossSchemeRecord(t *testing.T) {
	record := recordFixtureMap(t)
	record["auth_scheme"] = "oauth-openai"
	stateDir := t.TempDir()
	writeRecordFixture(t, stateDir, "github-copilot", record)
	if _, err := LoadAuth(stateDir, "github-copilot"); !errors.Is(err, ErrAuthCorrupt) {
		t.Fatalf("LoadAuth() error = %v, want ErrAuthCorrupt", err)
	}
}

func TestLoadAuthRejectsMismatchedInstance(t *testing.T) {
	record := recordFixtureMap(t)
	record["instance"] = "other-instance"
	stateDir := t.TempDir()
	writeRecordFixture(t, stateDir, "github-copilot", record)
	if _, err := LoadAuth(stateDir, "github-copilot"); !errors.Is(err, ErrAuthCorrupt) {
		t.Fatalf("LoadAuth() error = %v, want ErrAuthCorrupt", err)
	}
}

func TestLoadAuthRejectsMissingRequiredFields(t *testing.T) {
	for _, key := range []string{
		"instance", "auth_scheme", "version", "access_token", "copilot_token",
		"copilot_expiry", "obtained_at", "refresh_token", "refresh_expiry",
		"account_id",
	} {
		t.Run(key, func(t *testing.T) {
			record := recordFixtureMap(t)
			delete(record, key)
			stateDir := t.TempDir()
			writeRecordFixture(t, stateDir, "github-copilot", record)
			if _, err := LoadAuth(stateDir, "github-copilot"); !errors.Is(err, ErrAuthCorrupt) {
				t.Fatalf("LoadAuth() error = %v, want ErrAuthCorrupt", err)
			}
		})
	}
}

func TestDeleteAuthRemovesRecordAndIsIdempotent(t *testing.T) {
	stateDir := t.TempDir()
	record := sampleRecord()
	if err := SaveAuth(stateDir, record.Instance, record); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}
	if err := DeleteAuth(stateDir, record.Instance); err != nil {
		t.Fatalf("DeleteAuth() error = %v", err)
	}
	if _, err := LoadAuth(stateDir, record.Instance); !errors.Is(err, ErrAuthNotFound) {
		t.Fatalf("LoadAuth() after delete error = %v, want ErrAuthNotFound", err)
	}
	if err := DeleteAuth(stateDir, record.Instance); err != nil {
		t.Fatalf("DeleteAuth() on missing record error = %v, want nil", err)
	}
}

func recordFixtureMap(t *testing.T) map[string]any {
	t.Helper()
	data, err := json.Marshal(sampleRecord())
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var record map[string]any
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	return record
}

func writeRecordFixture(t *testing.T, stateDir, instance string, record map[string]any) {
	t.Helper()
	path := AuthFilePath(stateDir, instance)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}
