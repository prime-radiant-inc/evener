package schema

import (
	"testing"

	"github.com/spf13/afero"
)

// saveSessionMetaFS round-trips through an in-memory fs and surfaces a mkdir
// failure on a read-only fs.
func TestCov_SaveSessionMetaFS(t *testing.T) {
	mem := afero.NewMemMapFs()
	meta := SessionMeta{ID: "01SESSIONXXXXXXXXXXXXXXXXXX"}
	if err := saveSessionMetaFS(mem, "/state", meta); err != nil {
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
	if err := saveSessionMetaFS(ro, "/state", meta); err == nil {
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
	if err := saveSessionMetaFS(mem, "/state", SessionMeta{ID: "01AAA"}); err != nil {
		t.Fatal(err)
	}
	metas, err = listSessionMetasFS(mem, "/state")
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 || metas[0].ID != "01AAA" {
		t.Errorf("list = %+v, want one meta 01AAA", metas)
	}
}
