package agent

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestSessionLog_AppendAndRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.log")

	log := mustNewSessionLog(t, path)

	entry1 := SessionLogEntry{
		Turn:    1,
		Action:  "shell",
		Summary: "Ran git status",
		Outcome: "success",
	}
	entry2 := SessionLogEntry{
		Turn:         2,
		Action:       "edit_file",
		Summary:      "Modified auth.py",
		Outcome:      "success",
		FilesTouched: []string{"auth.py"},
	}

	if err := log.Append(entry1); err != nil {
		t.Fatalf("failed to append entry1: %v", err)
	}
	if err := log.Append(entry2); err != nil {
		t.Fatalf("failed to append entry2: %v", err)
	}

	entries := log.Entries()
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	if entries[0].Turn != 1 || entries[0].Action != "shell" {
		t.Errorf("entry1 mismatch: got %+v", entries[0])
	}
	if entries[1].Turn != 2 || entries[1].Action != "edit_file" {
		t.Errorf("entry2 mismatch: got %+v", entries[1])
	}
}

func TestSessionLog_Persistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.log")

	// Create log and append entries
	log1 := mustNewSessionLog(t, path)
	entry1 := SessionLogEntry{
		Turn:    1,
		Action:  "shell",
		Summary: "First command",
		Outcome: "success",
	}
	entry2 := SessionLogEntry{
		Turn:     2,
		Action:   "read_file",
		Summary:  "Read config.yaml",
		Outcome:  "failure",
		Failures: []string{"file not found"},
	}

	if err := log1.Append(entry1); err != nil {
		t.Fatalf("failed to append entry1: %v", err)
	}
	if err := log1.Append(entry2); err != nil {
		t.Fatalf("failed to append entry2: %v", err)
	}

	// Create new log from same path
	log2 := mustNewSessionLog(t, path)
	entries := log2.Entries()

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries after reload, got %d", len(entries))
	}

	if entries[0].Turn != 1 || entries[0].Summary != "First command" {
		t.Errorf("entry1 not persisted correctly: got %+v", entries[0])
	}
	if entries[1].Turn != 2 || len(entries[1].Failures) != 1 {
		t.Errorf("entry2 not persisted correctly: got %+v", entries[1])
	}
}

func TestSessionLog_AppendCreatesDirectory(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "nested", "deep", "session.log")

	log := mustNewSessionLog(t, path)

	entry := SessionLogEntry{
		Turn:    1,
		Action:  "shell",
		Summary: "Ran git status",
		Outcome: "success",
	}
	if err := log.Append(entry); err != nil {
		t.Fatalf("Append failed when directory did not exist: %v", err)
	}

	entries := log.Entries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	// Reload from disk to verify persistence through non-existent directory path
	log2 := mustNewSessionLog(t, path)
	reloaded := log2.Entries()
	if len(reloaded) != 1 {
		t.Fatalf("expected 1 entry after reload, got %d", len(reloaded))
	}
	if reloaded[0].Summary != "Ran git status" {
		t.Errorf("reloaded entry mismatch: got %+v", reloaded[0])
	}
}

func TestSessionLog_Range(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.log")

	log := mustNewSessionLog(t, path)

	for i := 0; i < 10; i++ {
		entry := SessionLogEntry{
			Turn:    i,
			Action:  "test",
			Summary: "Test entry",
			Outcome: "success",
		}
		if err := log.Append(entry); err != nil {
			t.Fatalf("failed to append entry %d: %v", i, err)
		}
	}

	tests := []struct {
		name  string
		start int
		end   int
		want  int
	}{
		{"middle range", 2, 5, 3},
		{"full range", 0, 10, 10},
		{"start clamped", -5, 3, 3},
		{"end clamped", 5, 20, 5},
		{"both clamped", -5, 20, 10},
		{"empty range", 5, 5, 0},
		{"reversed range", 5, 3, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entries := log.EntriesRange(tt.start, tt.end)
			if len(entries) != tt.want {
				t.Errorf("EntriesRange(%d, %d) = %d entries, want %d", tt.start, tt.end, len(entries), tt.want)
			}
		})
	}
}

