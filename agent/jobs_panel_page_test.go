package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/identifier"
)

func TestJobOutputPageSavedBoundsAndRawBytes(t *testing.T) {
	t.Parallel()
	dir, sessionID, jobID, path := newSavedPageJob(t, []byte("abécd"), 6, 1024)
	for _, tc := range []struct {
		name   string
		before *int64
		max    int64
		start  int64
		bytes  []byte
	}{
		{"latest split", nil, 3, 3, []byte{0xa9, 0x63, 0x64}},
		{"backward split", pageBefore(3), 3, 0, []byte{0x61, 0x62, 0xc3}},
		{"complete", nil, 100, 0, []byte("abécd")},
		{"explicit zero", pageBefore(0), 3, 0, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page, found, err := LoadSessionJobOutputPage(dir, sessionID, jobID, tc.before, tc.max)
			if err != nil || !found {
				t.Fatalf("page: found=%v, error %v", found, err)
			}
			if page.OffsetBytes != tc.start || page.BytesReturned != int64(len(tc.bytes)) || page.TotalBytes != 6 || page.RetainedStartBytes != 0 || !bytes.Equal(rawPageBytes(t, page), tc.bytes) {
				t.Fatalf("page = %+v, want %x at %d, total 6, floor 0", page, tc.bytes, tc.start)
			}
		})
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	_, found, err := LoadSessionJobOutputPage(dir, sessionID, jobID, nil, 4)
	if !found || err == nil {
		t.Fatalf("missing output found=%v, error %v, want transient read failure", found, err)
	}
}

func TestJobOutputPageSavedPruningAndLimits(t *testing.T) {
	t.Parallel()
	dir, sessionID, jobID, _ := newSavedPageJob(t, bytes.Repeat([]byte("s"), 200), 200, 100)
	for _, before := range []int64{0, 99} {
		_, found, err := LoadSessionJobOutputPage(dir, sessionID, jobID, &before, 64)
		var wire appwire.WireError
		if !found || !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable {
			t.Fatalf("pruned page found=%v, error %v, want typed unavailable", found, err)
		}
		data, marshalErr := json.Marshal(wire.Data)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if string(data) != `{"evenerErrorInfo":"jobOutputPruned","retainedStartBytes":100,"totalBytes":200}` {
			t.Fatalf("pruning bounds = %s", data)
		}
	}
	page, found, err := LoadSessionJobOutputPage(dir, sessionID, jobID, pageBefore(100), 64)
	if err != nil || !found || page.OffsetBytes != 100 || page.BytesReturned != 0 || page.TotalBytes != 200 || page.RetainedStartBytes != 100 {
		t.Fatalf("floor page = %+v, found=%v, error %v", page, found, err)
	}
	for _, before := range []int64{-1, 201} {
		_, found, err := LoadSessionJobOutputPage(dir, sessionID, jobID, &before, 64)
		var wire appwire.WireError
		if !found || !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
			t.Fatalf("invalid selector found=%v, error %v, want invalid parameters", found, err)
		}
	}
	dir, sessionID, jobID, _ = newSavedPageJob(t, bytes.Repeat([]byte("a"), 80000), 80000, 80000)
	for _, tc := range []struct{ max, count int64 }{{0, 4096}, {-1, 4096}, {100000, 65536}} {
		page, found, err := LoadSessionJobOutputPage(dir, sessionID, jobID, nil, tc.max)
		if err != nil || !found || page.OffsetBytes != 80000-tc.count || page.BytesReturned != tc.count || page.TotalBytes != 80000 || page.RetainedStartBytes != 0 || !bytes.Equal(rawPageBytes(t, page), bytes.Repeat([]byte("a"), int(tc.count))) {
			t.Fatalf("limit %d page = %+v, found=%v, error %v", tc.max, page, found, err)
		}
	}
}

