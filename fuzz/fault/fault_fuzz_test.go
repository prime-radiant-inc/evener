package fault

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/spf13/afero"
)

// FuzzFaultSequence drives the decorators as a stateful program. The same
// bytes must always produce the same observable results, including which
// operation faults and which underlying filesystem errors escape unchanged.
func FuzzFaultSequence(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12})
	f.Add([]byte{5, 1, 0, 5, 9, 13, 17, 0, 1, 2, 3, 4, 5, 6, 7})

	f.Fuzz(func(t *testing.T, data []byte) {
		got := runFaultProgram(data)
		if replay := runFaultProgram(data); !reflect.DeepEqual(got, replay) {
			t.Fatalf("fault program is not deterministic:\nfirst:  %v\nreplay: %v", got, replay)
		}
		exerciseFaultContracts(t, data)
	})
}

func runFaultProgram(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	planLen := int(data[0]) % len(data)
	plan := append([]byte(nil), data[1:1+planLen]...)
	ops := data[1+planLen:]
	base := afero.NewMemMapFs()
	fs := FS(base, FromBytes(plan))
	trace := make([]string, 0, len(ops))
	for i, op := range ops {
		path := fmt.Sprintf("/d%d", i%3)
		var err error
		switch op % 11 {
		case 0:
			err = fs.MkdirAll(path, 0o755)
		case 1:
			_, err = fs.Create(path + "/f")
		case 2:
			_, err = fs.Open(path + "/f")
		case 3:
			_, err = fs.OpenFile(path+"/f", os.O_RDWR|os.O_CREATE, 0o600)
		case 4:
			_, err = fs.Stat(path + "/f")
		case 5:
			err = fs.Chmod(path+"/f", 0o640)
		case 6:
			err = fs.Chtimes(path+"/f", time.Unix(1, 0), time.Unix(2, 0))
		case 7:
			err = fs.Rename(path+"/f", path+"/g")
		case 8:
			err = fs.Remove(path + "/f")
		case 9:
			err = fs.RemoveAll(path)
		case 10:
			err = fs.Mkdir(path, 0o755)
		}
		trace = append(trace, errorShape(err))
	}
	return trace
}

func errorShape(err error) string {
	if err == nil {
		return "ok"
	}
	if pe, ok := err.(*os.PathError); ok {
		return pe.Op + ":" + pe.Err.Error()
	}
	return err.Error()
}

type openErrorFS struct{ afero.Fs }

func (openErrorFS) OpenFile(string, int, os.FileMode) (afero.File, error) {
	return nil, os.ErrInvalid
}

func (openErrorFS) Create(string) (afero.File, error) { return nil, os.ErrInvalid }