func TestSessionLog_String(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.log")

	log := mustNewSessionLog(t, path)

	entry1 := SessionLogEntry{
		Turn:    47,
		Action:  "edit_file",
		Summary: "Modified auth middleware",
		Outcome: "success",
	}
	entry2 := SessionLogEntry{
		Turn:     48,
		Action:   "shell",
		Summary:  "Ran tests",
		Outcome:  "failure",
		Failures: []string{"test_auth.py failed"},
	}

	if err := log.Append(entry1); err != nil {
		t.Fatalf("failed to append entry1: %v", err)
	}
	if err := log.Append(entry2); err != nil {
		t.Fatalf("failed to append entry2: %v", err)
	}

	str := log.String()

	// Verify format contains key information
	if str == "" {
		t.Error("String() returned empty string")
	}
	// Should contain turn number, action, outcome, and summary
	expectedSubstrings := []string{
		"Turn 47",
		"edit_file",
		"success",
		"Modified auth middleware",
		"Turn 48",
		"shell",
		"failure",
		"Ran tests",
	}

	for _, substr := range expectedSubstrings {
		if !strings.Contains(str, substr) {
			t.Errorf("String() missing expected substring: %q\nGot: %s", substr, str)
		}
	}
}

func TestSessionLog_ConcurrentAppend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.log")

	log := mustNewSessionLog(t, path)

	const numGoroutines = 10
	const entriesPerGoroutine = 10

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < entriesPerGoroutine; j++ {
				entry := SessionLogEntry{
					Turn:    id*100 + j,
					Action:  "test",
					Summary: "Concurrent test",
					Outcome: "success",
				}
				if err := log.Append(entry); err != nil {
					t.Errorf("goroutine %d: failed to append: %v", id, err)
				}
			}
		}(i)
	}

	wg.Wait()

	entries := log.Entries()
	expected := numGoroutines * entriesPerGoroutine
	if len(entries) != expected {
		t.Errorf("expected %d entries after concurrent appends, got %d", expected, len(entries))
	}
}

func TestSessionLog_EmptyLog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.log")

	log := mustNewSessionLog(t, path)

	entries := log.Entries()
	if len(entries) != 0 {
		t.Errorf("new log should have 0 entries, got %d", len(entries))
	}

	if log.Len() != 0 {
		t.Errorf("new log Len() should be 0, got %d", log.Len())
	}

	str := log.String()
	if str != "" {
		t.Errorf("empty log String() should be empty, got %q", str)
	}

	rangeEntries := log.EntriesRange(0, 10)
	if len(rangeEntries) != 0 {
		t.Errorf("empty log EntriesRange should return empty slice, got %d entries", len(rangeEntries))
	}
}

func TestSessionLog_Len(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.log")

	log := mustNewSessionLog(t, path)

	if log.Len() != 0 {
		t.Errorf("new log should have length 0, got %d", log.Len())
	}

	for i := 1; i <= 5; i++ {
		entry := SessionLogEntry{
			Turn:    i,
			Action:  "test",
			Summary: "Test",
			Outcome: "success",
		}
		if err := log.Append(entry); err != nil {
			t.Fatalf("failed to append entry %d: %v", i, err)
		}
		if log.Len() != i {
			t.Errorf("after appending %d entries, Len() = %d, want %d", i, log.Len(), i)
		}
	}
}

// mustNewSessionLog is a test helper that creates a SessionLog or fails the test.
func mustNewSessionLog(t *testing.T, path string) *SessionLog {
	t.Helper()
	log, err := NewSessionLog(path)
	if err != nil {
		t.Fatalf("NewSessionLog(%q): %v", path, err)
	}
	return log
}

