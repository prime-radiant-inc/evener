package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/doctor"
	"primeradiant.com/evener/fuzz/reflectfill"
)

// evener-doctor's mutations reader cannot import clientMutationSnapshot — it is
// unexported — so it mirrors the persisted shape and decodes with
// DisallowUnknownFields. That leaves exactly one way for the two to drift: a
// field added here that the mirror has never heard of, which would make the
// doctor refuse a real store mid-diagnosis.
//
// This walks the TYPE with reflection rather than hand-writing a fixture, so a
// field added tomorrow is populated with no edit to this test, marshaled by the
// same json.Marshal saveClientMutationSnapshotFS uses, and named by the failure.
func TestClientMutationSnapshotStaysReadableByTheDoctor(t *testing.T) {
	t.Parallel()
	base := t.TempDir() // an override root: base IS the bucket
	sid := "02wMz5TxvEMoJEDTDGOTil"
	writeDoctorDriftFile(t, filepath.Join(base, sessionsSubdir, sid+".transcript.jsonl"),
		`{"kind":"header","session_id":"`+sid+`"}`+"\n")

	var snapshot clientMutationSnapshot
	reflectfill.Fill(t, reflect.ValueOf(&snapshot).Elem(), "clientMutationSnapshot")
	// The two fields the doctor checks against its caller, not against itself.
	snapshot.Version = clientMutationSnapshotVersion
	snapshot.SessionID = sid

	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	writeDoctorDriftFile(t, clientMutationFilePath(base, sid), string(data))

	report, err := doctor.Mutations(base, sid)
	if err != nil {
		t.Fatalf("evener-doctor cannot read a fully populated client-mutation store — its mirror "+
			"has drifted from clientMutationSnapshot (add the field to clientMutationStoreFile "+
			"in agent/doctor/mutations.go): %v", err)
	}
	if !report.Present || len(report.Journal) != 1 {
		t.Fatalf("doctor read the store but reported %d journal records (present=%t), want 1",
			len(report.Journal), report.Present)
	}
}

func writeDoctorDriftFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
