package doctor

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	agentdoctor "primeradiant.com/evener/agent/doctor"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func recoveryHashes(t *testing.T, root string) map[string]string {
	t.Helper()
	hashes := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		hashes[rel] = fmt.Sprintf("%x", sha256.Sum256(data))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return hashes
}

func recoveryCommand(t *testing.T, args ...string) []byte {
	t.Helper()
	var out, stderr bytes.Buffer
	if code := Run(args, nil, &out, &stderr); code != 0 {
		t.Fatalf("doctor %s: exit %d: %s", args[0], code, &stderr)
	}
	return out.Bytes()
}

func recoveryReadHistory(t *testing.T, root, sid, sentinel string) {
	t.Helper()
	var listing agentdoctor.SessionsResult
	if err := json.Unmarshal(recoveryCommand(t, "sessions", "--state-dir", root, "--json"), &listing); err != nil {
		t.Fatal(err)
	}
	if len(listing.Unreadable) != 0 || len(listing.Sessions) != 1 || listing.Sessions[0].SessionID != sid {
		t.Fatalf("unexpected session inventory: %+v", listing)
	}
	var history agentdoctor.TranscriptResult
	if err := json.Unmarshal(recoveryCommand(t, "transcript", sid, "--state-dir", root, "--json", "--full-text"), &history); err != nil {
		t.Fatal(err)
	}
	if history.SessionID != sid || history.TurnsTotal == 0 || history.Elided != 0 {
		t.Fatalf("incomplete history: %+v", history)
	}
	found := false
	for _, turn := range history.Turns {
		if turn.Text == sentinel {
			found = true
		}
	}
	if !found {
		t.Fatal("fixture input missing from read-only history")
	}
	// Exercise the documented human-readable entry point too, without pinning its copy.
	recoveryCommand(t, "transcript", sid, "--state-dir", root, "--format", "outline")
}

func TestBackupRestoreRunbook(t *testing.T) {
	t.Parallel()
	const sid = "02wLIRxqmq3AUo6vl2OW37"
	const sentinel = "backup-input-4f98"
	original := writeStudySession(t, t.TempDir(), sid, []schema.Turn{schema.NewTurn(schema.TurnUserInput, llm.User(sentinel))})
	before := recoveryHashes(t, original)
	// The fixture writer is closed before copying: this is a consistent, synthetic backup.
	backup := filepath.Join(t.TempDir(), "backup")
	if err := os.CopyFS(backup, os.DirFS(original)); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(t.TempDir(), "history-rehearsal")
	if err := os.CopyFS(restored, os.DirFS(backup)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, recoveryHashes(t, restored)) {
		t.Fatal("restore was not an exact copy")
	}
	recoveryReadHistory(t, restored, sid, sentinel)
	for _, root := range []string{original, backup, restored} {
		if !reflect.DeepEqual(before, recoveryHashes(t, root)) {
			t.Fatal("read-only rehearsal changed input bytes")
		}
	}
	t.Logf("provider-free sessions/transcript reads preserved original, backup and restored hashes: %v", before)
}

func TestReconstructionRunbook(t *testing.T) {
	t.Parallel()
	const sid = "02wLIRxqmq3AUo6vl2OW37"
	dbPath, metaPath, _ := reconstructionFixture(t)
	// A real SQLite archive with one omitted output exercises the reported loss boundary.
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE tool_result_events SET content = NULL"); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	source := filepath.Dir(dbPath)
	originalHashes := recoveryHashes(t, source)
	copies := filepath.Join(t.TempDir(), "inputs")
	if err := os.CopyFS(copies, os.DirFS(source)); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "staged-history")
	args := []string{"reconstruct", sid, "--agentsview-db", filepath.Join(copies, filepath.Base(dbPath)), "--meta", filepath.Join(copies, filepath.Base(metaPath)), "--output-dir", output, "--json"}
	var report reconstructionReport
	if err := json.Unmarshal(recoveryCommand(t, args...), &report); err != nil {
		t.Fatal(err)
	}
	if report.SessionID != sid || report.RecoveredThrough != "2026-09-09T01:04:00Z" || !report.MetadataUpdatedAt.After(time.Date(2026, 9, 9, 1, 4, 0, 0, time.UTC)) || report.Entries != 5 || report.ArchivedMessages != 5 || report.ToolCalls != 1 || report.ToolResults != 1 || report.MissingToolOutputs != 1 || len(report.Limitations) == 0 {
		t.Fatalf("unexpected recovery boundary: %+v", report)
	}
	hashes := recoveryHashes(t, output)
	expected := []string{"report.json", "source-snapshot.json", "sessions/" + sid + ".meta.json", "sessions/" + sid + ".transcript.jsonl"}
	if len(hashes) != len(expected) {
		t.Fatalf("reconstruction created runtime state: %v", hashes)
	}
	for _, path := range expected {
		if _, ok := hashes[path]; !ok {
			t.Fatalf("missing staged file %s", path)
		}
	}
	if hashes["source-snapshot.json"] != report.SourceSnapshotSHA256 || hashes["sessions/"+sid+".transcript.jsonl"] != report.TranscriptSHA256 {
		t.Fatal("report hashes do not describe staged bytes")
	}
	recoveryReadHistory(t, output, sid, "task sentinel")
	var out, stderr bytes.Buffer
	if Run(args, nil, &out, &stderr) == 0 {
		t.Fatal("reconstruction overwrote an existing stage")
	}
	if !reflect.DeepEqual(hashes, recoveryHashes(t, output)) {
		t.Fatal("inspection or refused overwrite changed stage")
	}
	for _, root := range []string{source, copies} {
		if !reflect.DeepEqual(originalHashes, recoveryHashes(t, root)) {
			t.Fatal("reconstruction changed source inputs")
		}
	}
	t.Logf("source hashes preserved: %v; recovered_through=%s entries=%d missing_tool_outputs=%d staged_files=%d", originalHashes, report.RecoveredThrough, report.Entries, report.MissingToolOutputs, len(hashes))
}
