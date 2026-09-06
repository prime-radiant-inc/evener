//go:build unix

package doctor

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"os"
	"reflect"
	"syscall"
	"testing"
	"time"
)

func TestDoctorStableDelegateReadOnlyDoesNotMutateState(t *testing.T) {
	base, sid, jobsPath, delegatesPath := stableDoctorFixture(t)
	beforeJobs := stableDoctorFileStateOf(t, jobsPath)
	beforeDelegates := stableDoctorFileStateOf(t, delegatesPath)

	jobs, err := Jobs(base, sid, JobOpts{})
	if err != nil {
		t.Fatalf("Jobs: %v", err)
	}
	watches, err := Watches(base, sid, WatchOpts{})
	if err != nil {
		t.Fatalf("Watches: %v", err)
	}
	tree, err := Tree(base, sid, TreeOpts{})
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}
	wire, err := json.Marshal(struct {
		Jobs    JobReport
		Watches WatchReport
		Tree    TreeNode
	}{jobs, watches, tree})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(wire, []byte(`"dlg_doctor_stable"`)) {
		t.Fatalf("doctor omitted stable aggregate row: %s", wire)
	}
	if got := stableDoctorFileStateOf(t, jobsPath); !reflect.DeepEqual(got, beforeJobs) {
		t.Fatalf("doctor mutated jobs journal:\n got=%#v\nwant=%#v", got, beforeJobs)
	}
	if got := stableDoctorFileStateOf(t, delegatesPath); !reflect.DeepEqual(got, beforeDelegates) {
		t.Fatalf("doctor mutated delegate journal:\n got=%#v\nwant=%#v", got, beforeDelegates)
	}
}

type stableDoctorFileState struct {
	Inode uint64
	Size  int64
	Mode  os.FileMode
	Mtime time.Time
	Hash  [sha256.Size]byte
	Bytes []byte
}

func stableDoctorFileStateOf(t *testing.T, path string) stableDoctorFileState {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("unexpected stat payload %T", info.Sys())
	}
	return stableDoctorFileState{
		Inode: stat.Ino, Size: info.Size(), Mode: info.Mode(), Mtime: info.ModTime(),
		Hash: sha256.Sum256(raw), Bytes: bytes.Clone(raw),
	}
}
