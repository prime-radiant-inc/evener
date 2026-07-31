package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"primeradiant.com/serf/agent/doctor"
)

// serf-doctor's mutations reader cannot import clientMutationSnapshot — it is
// unexported — so it mirrors the persisted shape and decodes with
// DisallowUnknownFields. That leaves exactly one way for the two to drift: a
// field added here that the mirror has never heard of, which would make the
// doctor refuse a real store mid-diagnosis.
//
// This walks the TYPE with reflection rather than hand-writing a fixture, so a
// field added tomorrow is populated with no edit to this test, marshaled by the
// same json.Marshal saveClientMutationSnapshotFS uses, and named by the failure.
func TestClientMutationSnapshotStaysReadableByTheDoctor(t *testing.T) {
	base := t.TempDir() // an override root: base IS the bucket
	sid := "02wMz5TxvEMoJEDTDGOTil"
	writeDoctorDriftFile(t, filepath.Join(base, sessionsSubdir, sid+".transcript.jsonl"),
		`{"kind":"header","session_id":"`+sid+`"}`+"\n")

	var snapshot clientMutationSnapshot
	fillEveryField(t, reflect.ValueOf(&snapshot).Elem(), "clientMutationSnapshot")
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
		t.Fatalf("serf-doctor cannot read a fully populated client-mutation store — its mirror "+
			"has drifted from clientMutationSnapshot (add the field to clientMutationStoreFile "+
			"in agent/doctor/mutations.go): %v", err)
	}
	if !report.Present || len(report.Journal) != 1 {
		t.Fatalf("doctor read the store but reported %d journal records (present=%t), want 1",
			len(report.Journal), report.Present)
	}
}

var doctorDriftRawMessageType = reflect.TypeOf(json.RawMessage(nil))

// fillEveryField sets every field reachable from v to a non-zero value, so that
// omitempty cannot drop it from the marshaled snapshot. An unhandled kind fails
// the test: a populator that silently skips a kind is a drift test that silently
// stops covering it.
func fillEveryField(t *testing.T, v reflect.Value, path string) {
	t.Helper()
	switch v.Kind() {
	case reflect.String:
		v.SetString(path)
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(7)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(7)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(7)
	case reflect.Pointer:
		pointer := reflect.New(v.Type().Elem())
		fillEveryField(t, pointer.Elem(), path)
		v.Set(pointer)
	case reflect.Slice:
		// A raw message must hold valid JSON; any other byte slice marshals as
		// base64 and can hold anything.
		if v.Type() == doctorDriftRawMessageType {
			v.SetBytes([]byte(`{"populated":true}`))
			return
		}
		if v.Type().Elem().Kind() == reflect.Uint8 {
			v.SetBytes([]byte{7})
			return
		}
		element := reflect.New(v.Type().Elem())
		fillEveryField(t, element.Elem(), path+"[0]")
		v.Set(reflect.Append(reflect.MakeSlice(v.Type(), 0, 1), element.Elem()))
	case reflect.Map:
		key := reflect.New(v.Type().Key())
		fillEveryField(t, key.Elem(), path+".key")
		value := reflect.New(v.Type().Elem())
		fillEveryField(t, value.Elem(), path+".value")
		entries := reflect.MakeMap(v.Type())
		entries.SetMapIndex(key.Elem(), value.Elem())
		v.Set(entries)
	case reflect.Struct:
		for i := range v.NumField() {
			// Unexported fields are unsettable and invisible to encoding/json,
			// so skipping them cannot hide a field from the marshaled shape.
			if !v.Type().Field(i).IsExported() {
				continue
			}
			fillEveryField(t, v.Field(i), path+"."+v.Type().Field(i).Name)
		}
	default:
		t.Fatalf("%s: unhandled kind %s — teach the populator this kind instead of skipping it", path, v.Kind())
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
