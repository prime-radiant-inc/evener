package schema

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
)

// TestSessionMeta_CumulativeUsageOmitzero proves CumulativeUsage and WorkMillis
// marshal onto SessionMeta when set, and are both omitted (via omitzero) on a
// zero-valued legacy SessionMeta, so old metas without these fields round-trip
// unchanged (WS2 working-state-metrics).
func TestSessionMeta_CumulativeUsageOmitzero(t *testing.T) {
	meta := SessionMeta{
		CumulativeUsage: CumulativeUsage{
			InputTokens:     100,
			OutputTokens:    200,
			CacheReadTokens: 50,
			TotalTokens:     300,
		},
		WorkMillis: 45000,
	}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	const wantUsage = `"cumulative_usage":{"input_tokens":100,"output_tokens":200,"cache_read_tokens":50,"total_tokens":300}`
	if !strings.Contains(string(data), wantUsage) {
		t.Errorf("marshal = %s, want substring %s", data, wantUsage)
	}
	const wantWorkMillis = `"work_millis":45000`
	if !strings.Contains(string(data), wantWorkMillis) {
		t.Errorf("marshal = %s, want substring %s", data, wantWorkMillis)
	}

	zeroData, err := json.Marshal(SessionMeta{})
	if err != nil {
		t.Fatalf("marshal zero: %v", err)
	}
	if strings.Contains(string(zeroData), "cumulative_usage") {
		t.Errorf("zero-valued SessionMeta marshal = %s, want no cumulative_usage key", zeroData)
	}
	if strings.Contains(string(zeroData), "work_millis") {
		t.Errorf("zero-valued SessionMeta marshal = %s, want no work_millis key", zeroData)
	}
}

// SaveSessionMetaWithFS round-trips through an in-memory fs and surfaces a mkdir
// failure on a read-only fs.
func TestCov_SaveSessionMetaFS(t *testing.T) {
	mem := afero.NewMemMapFs()
	meta := SessionMeta{ID: "01SESSIONXXXXXXXXXXXXXXXXXX"}
	if err := SaveSessionMetaWithFS(mem, "/state", meta); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := loadSessionMetaFS(mem, "/state", meta.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.ID != meta.ID {
		t.Errorf("round-trip ID = %q, want %q", got.ID, meta.ID)
	}

	// Read-only fs → MkdirAll fails → save surfaces an error.
	ro := afero.NewReadOnlyFs(afero.NewMemMapFs())
	if err := SaveSessionMetaWithFS(ro, "/state", meta); err == nil {
		t.Fatal("save on a read-only fs should error")
	}
}

// listSessionMetasFS returns nil for a missing sessions dir and lists saved metas.
func TestCov_ListSessionMetasFS(t *testing.T) {
	mem := afero.NewMemMapFs()
	metas, err := listSessionMetasFS(mem, "/nowhere")
	if err != nil || metas != nil {
		t.Errorf("missing dir should yield (nil,nil), got %v %v", metas, err)
	}
	const sessionID = "02wMz5Txv1C3Hut0M8GCeB"
	if err := SaveSessionMetaWithFS(mem, "/state", SessionMeta{ID: sessionID}); err != nil {
		t.Fatal(err)
	}
	metas, err = listSessionMetasFS(mem, "/state")
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 || metas[0].ID != sessionID {
		t.Errorf("list = %+v, want one meta %s", metas, sessionID)
	}
}

func TestListSessionMetas_CleanBreakValidatesFilenameAndMetadataID(t *testing.T) {
	dir := t.TempDir()
	sessionsDir := filepath.Join(dir, sessionsSubdir)
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const validID = "02wMz5Txv1C3Hut0M8GCeB"
	const otherValidID = "02wMz5Txv2enqVTitaig6F"
	legacyPath := filepath.Join(sessionsDir, "01LEGACY.meta.json")
	mismatchPath := filepath.Join(sessionsDir, otherValidID+".meta.json")
	validPath := filepath.Join(sessionsDir, validID+".meta.json")
	write := func(path, id string) {
		t.Helper()
		data, err := json.Marshal(SessionMeta{ID: id, UpdatedAt: time.Unix(1, 0)})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(legacyPath, validID)
	write(mismatchPath, validID)
	write(validPath, validID)
	oldTime := time.Unix(1_600_000_000, 0)
	for _, path := range []string{legacyPath, mismatchPath} {
		if err := os.Chtimes(path, oldTime, oldTime); err != nil {
			t.Fatal(err)
		}
	}
	legacyBytes, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	mismatchBytes, err := os.ReadFile(mismatchPath)
	if err != nil {
		t.Fatal(err)
	}

	metas, err := ListSessionMetas(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 || metas[0].ID != validID {
		t.Fatalf("metas=%+v, want only matching valid filename/id", metas)
	}
	for path, want := range map[string][]byte{legacyPath: legacyBytes, mismatchPath: mismatchBytes} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("legacy fixture %s was modified", path)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !info.ModTime().Equal(oldTime) {
			t.Fatalf("legacy fixture %s mtime=%v, want %v", path, info.ModTime(), oldTime)
		}
	}
}