func exerciseFaultContracts(t *testing.T, data []byte) {
	t.Helper()

	if FromBytes(nil).Active() || (*Schedule)(nil).Active() {
		t.Fatal("empty schedules must be inactive")
	}
	if err := (*Schedule)(nil).trip(); err != nil {
		t.Fatalf("nil schedule tripped: %v", err)
	}
	base := afero.NewMemMapFs()
	if FS(base, nil) != base {
		t.Fatal("inactive filesystem decorator did not return its base")
	}
	baseRT := &bodyRT{}
	if RoundTripper(baseRT, nil) != baseRT {
		t.Fatal("inactive transport decorator did not return its base")
	}

	failByte := byte(0)
	if len(data) > 0 {
		failByte = data[0] &^ 3
	}
	failPlan := []byte{failByte}
	passPlan := []byte{failByte | 1}
	if err := FromBytes(passPlan).trip(); err != nil {
		t.Fatalf("pass schedule tripped: %v", err)
	}
	if err := FromBytes(failPlan).trip(); err == nil {
		t.Fatal("fail schedule passed")
	}

	req, err := http.NewRequest(http.MethodPost, "http://example.test", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp, err := RoundTripper(baseRT, FromBytes(failPlan)).RoundTrip(req); resp != nil || err == nil {
		t.Fatalf("faulted nil-body request = (%v, %v)", resp, err)
	}
	body := &trackBody{r: bytes.NewReader([]byte("body"))}
	req, _ = http.NewRequest(http.MethodPost, "http://example.test", body)
	if resp, err := RoundTripper(baseRT, FromBytes(failPlan)).RoundTrip(req); resp != nil || err == nil || !body.closed {
		t.Fatalf("faulted body request = (%v, %v), closed=%v", resp, err, body.closed)
	}
	if resp, err := RoundTripper(baseRT, FromBytes(passPlan)).RoundTrip(req); err != nil || resp == nil {
		t.Fatalf("passing request = (%v, %v)", resp, err)
	}

	exerciseFilesystemFaults(t, failPlan)
	exerciseFilesystemDelegation(t, passPlan)
	exerciseFileFaults(t, failByte)
}

func exerciseFilesystemFaults(t *testing.T, failPlan []byte) {
	t.Helper()
	fs := FS(afero.NewMemMapFs(), FromBytes(failPlan))
	now := time.Unix(1, 0)
	checks := []func() error{
		func() error { _, err := fs.Open("/x"); return err },
		func() error { _, err := fs.OpenFile("/x", os.O_CREATE, 0o600); return err },
		func() error { _, err := fs.Create("/x"); return err },
		func() error { return fs.Mkdir("/x", 0o755) },
		func() error { return fs.MkdirAll("/x/y", 0o755) },
		func() error { return fs.Remove("/x") },
		func() error { return fs.RemoveAll("/x") },
		func() error { return fs.Rename("/x", "/y") },
		func() error { _, err := fs.Stat("/x"); return err },
		func() error { return fs.Chmod("/x", 0o600) },
		func() error { return fs.Chtimes("/x", now, now) },
	}
	for i, check := range checks {
		if err := check(); err == nil {
			t.Fatalf("filesystem fault check %d passed", i)
		}
	}
}

func exerciseFilesystemDelegation(t *testing.T, passPlan []byte) {
	t.Helper()
	base := afero.NewMemMapFs()
	fs := FS(base, FromBytes(passPlan))
	now := time.Unix(1, 0)
	if _, err := fs.Open("/missing"); err == nil {
		t.Fatal("delegated Open error disappeared")
	}
	errorFS := FS(openErrorFS{Fs: base}, FromBytes(passPlan))
	if _, err := errorFS.OpenFile("/f", os.O_CREATE, 0o600); err == nil {
		t.Fatal("delegated OpenFile error disappeared")
	}
	if _, err := errorFS.Create("/f"); err == nil {
		t.Fatal("delegated Create error disappeared")
	}
	if err := fs.Mkdir("/d", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := fs.MkdirAll("/a/b", 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := fs.Create("/d/f")
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	if _, err := fs.Open("/d/f"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.OpenFile("/d/f", os.O_RDWR, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Stat("/d/f"); err != nil {
		t.Fatal(err)
	}
	if err := fs.Chmod("/d/f", 0o640); err != nil {
		t.Fatal(err)
	}
	if err := fs.Chtimes("/d/f", now, now); err != nil {
		t.Fatal(err)
	}
	if err := fs.Rename("/d/f", "/d/g"); err != nil {
		t.Fatal(err)
	}
	if err := fs.Remove("/d/g"); err != nil {
		t.Fatal(err)
	}
	if err := fs.RemoveAll("/a"); err != nil {
		t.Fatal(err)
	}
}

func exerciseFileFaults(t *testing.T, failByte byte) {
	t.Helper()
	base := afero.NewMemMapFs()
	if err := afero.WriteFile(base, "/f", []byte("abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}
	open := func(fail bool) afero.File {
		plan := []byte{1, 1}
		if fail {
			plan[1] = failByte
		}
		file, err := FS(base, FromBytes(plan)).OpenFile("/f", os.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		return file
	}
	operations := []func(afero.File) error{
		func(f afero.File) error { _, err := f.Read(make([]byte, 2)); return err },
		func(f afero.File) error { _, err := f.ReadAt(make([]byte, 2), 0); return err },
		func(f afero.File) error { _, err := f.Write([]byte("x")); return err },
		func(f afero.File) error { _, err := f.WriteAt([]byte("y"), 1); return err },
		func(f afero.File) error { _, err := f.Seek(0, io.SeekStart); return err },
		func(f afero.File) error { return f.Truncate(3) },
		func(f afero.File) error { return f.Sync() },
	}
	for i, operation := range operations {
		if err := operation(open(true)); err == nil {
			t.Fatalf("file fault operation %d passed", i)
		}
		if err := operation(open(false)); err != nil {
			t.Fatalf("file delegation operation %d failed: %v", i, err)
		}
	}
}