func TestJobOutputPageRealShellLiveAndSettled(t *testing.T) {
	t.Parallel()
	jm, executor := newShellTestRig(t)
	t.Cleanup(func() { _ = jm.close() })
	dir := t.TempDir()
	gate, ready := filepath.Join(dir, "gate"), filepath.Join(dir, "ready")
	command := fmt.Sprintf("mkfifo %q; printf 'ab\\303\\251cd'; : > %q; cat %q >/dev/null; printf FINAL", gate, ready, gate)
	result := runShell(context.Background(), jm, executor, shellArgs{Command: command, Background: true})
	if result.JobID == "" || !result.RunningInBackground {
		t.Fatalf("background start = %+v", result)
	}
	session := &Session{}
	session.jobManager = jm
	// TRIPWIRE: six bytes arrive before the shell blocks on its gate; 30s detects a stuck launch or output drain.
	waitForCondition(t, 30*time.Second, "real shell to write its complete first output", func() bool {
		page, found, err := session.JobOutputPage(result.JobID, nil, 6)
		return err == nil && found && page.TotalBytes == 6
	})
	page, found, err := session.JobOutputPage(result.JobID, nil, 3)
	if err != nil || !found || page.OffsetBytes != 3 || page.BytesReturned != 3 || page.TotalBytes != 6 || page.RetainedStartBytes != 0 || !bytes.Equal(rawPageBytes(t, page), []byte{0xa9, 0x63, 0x64}) {
		t.Fatalf("live page = %+v, found=%v, error %v", page, found, err)
	}
	// TRIPWIRE: readiness is written immediately after those bytes; 30s catches a shell stuck before opening its gate.
	waitForCondition(t, 30*time.Second, "real shell to open its output gate", func() bool {
		_, err := os.Stat(ready)
		return err == nil
	})
	f, err := os.OpenFile(gate, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("release\n"); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	waitForShellDone(t, jm, result.JobID)
	page, found, err = session.JobOutputPage(result.JobID, nil, 5)
	if err != nil || !found || page.OffsetBytes != 6 || page.BytesReturned != 5 || page.TotalBytes != 11 || string(rawPageBytes(t, page)) != "FINAL" {
		t.Fatalf("settled page = %+v, found=%v, error %v", page, found, err)
	}
}

func newSavedPageJob(t *testing.T, content []byte, total, retentionCap int64) (dir, sessionID, jobID, path string) {
	t.Helper()
	dir, sessionID, jobID = t.TempDir(), identifier.MustNewSessionID(), "job_page"
	path = filepath.Join(jobsDir(dir, sessionID), "jobs", jobID+".log")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	output, err := jobstore.CreateOutputNoSync(path, retentionCap)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := output.Append(content); err != nil {
		_ = output.Close()
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := jobstore.OpenNoSync(filepath.Join(jobsDir(dir, sessionID), "jobs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	for _, event := range []jobstore.Event{
		{Kind: jobstore.EventJobStarted, TS: now, JobID: jobID, Type: jobstore.JobShell, Status: jobstore.StatusRunning, OwnerSessionID: sessionID, StartedAt: &now, OutputPath: path},
		{Kind: jobstore.EventJobFinished, TS: now, JobID: jobID, Status: jobstore.StatusCompleted, OutputBytes: total, TerminalGen: "page-generation"},
	} {
		if err := store.Append(event); err != nil {
			t.Fatal(err)
		}
	}
	return dir, sessionID, jobID, path
}

func rawPageBytes(t *testing.T, page appwire.JobOutputPage) []byte {
	t.Helper()
	switch page.Encoding {
	case "utf8":
		return []byte(page.Data)
	case "base64":
		bytes, err := base64.StdEncoding.DecodeString(page.Data)
		if err != nil {
			t.Fatal(err)
		}
		return bytes
	default:
		t.Fatalf("unknown output encoding %q", page.Encoding)
		return nil
	}
}

func pageBefore(before int64) *int64 { return new(before) }
