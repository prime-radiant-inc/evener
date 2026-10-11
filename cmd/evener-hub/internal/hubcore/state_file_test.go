package hubcore

import (
	"bytes"
	"errors"
	"syscall"
	"testing"

	"github.com/spf13/afero"
)

// TestWriteStateFileAtomicPublishesAndReportsRenamed pins the shared writer's
// happy path: the data is published at path, the temp file is gone, and
// renamed is true once the rename has happened.
func TestWriteStateFileAtomicPublishesAndReportsRenamed(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := "/state/deletions/state.json"
	data := []byte(`{"ok":true}`)

	renamed, err := writeStateFileAtomic(fs, path, "deletion", data, nil, nil)
	if err != nil || !renamed {
		t.Fatalf("writeStateFileAtomic = %v, %v; want true, nil", renamed, err)
	}
	got, err := afero.ReadFile(fs, path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("published content = %q, want %q", got, data)
	}
	entries, err := afero.ReadDir(fs, "/state/deletions")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("directory holds %v, want only the state file", entries)
	}
}

// TestWriteStateFileAtomicLabelsErrors pins that the store-specific label is
// folded into the wrapped error so each store's diagnostics are unchanged.
func TestWriteStateFileAtomicLabelsErrors(t *testing.T) {
	fs := &faultCreateFs{Fs: afero.NewMemMapFs(), writeErr: errors.New("boom")}
	if _, err := writeStateFileAtomic(fs, "/state/deletions/state.json", "deletion", []byte("x"), nil, nil); !errContains(err, "write temp deletion state") {
		t.Fatalf("write error = %v, want write temp deletion state", err)
	}
}

// TestWriteStateFileAtomicPreRenameFailureCleansTemp pins that a failure before
// the rename reports renamed=false and leaves no temp residue.
func TestWriteStateFileAtomicPreRenameFailureCleansTemp(t *testing.T) {
	fs := afero.NewMemMapFs()
	want := errors.New("before rename fault")
	renamed, err := writeStateFileAtomic(fs, "/state/deletions/state.json", "deletion", []byte("x"),
		func() error { return want }, nil)
	if renamed || !errors.Is(err, want) {
		t.Fatalf("pre-rename renamed=%v err=%v, want false and the fault", renamed, err)
	}
	entries, err := afero.ReadDir(fs, "/state/deletions")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("pre-rename temp residue = %v", entries)
	}
}

// TestWriteStateFileAtomicToleratesUnsupportedSync pins the deletion-store
// tolerance contract: an unsupported fsync is not a failure.
func TestWriteStateFileAtomicToleratesUnsupportedSync(t *testing.T) {
	fs := &faultCreateFs{Fs: afero.NewMemMapFs(), syncErr: syscall.ENOSYS}
	renamed, err := writeStateFileAtomic(fs, "/state/deletions/state.json", "deletion", []byte("x"), nil, nil)
	if err != nil || !renamed {
		t.Fatalf("unsupported sync renamed=%v err=%v, want true, nil", renamed, err)
	}
}

// TestReadStateFileMissingAndPresent pins the missing-file and present-file
// contract the stores fall back on.
func TestReadStateFileMissingAndPresent(t *testing.T) {
	fs := afero.NewMemMapFs()
	if data, ok, err := readStateFile(fs, "/state/deletions/state.json", "deletion"); ok || err != nil || data != nil {
		t.Fatalf("missing read = %q, %v, %v; want nil, false, nil", data, ok, err)
	}
	if err := afero.WriteFile(fs, "/state/deletions/state.json", []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, ok, err := readStateFile(fs, "/state/deletions/state.json", "deletion")
	if err != nil || !ok || string(data) != "hi" {
		t.Fatalf("present read = %q, %v, %v; want hi, true, nil", data, ok, err)
	}
}

// TestDecodeStateFileStrict pins the strict decode: unknown fields and trailing
// JSON are reported, and the label is folded into the message.
func TestDecodeStateFileStrict(t *testing.T) {
	var out struct {
		Name string `json:"name"`
	}
	if err := decodeStateFileStrict([]byte(`{"name":"a"}`), "deletion", &out); err != nil || out.Name != "a" {
		t.Fatalf("decode = %v, out=%+v; want a, nil", err, out)
	}
	if err := decodeStateFileStrict([]byte(`{"name":"a","extra":1}`), "deletion", &out); !errContains(err, "decode deletion state") {
		t.Fatalf("unknown field err = %v, want decode deletion state", err)
	}
	if err := decodeStateFileStrict([]byte(`{"name":"a"} {}`), "deletion", &out); !errContains(err, "trailing JSON value") {
		t.Fatalf("trailing err = %v, want trailing JSON value", err)
	}
}
