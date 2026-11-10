package agent

import (
	"errors"
	"os"
	"slices"
	"sync"
	"testing"

	"github.com/spf13/afero"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/execsupport/fsatomic"
)

// recordingSyncFS records the Sync calls made through it, by the name of the
// file or directory handle that was synced.
type recordingSyncFS struct {
	afero.Fs
	mu  sync.Mutex
	ops []string
}

func (r *recordingSyncFS) record(op string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ops = append(r.ops, op)
}

func (r *recordingSyncFS) recorded() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ops...)
}

func (r *recordingSyncFS) sawSync(name string) bool {
	return slices.Contains(r.recorded(), "sync:"+name)
}

func (r *recordingSyncFS) Open(name string) (afero.File, error) {
	f, err := r.Fs.Open(name)
	if err != nil {
		return nil, err
	}
	return &recordingSyncFile{File: f, rec: r}, nil
}

func (r *recordingSyncFS) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	f, err := r.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &recordingSyncFile{File: f, rec: r}, nil
}

func (r *recordingSyncFS) Rename(oldname, newname string) error {
	r.record("rename:" + oldname + "->" + newname)
	return r.Fs.Rename(oldname, newname)
}

type recordingSyncFile struct {
	afero.File
	rec *recordingSyncFS
}

func (f *recordingSyncFile) Sync() error {
	f.rec.record("sync:" + f.Name())
	return f.File.Sync()
}

func TestClientMutationSnapshotSyncsDirectory(t *testing.T) {
	t.Parallel()
	fs := &recordingSyncFS{Fs: afero.NewMemMapFs()}
	snapshot := newEmptyClientMutationSnapshot("session-1")
	renamed, err := saveClientMutationSnapshotFS(fs, "/state", "session-1", snapshot, clientMutationWriteReservation, clientMutationFaults{})
	if err != nil {
		t.Fatalf("saveClientMutationSnapshotFS: %v", err)
	}
	if !renamed {
		t.Fatal("saveClientMutationSnapshotFS reported no rename")
	}
	if !fs.sawSync("/state/mutations") {
		t.Fatalf("client mutation snapshot did not sync its directory; operations: %v", fs.recorded())
	}
}

func TestClientMutationSnapshotDirectorySyncFailureReportsPublished(t *testing.T) {
	t.Parallel()
	base := afero.NewMemMapFs()
	fs := &dirOpenFailFS{Fs: base, dir: "/state/mutations"}
	snapshot := newEmptyClientMutationSnapshot("session-1")
	renamed, err := saveClientMutationSnapshotFS(fs, "/state", "session-1", snapshot, clientMutationWriteReservation, clientMutationFaults{})
	if !renamed {
		t.Fatal("directory sync failure lost the rename outcome")
	}
	if !errors.Is(err, fsatomic.ErrPublished) {
		t.Fatalf("saveClientMutationSnapshotFS error = %v, want fsatomic.ErrPublished", err)
	}
	reloaded, loadErr := loadClientMutationSnapshotFS(base, "/state", "session-1")
	if loadErr != nil {
		t.Fatalf("load published snapshot: %v", loadErr)
	}
	if reloaded.SessionID != "session-1" {
		t.Fatalf("published snapshot session = %q", reloaded.SessionID)
	}
}

// TestSteeringCarrierClaimDirectorySyncFailureKeepsPublishedCarrier covers a
// claim whose directory sync failed after the rename: the active-turn slot and
// the carrier were published, so the claim must hand the carrier to the normal
// execution path instead of parking the steer behind the occupied slot it just
// published.
func TestSteeringCarrierClaimDirectorySyncFailureKeepsPublishedCarrier(t *testing.T) {
	t.Parallel()
	sess := newTwoStageMutationSession(t)
	const clientMutationID = "published-carrier"
	steer, err := sess.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: clientMutationID,
		Input:            []appwire.InputItem{{Type: "text", Text: "carry me"}},
	})
	if err != nil {
		t.Fatalf("AcceptClientMutationSteer: %v", err)
	}
	wantTurnID := steer.Receipt.TurnID
	if wantTurnID == "" {
		t.Fatal("the steer's Applied receipt carried no turn id")
	}
	sess.clientMutations.faults.SnapshotDirSync = func() error {
		return errors.New("injected claim directory sync failure")
	}
	defer func() { sess.clientMutations.faults.SnapshotDirSync = nil }()

	carrier, refusal := sess.claimSteeringCarrierInput()
	if refusal != nil {
		t.Fatalf("claimSteeringCarrierInput refusal = %v, want the published claim", refusal)
	}
	if !carrier.SteeringCarrier || carrier.ClientMutationID != clientMutationID || carrier.StableTurnID != wantTurnID {
		t.Fatalf("carrier = %#v, want the published carrier for %q turn %q", carrier, clientMutationID, wantTurnID)
	}
	if got := sess.clientMutations.snapshot().ActiveTurnID; got != wantTurnID {
		t.Fatalf("published ActiveTurnID = %q, want %q", got, wantTurnID)
	}
	if sess.steeringParkedNow() {
		t.Fatal("the steering was parked although its carrier claim was published")
	}
}

// dirOpenFailFS fails Open for one directory path, leaving every file open and
// the rest of the filesystem intact.
type dirOpenFailFS struct {
	afero.Fs
	dir string
}

func (f *dirOpenFailFS) Open(name string) (afero.File, error) {
	if name == f.dir {
		return nil, errors.New("injected directory open failure")
	}
	return f.Fs.Open(name)
}
