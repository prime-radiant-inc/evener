package agent

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
)

// A closed output file can still hold bytes older than the retention cap;
// the closed-file reader starts at the first visible lifetime offset and
// never returns anything before it.
func TestClosedOutputFileReadersStartAtTheVisibleStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.log")
	// Two caps of output stay in the file before compaction; readers see only
	// the last cap.
	out, err := jobstore.OpenOutput(path, int64(len("new-1\nnew-2\n")))
	if err != nil {
		t.Fatal(err)
	}
	for _, chunk := range []string{"old-a\nold-b\n", "new-1\nnew-2\n"} {
		if _, err := out.Append([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	total := int64(len("old-a\nold-b\nnew-1\nnew-2\n"))

	tail, gotTotal, truncated, err := readClosedJobOutput(path, nil, 1024, false)
	if err != nil || tail != "new-1\nnew-2\n" || gotTotal != total || !truncated {
		t.Fatalf("tail = %q, %d, %v, %v; want the visible bytes, truncated", tail, gotTotal, truncated, err)
	}
	head, _, truncated, err := readClosedJobOutput(path, nil, 6, true)
	if err != nil || head != "new-1\n" || !truncated {
		t.Fatalf("head = %q, %v, %v; want the first visible line, truncated", head, truncated, err)
	}
}

// Closed job output is read through one opened regular file, never through a
// symlink planted at the output path.
func TestReadOutputRefusesASymlinkedOutputFile(t *testing.T) {
	jm := newTestJM(t)
	t.Cleanup(func() { _ = jm.close() })
	const jobID = "job_symlinked_output"
	target := filepath.Join(t.TempDir(), "elsewhere.log")
	if err := os.WriteFile(target, []byte("not this job's output\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(jm.dir, "jobs", jobID+".log")
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, outputPath); err != nil {
		t.Fatal(err)
	}
	start := time.Unix(1, 0).UTC()
	if err := jm.store.Append(jobstore.Event{
		Kind: jobstore.EventJobStarted, JobID: jobID, Type: jobstore.JobShell, Status: jobstore.StatusRunning,
		OwnerSessionID: testOwnerSessionID, VisibleToSession: testOwnerSessionID, StartedAt: &start, OutputPath: outputPath,
	}); err != nil {
		t.Fatal(err)
	}

	if content, _, _, err := jm.readOutput(jobID, 1024); err == nil {
		t.Fatalf("readOutput through a symlink = %q, nil; want an error", content)
	}
	if content, _, _, err := jm.readOutputHead(jobID, 1024); err == nil {
		t.Fatalf("readOutputHead through a symlink = %q, nil; want an error", content)
	}
	if matches, err := jm.grepOutput(jobID, regexp.MustCompile(`output`)); err == nil {
		t.Fatalf("grepOutput through a symlink = %+v, nil; want an error", matches)
	}
}

// A finished job's output file that grew past the lifetime total its record
// pins is refused rather than served with the extra bytes.
func TestReadOutputRefusesAClosedFileThatGrewPastItsRecord(t *testing.T) {
	jm := newTestJM(t)
	t.Cleanup(func() { _ = jm.close() })
	const jobID = "job_grown_output"
	outputPath := writeFinishedJobWithOutput(t, jm, jobID, jobstore.JobShell, "done\n")
	f, err := os.OpenFile(outputPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("later\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	if content, _, _, err := jm.readOutput(jobID, 1024); err == nil {
		t.Fatalf("readOutput of a grown closed file = %q, nil; want an error", content)
	}
	if content, _, _, err := jm.readOutputHead(jobID, 1024); err == nil {
		t.Fatalf("readOutputHead of a grown closed file = %q, nil; want an error", content)
	}
}
