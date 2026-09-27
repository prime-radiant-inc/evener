package agent

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/identifier"
)

func TestLocalJobRetainedReadsRefusePostLocateLeafSwap(t *testing.T) {
	for _, replacement := range []string{"symlink", "fifo", "directory"} {
		for _, reader := range []string{"metadata", "window"} {
			t.Run(replacement+"/"+reader, func(t *testing.T) {
				bucket := localJobProjectBucket(t, t.TempDir(), localJobCurrentProject)
				owner := identifier.MustNewSessionID()
				jobID := identifier.MustNewJobID(owner)
				seedLocalJob(t, bucket, owner, jobID, "/dev/null", "ORIGINAL\n", false)

				target, err := locateLocalJobRetainedTarget(bucket, jobID)
				if err != nil {
					t.Fatalf("locate retained target: %v", err)
				}
				if err := os.Remove(target.OutputPath); err != nil {
					t.Fatalf("remove located output: %v", err)
				}
				switch replacement {
				case "symlink":
					swapped := filepath.Join(filepath.Dir(target.OutputPath), "swapped.log")
					writeAttackerOutputFixture(t, swapped, "SWAPPED CONTENT\n")
					copyOutputMetadata(t, swapped, target.OutputPath)
					if err := os.Symlink(swapped, target.OutputPath); err != nil {
						t.Skipf("symlinks unavailable: %v", err)
					}
				case "fifo":
					if _, err := exec.LookPath("mkfifo"); err != nil {
						t.Skip("mkfifo unavailable")
					}
					metadataSource := filepath.Join(filepath.Dir(target.OutputPath), "fifo-metadata-source.log")
					writeAttackerOutputFixture(t, metadataSource, "")
					copyOutputMetadata(t, metadataSource, target.OutputPath)
					if out, err := exec.Command("mkfifo", target.OutputPath).CombinedOutput(); err != nil {
						t.Skipf("mkfifo unavailable: %v (%s)", err, out)
					}
				case "directory":
					if err := os.Mkdir(target.OutputPath, 0o700); err != nil {
						t.Fatalf("create swapped directory: %v", err)
					}
				}

				type result struct {
					content []byte
					err     error
				}
				done := make(chan result, 1)
				go func() {
					if reader == "metadata" {
						got, err := readLocalJobRetainedMetadata(target)
						done <- result{content: got.Content, err: err}
						return
					}
					got, err := (localJobSearchSource{target: target}).ReadWindow(0, 1024)
					done <- result{content: got.Content, err: err}
				}()

				select {
				case got := <-done:
					if got.err == nil {
						t.Fatalf("post-locate %s swap was read: %q", replacement, got.content)
					}
					if strings.Contains(string(got.content), "SWAPPED") {
						t.Fatalf("read swapped content: %q (error %v)", got.content, got.err)
					}
					if !strings.Contains(got.err.Error(), "output_unavailable") {
						t.Fatalf("error = %v, want output_unavailable refusal", got.err)
					}
				// TRIPWIRE: secure non-blocking opens return immediately; three seconds is far above the expected time.
				case <-time.After(3 * time.Second):
					if replacement == "fifo" {
						unblock, err := os.OpenFile(target.OutputPath, os.O_RDWR, 0)
						if err == nil {
							_ = unblock.Close()
						}
					}
					t.Fatal("retained read blocked after post-locate leaf swap")
				}
			})
		}
	}
}

func writeAttackerOutputFixture(t *testing.T, path, content string) {
	t.Helper()
	output, err := jobstore.OpenOutputNoSync(path, 1024)
	if err != nil {
		t.Fatalf("open attacker output fixture: %v", err)
	}
	if content != "" {
		if _, err := output.Append([]byte(content)); err != nil {
			_ = output.Close()
			t.Fatalf("append attacker output fixture: %v", err)
		}
	}
	if err := output.Close(); err != nil {
		t.Fatalf("close attacker output fixture: %v", err)
	}
}

func copyOutputMetadata(t *testing.T, sourceOutputPath, destinationOutputPath string) {
	t.Helper()
	metadata, err := os.ReadFile(sourceOutputPath + ".meta.json")
	if err != nil {
		t.Fatalf("read attacker output metadata: %v", err)
	}
	if err := os.WriteFile(destinationOutputPath+".meta.json", metadata, 0o600); err != nil {
		t.Fatalf("replace output metadata: %v", err)
	}
}

func TestOpenJobOutputFileRefusesNonRegularLeaf(t *testing.T) {
	for _, replacement := range []string{"symlink", "fifo"} {
		t.Run(replacement, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "output.log")
			switch replacement {
			case "symlink":
				target := filepath.Join(dir, "target.log")
				if err := os.WriteFile(target, []byte("must not read\n"), 0o600); err != nil {
					t.Fatalf("write symlink target: %v", err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			case "fifo":
				if _, err := exec.LookPath("mkfifo"); err != nil {
					t.Skip("mkfifo unavailable")
				}
				if out, err := exec.Command("mkfifo", path).CombinedOutput(); err != nil {
					t.Skipf("mkfifo unavailable: %v (%s)", err, out)
				}
			}

			type result struct {
				r   io.ReadCloser
				err error
			}
			done := make(chan result, 1)
			go func() {
				r, err := openJobOutputFile(path, "")
				done <- result{r: r, err: err}
			}()
			select {
			case got := <-done:
				if got.err == nil {
					if got.r != nil {
						_ = got.r.Close()
					}
					t.Fatal("openJobOutputFile accepted a non-regular leaf")
				}
			// TRIPWIRE: secure non-blocking opens return immediately; three seconds is far above the expected time.
			case <-time.After(3 * time.Second):
				if replacement == "fifo" {
					unblock, err := os.OpenFile(path, os.O_RDWR, 0)
					if err == nil {
						_ = unblock.Close()
					}
				}
				t.Fatal("openJobOutputFile blocked on a FIFO")
			}
		})
	}
}

func TestReadLocalJobSnapshotUsesOutputReadSeam(t *testing.T) {
	bucket := localJobProjectBucket(t, t.TempDir(), localJobCurrentProject)
	owner := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(owner)
	seedLocalJob(t, bucket, owner, jobID, "/dev/null", "original\n", false)

	oldRead := readLocalJobOutputSnapshot
	t.Cleanup(func() { readLocalJobOutputSnapshot = oldRead })
	called := false
	readLocalJobOutputSnapshot = func(string, int, bool) (jobstore.OutputSnapshot, error) {
		called = true
		return jobstore.OutputSnapshot{Content: []byte("through seam\n"), TotalBytes: 13}, nil
	}

	got, err := readLocalJobSnapshot(bucket, jobID, 1024)
	if err != nil {
		t.Fatalf("readLocalJobSnapshot: %v", err)
	}
	if !called || got.Content != "through seam\n" {
		t.Fatalf("called=%v snapshot=%+v, want seam result", called, got)
	}
}
