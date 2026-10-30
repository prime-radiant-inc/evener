package hubendpoint

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testRecord(root string) Record {
	return Record{SchemaVersion: 1, BootID: "boot-one", PID: os.Getpid(), StartedAt: time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC), ConfigPath: "/config/hub.toml", HubStateRoot: root, RequestedListenAddr: "127.0.0.1:0", ListenSource: "config", BoundAddr: "127.0.0.1:8181", LocalURL: "http://127.0.0.1:8181"}
}
func TestEndpointRecordGenerationAndMode(t *testing.T) {
	root := t.TempDir()
	r := testRecord(root)
	remove, err := Publish(root, r)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "hub-endpoint.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode: %v", info.Mode())
	}
	got, err := Read(root)
	if err != nil || got.BootID != r.BootID {
		t.Fatalf("record: %+v %v", got, err)
	}
	next := r
	next.BootID = "boot-two"
	later, err := Publish(root, next)
	if err != nil {
		t.Fatal(err)
	}
	if err := remove(); err != nil {
		t.Fatal(err)
	}
	got, err = Read(root)
	if err != nil || got.BootID != next.BootID {
		t.Fatalf("old retirement removed new generation: %+v %v", got, err)
	}
	if err := later(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retirement left record: %v", err)
	}
}
func TestEndpointRecordRejectsUnsafeFiles(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "hub-endpoint.json")
	r := testRecord(root)
	payload, _ := json.Marshal(r)
	for _, mode := range []os.FileMode{0666, 0620} {
		if err := os.WriteFile(path, payload, mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		if _, err := Read(root); err == nil {
			t.Fatalf("accepted %v", mode)
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "missing"), path); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(root); err == nil {
		t.Fatal("accepted symlink")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(root); err == nil {
		t.Fatal("accepted directory")
	}
}
func TestEndpointRecordReadBoundAndSchema(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "hub-endpoint.json")
	if err := os.WriteFile(path, bytes.Repeat([]byte("a"), 65537), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(root); err == nil {
		t.Fatal("oversize record accepted")
	}
	r := testRecord(root)
	r.SchemaVersion = 2
	payload, _ := json.Marshal(r)
	if err := os.WriteFile(path, payload, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(root); err == nil {
		t.Fatal("unsupported schema accepted")
	}
	if _, err := DecodeReport(strings.NewReader(strings.Repeat("x", 65537))); err == nil {
		t.Fatal("oversize report accepted")
	}
}
