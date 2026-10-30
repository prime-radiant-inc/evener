//go:build linux || darwin

package hubendpoint

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"syscall"
	"testing"
	"time"
)

type otherOwnerRecordInfo struct {
	os.FileInfo
	uid uint32
}

func (f otherOwnerRecordInfo) Sys() any { return &syscall.Stat_t{Uid: f.uid} }
func TestEndpointRecordRejectsOtherOwner(t *testing.T) {
	info, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if ownedByCurrentUser(otherOwnerRecordInfo{FileInfo: info, uid: uint32(os.Geteuid()) + 1}) {
		t.Fatal("accepted another OS user")
	}
}

func TestOpenEndpointRecordNonblocking(t *testing.T) {
	t.Run("fifo", func(t *testing.T) { testFIFORecordOpen(t, false) })
	t.Run("fifo-after-lstat", func(t *testing.T) { testFIFORecordOpen(t, true) })
	t.Run("regular", func(t *testing.T) {
		root := t.TempDir()
		record := testRecord(root)
		data, err := jsonBytes(record)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, recordName)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		f, err := openRecord(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			t.Fatal(err)
		}
		if err := checkRecordFile(info); err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(f)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("regular record read=%q error=%v", got, err)
		}
		read, err := Read(root)
		if err != nil || read.BootID != record.BootID || !read.StartedAt.Equal(record.StartedAt) {
			t.Fatalf("regular record=%+v error=%v", read, err)
		}
	})
}

func testFIFORecordOpen(t *testing.T, replace bool) {
	t.Helper()
	const rootEnv = "EVENER_TEST_ENDPOINT_FIFO_ROOT"
	const childEnv = "EVENER_TEST_ENDPOINT_FIFO_CHILD"
	// A CI-provided root must not make the parent take the child branch. The
	// subprocess marker keeps the parent's clear from erasing the child's root.
	if os.Getenv(childEnv) == "" {
		t.Setenv(rootEnv, "")
	}
	root := os.Getenv(rootEnv)
	if root == "" {
		// A subprocess bounds even a blocking syscall.Open and is reaped on
		// timeout, rather than leaving a blocked test goroutine behind.
		binary, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, "-test.run=^"+regexp.QuoteMeta(t.Name())+"$")
		cmd.Env = append(os.Environ(), rootEnv+"="+t.TempDir(), childEnv+"=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("FIFO open failed: deadline=%v error=%v output=%s", ctx.Err(), err, out)
		}
		return
	}
	path := filepath.Join(root, recordName)
	var before os.FileInfo
	if replace {
		if err := os.WriteFile(path, []byte("old regular record"), 0600); err != nil {
			t.Fatal(err)
		}
		var err error
		before, err = os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := checkRecordFile(before); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(path, path+".previous"); err != nil {
			t.Fatal(err)
		}
	}
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	f, err := openRecord(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeNamedPipe == 0 || info.Mode().IsRegular() {
		t.Fatalf("opened FIFO has mode=%v", info.Mode())
	}
	if before != nil && os.SameFile(before, info) {
		t.Fatal("replacement FIFO matches the previous regular descriptor identity")
	}
	if err := checkRecordFile(info); err == nil {
		t.Fatal("accepted an opened FIFO as a regular endpoint record")
	}
}
