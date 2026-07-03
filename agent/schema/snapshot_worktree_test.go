package schema

import (
	"encoding/json"
	"testing"

	"github.com/spf13/afero"
)

// TestSessionMeta_WorktreeFieldsJSONRoundTrip covers the native worktree tools
// spec §7 "Persistence and resume" persisted fields: WorktreePath,
// WorktreeManaged, and WorktreeRestoreRoot must survive a JSON marshal/unmarshal
// round-trip byte-identically, since RestoreSessionFromMeta reads them straight
// off the decoded struct.
func TestSessionMeta_WorktreeFieldsJSONRoundTrip(t *testing.T) {
	meta := SessionMeta{
		ID:                  "01WORKTREEMETAROUNDTRIP001",
		WorktreePath:        "/repo/.worktrees/proj/lane-a",
		WorktreeManaged:     true,
		WorktreeRestoreRoot: "/repo",
	}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got SessionMeta
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.WorktreePath != meta.WorktreePath {
		t.Errorf("WorktreePath = %q, want %q", got.WorktreePath, meta.WorktreePath)
	}
	if got.WorktreeManaged != meta.WorktreeManaged {
		t.Errorf("WorktreeManaged = %v, want %v", got.WorktreeManaged, meta.WorktreeManaged)
	}
	if got.WorktreeRestoreRoot != meta.WorktreeRestoreRoot {
		t.Errorf("WorktreeRestoreRoot = %q, want %q", got.WorktreeRestoreRoot, meta.WorktreeRestoreRoot)
	}

	// The non-managed / not-in-a-worktree zero value must omit all three
	// fields from the wire form (omitempty) — a resumed session with no
	// worktree history should not grow persisted cruft.
	zero := SessionMeta{ID: "01WORKTREEMETAZEROVALUE001"}
	zeroData, err := json.Marshal(zero)
	if err != nil {
		t.Fatalf("marshal zero: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(zeroData, &raw); err != nil {
		t.Fatalf("unmarshal zero raw: %v", err)
	}
	for _, key := range []string{"worktree_path", "worktree_managed", "worktree_restore_root"} {
		if _, present := raw[key]; present {
			t.Errorf("zero-value SessionMeta JSON unexpectedly has key %q: %s", key, zeroData)
		}
	}
}

// TestSessionMeta_WorktreeFieldsFileRoundTrip covers the same fields through
// the actual SaveSessionMeta/LoadSessionMeta disk path (afero-backed).
func TestSessionMeta_WorktreeFieldsFileRoundTrip(t *testing.T) {
	mem := afero.NewMemMapFs()
	meta := SessionMeta{
		ID:                  "01WORKTREEMETAFILEROUND001",
		WorktreePath:        "/repo/.worktrees/proj/lane-b",
		WorktreeManaged:     false,
		WorktreeRestoreRoot: "/repo",
	}
	if err := saveSessionMetaFS(mem, "/state", meta); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := loadSessionMetaFS(mem, "/state", meta.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.WorktreePath != meta.WorktreePath || got.WorktreeManaged != meta.WorktreeManaged || got.WorktreeRestoreRoot != meta.WorktreeRestoreRoot {
		t.Errorf("round-trip = %+v, want worktree fields matching %+v", got, meta)
	}
}
