package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
)

// TestSaveQueuesFS_EmptyRemovesFile covers the empty-queues-removes-file path
// (line 56-60): when both steering and input are empty, the file is removed.
func TestSaveQueuesFS_EmptyRemovesFile(t *testing.T) {
	fs := afero.NewMemMapFs()
	stateDir := "/state"
	id := "sid1"
	path := queuesFilePath(stateDir, id)
	// Create a pre-existing file so Remove has something to remove.
	if err := afero.WriteFile(fs, path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := saveQueuesFS(fs, stateDir, id, nil, nil); err != nil {
		t.Fatalf("saveQueuesFS empty: %v", err)
	}
	if _, err := fs.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected file to be removed, got err=%v", err)
	}
}

// TestSaveQueuesFS_EmptyStateDir covers the empty-stateDir no-op path
// (line 52-53).
func TestSaveQueuesFS_EmptyStateDir(t *testing.T) {
	fs := afero.NewMemMapFs()
	if err := saveQueuesFS(fs, "", "sid1", []steeringMessage{{Text: "hi"}}, nil); err != nil {
		t.Errorf("saveQueuesFS with empty stateDir: %v", err)
	}
}

// TestSaveQueuesFS_HappyPath covers the write-temp-rename happy path.
func TestSaveQueuesFS_HappyPath(t *testing.T) {
	fs := afero.NewMemMapFs()
	stateDir := "/state"
	id := "sid1"
	steering := []steeringMessage{{Text: "steer"}}
	input := []queuedInput{{Text: "input"}}
	if err := saveQueuesFS(fs, stateDir, id, steering, input); err != nil {
		t.Fatalf("saveQueuesFS: %v", err)
	}
	data, err := afero.ReadFile(fs, queuesFilePath(stateDir, id))
	if err != nil {
		t.Fatal(err)
	}
	var snap persistedQueues
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.Steering) != 1 || snap.Steering[0].Text != "steer" {
		t.Errorf("steering = %v, want one entry with text 'steer'", snap.Steering)
	}
	if len(snap.Input) != 1 || snap.Input[0].Text != "input" {
		t.Errorf("input = %v, want one entry with text 'input'", snap.Input)
	}
}

// TestSaveQueuesFS_MkdirError covers the MkdirAll error path (line 64-65).
// MemMapFs does not fail MkdirAll when a file exists at the path, so this
// path is exercised only on the real OS filesystem. We skip it here and
// document it as a platform-dependent unreachable branch for MemMapFs.
func TestSaveQueuesFS_MkdirError(t *testing.T) {
	t.Skip("MemMapFs does not fail MkdirAll on file-at-path; mkdir error is unreachable in-memory")
}

// TestLoadQueuesFS_EmptyStateDir covers the empty-stateDir no-op path (line 92-93).
func TestLoadQueuesFS_EmptyStateDir(t *testing.T) {
	fs := afero.NewMemMapFs()
	steering, input, err := loadQueuesFS(fs, "", "sid1")
	if err != nil || steering != nil || input != nil {
		t.Errorf("loadQueuesFS with empty stateDir: steering=%v input=%v err=%v", steering, input, err)
	}
}

// TestLoadQueuesFS_NotExist covers the IsNotExist path (line 96-97).
func TestLoadQueuesFS_NotExist(t *testing.T) {
	fs := afero.NewMemMapFs()
	steering, input, err := loadQueuesFS(fs, "/state", "sid1")
	if err != nil || steering != nil || input != nil {
		t.Errorf("loadQueuesFS on nonexistent file: steering=%v input=%v err=%v", steering, input, err)
	}
}

// TestLoadQueuesFS_UnmarshalError covers the unmarshal-error path (line 103-104).
func TestLoadQueuesFS_UnmarshalError(t *testing.T) {
	fs := afero.NewMemMapFs()
	stateDir := "/state"
	id := "sid1"
	path := queuesFilePath(stateDir, id)
	if err := fs.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := afero.WriteFile(fs, path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := loadQueuesFS(fs, stateDir, id)
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
}

// TestLoadQueuesFS_RoundTrip covers the happy path: save then load.
func TestLoadQueuesFS_RoundTrip(t *testing.T) {
	fs := afero.NewMemMapFs()
	stateDir := "/state"
	id := "sid1"
	steering := []steeringMessage{{Text: "steer"}}
	input := []queuedInput{{Text: "input"}}
	if err := saveQueuesFS(fs, stateDir, id, steering, input); err != nil {
		t.Fatalf("saveQueuesFS: %v", err)
	}
	gotSteering, gotInput, err := loadQueuesFS(fs, stateDir, id)
	if err != nil {
		t.Fatalf("loadQueuesFS: %v", err)
	}
	if len(gotSteering) != 1 || gotSteering[0].Text != "steer" {
		t.Errorf("steering = %v, want one entry with text 'steer'", gotSteering)
	}
	if len(gotInput) != 1 || gotInput[0].Text != "input" {
		t.Errorf("input = %v, want one entry with text 'input'", gotInput)
	}
}
