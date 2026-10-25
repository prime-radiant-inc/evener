package transcript

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
)

type identityBarrierFile struct {
	afero.File
	statErr, syncErr error
	syncs            int
}

func (f *identityBarrierFile) Stat() (os.FileInfo, error) {
	if f.statErr != nil {
		return nil, f.statErr
	}
	return f.File.Stat()
}

func (f *identityBarrierFile) Sync() error {
	f.syncs++
	if f.syncErr != nil {
		return f.syncErr
	}
	return f.File.Sync()
}

func TestEstablishDurabilityForFile(t *testing.T) {
	newFixture := func(t *testing.T) (*Writer, *identityBarrierFile, string) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "receiver.jsonl")
		w, err := NewWriter(path, Header{SessionID: "identity-receiver"})
		if err != nil {
			t.Fatal(err)
		}
		f := &identityBarrierFile{File: w.file}
		w.file = f
		t.Cleanup(func() { _ = w.Close() })
		return w, f, path
	}
	readerInfo := func(t *testing.T, path string) os.FileInfo {
		t.Helper()
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			t.Fatal(err)
		}
		return info
	}
	for _, alias := range []bool{false, true} {
		t.Run(map[bool]string{false: "same descriptor identity", true: "hard link alias"}[alias], func(t *testing.T) {
			w, f, path := newFixture(t)
			if alias {
				link := path + ".alias"
				if err := os.Link(path, link); err != nil {
					t.Fatal(err)
				}
				path = link
			}
			if err := w.EstablishDurabilityForFile(readerInfo(t, path)); err != nil {
				t.Fatal(err)
			}
			if f.syncs != 1 {
				t.Fatalf("matching file Syncs=%d want1", f.syncs)
			}
		})
	}
	t.Run("renamed writer and same-session replacement", func(t *testing.T) {
		w, f, path := newFixture(t)
		if err := os.Rename(path, path+".held"); err != nil {
			t.Fatal(err)
		}
		replacement, err := NewWriter(path, Header{SessionID: "identity-receiver"})
		if err != nil {
			t.Fatal(err)
		}
		if err := replacement.Close(); err != nil {
			t.Fatal(err)
		}
		if err := w.EstablishDurabilityForFile(readerInfo(t, path)); err == nil {
			t.Error("replacement accepted as original descriptor")
		}
		if f.syncs != 0 {
			t.Fatalf("mismatch Syncs=%d want0", f.syncs)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(path+".held", path); err != nil {
			t.Fatal(err)
		}
		if err := w.EstablishDurabilityForFile(readerInfo(t, path)); err != nil {
			t.Fatalf("restored identity: %v", err)
		}
		if f.syncs != 1 {
			t.Fatalf("restored file Syncs=%d want1", f.syncs)
		}
	})
	t.Run("descriptor Stat error", func(t *testing.T) {
		w, f, path := newFixture(t)
		info := readerInfo(t, path)
		failure := errors.New("descriptor stat unavailable")
		f.statErr = failure
		if err := w.EstablishDurabilityForFile(info); !errors.Is(err, failure) {
			t.Fatalf("Stat error=%v", err)
		}
		if f.syncs != 0 {
			t.Fatalf("failed Stat Syncs=%d want0", f.syncs)
		}
		// The original API has no identity check and must keep that behavior.
		if err := w.EstablishDurability(); err != nil {
			t.Fatalf("original barrier semantics changed: %v", err)
		}
		if f.syncs != 1 {
			t.Fatalf("original barrier Syncs=%d want1", f.syncs)
		}
	})
	t.Run("Sync error identity and recovery", func(t *testing.T) {
		w, f, path := newFixture(t)
		info := readerInfo(t, path)
		failure := errors.New("checked durability Sync unavailable")
		f.syncErr = failure
		w.dirty = true
		lastSync := w.lastSync
		for _, barrier := range []func() error{func() error { return w.EstablishDurabilityForFile(info) }, w.EstablishDurability} {
			if err := barrier(); !errors.Is(err, failure) {
				t.Fatalf("Sync error=%v", err)
			}
			if !w.dirty || !w.lastSync.Equal(lastSync) {
				t.Fatal("failed Sync cleared durability debt")
			}
		}
		f.syncErr = nil
		if err := w.EstablishDurabilityForFile(info); err != nil {
			t.Fatal(err)
		}
		if f.syncs != 3 || w.dirty {
			t.Fatalf("recovery Syncs=%d dirty=%t", f.syncs, w.dirty)
		}
	})
	t.Run("nil missing and closed writer", func(t *testing.T) {
		w, f, path := newFixture(t)
		info := readerInfo(t, path)
		if err := w.EstablishDurabilityForFile(nil); err == nil {
			t.Error("nil reader identity accepted")
		}
		if f.syncs != 0 {
			t.Fatalf("nil identity Syncs=%d want0", f.syncs)
		}
		for _, absent := range []*Writer{nil, {}} {
			if absent.EstablishDurability() == nil || absent.EstablishDurabilityForFile(info) == nil {
				t.Error("absent writer accepted")
			}
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		before := f.syncs
		if w.EstablishDurability() == nil || w.EstablishDurabilityForFile(info) == nil {
			t.Error("closed writer accepted")
		}
		if f.syncs != before {
			t.Fatal("closed writer attempted Sync")
		}
	})
}
