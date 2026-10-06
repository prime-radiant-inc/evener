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

func TestLocalJobRetainedReadsRefusePostLocateIntermediateSymlinkSwap(t *testing.T) {
	for _, reader := range []string{"metadata", "window"} {
		t.Run(reader, func(t *testing.T) {
			bucket := localJobProjectBucket(t, t.TempDir(), localJobCurrentProject)
			owner := identifier.MustNewSessionID()
			jobID := identifier.MustNewJobID(owner)
			seedLocalJob(t, bucket, owner, jobID, "/dev/null", "ORIGINAL\n", false)

			target, err := locateLocalJobRetainedTarget(bucket, jobID)
			if err != nil {
				t.Fatalf("locate retained target: %v", err)
			}
			attackerJobs := filepath.Join(t.TempDir(), "jobs")
			if err := os.MkdirAll(attackerJobs, 0o700); err != nil {
				t.Fatalf("create attacker jobs dir: %v", err)
			}
			writeAttackerOutputFixture(t, filepath.Join(attackerJobs, filepath.Base(target.OutputPath)), "FAKE INTERMEDIATE CONTENT\n")

			jobsDir := filepath.Dir(target.OutputPath)
			if err := os.Rename(jobsDir, jobsDir+".honest"); err != nil {
				t.Fatalf("move honest jobs dir: %v", err)
			}
			if err := os.Symlink(attackerJobs, jobsDir); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
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
					t.Fatalf("post-locate intermediate symlink swap was read: %q", got.content)
				}
				if strings.Contains(string(got.content), "FAKE INTERMEDIATE") {
					t.Fatalf("read fake intermediate content: %q (error %v)", got.content, got.err)
				}
				if !strings.Contains(got.err.Error(), "output_unavailable") {
					t.Fatalf("error = %v, want output_unavailable refusal", got.err)
				}
			// TRIPWIRE: secure non-blocking descriptor walks return immediately; three seconds is far above the expected time.
			case <-time.After(3 * time.Second):
				t.Fatal("retained read blocked after post-locate intermediate symlink swap")
			}
		})
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
				f   *os.File
				err error
			}
			done := make(chan result, 1)
			go func() {
				f, err := openJobOutputFile(path)
				done <- result{f: f, err: err}
			}()
			select {
			case got := <-done:
				if got.err == nil {
					if got.f != nil {
						_ = got.f.Close()
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

func TestOpenJobOutputFileRefusesSymlinkedIntermediateDir(t *testing.T) {
	stateHome := t.TempDir()
	bucket := filepath.Join(stateHome, "evener", "projects", "deadbeef")
	realSessions := filepath.Join(t.TempDir(), "sessions")
	path := filepath.Join(bucket, "sessions", "owner", "jobs", "output.log")
	attackerPath := filepath.Join(realSessions, "owner", "jobs", "output.log")
	if err := os.MkdirAll(filepath.Dir(attackerPath), 0o700); err != nil {
		t.Fatalf("create attacker tree: %v", err)
	}
	if err := os.WriteFile(attackerPath, []byte("must not read\n"), 0o600); err != nil {
		t.Fatalf("write attacker output: %v", err)
	}
	if err := os.MkdirAll(bucket, 0o700); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	if err := os.Symlink(realSessions, filepath.Join(bucket, "sessions")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	f, err := openJobOutputFile(path)
	if f != nil {
		_ = f.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("openJobOutputFile error = %v, want intermediate symlink refusal", err)
	}
}

func TestOpenJobOutputFileOpensLegacyNamedBucket(t *testing.T) {
	stateHome := t.TempDir()
	path := filepath.Join(stateHome, "evener", "projects", "deadbeef", "sessions", "owner", "jobs", "output.log")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create legacy bucket tree: %v", err)
	}
	if err := os.WriteFile(path, []byte("legacy bucket output\n"), 0o600); err != nil {
		t.Fatalf("write output: %v", err)
	}

	f, err := openJobOutputFile(path)
	if err != nil {
		t.Fatalf("openJobOutputFile: %v", err)
	}
	defer func() { _ = f.Close() }()
	got, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if string(got) != "legacy bucket output\n" {
		t.Fatalf("content = %q, want legacy bucket output", got)
	}
}

func TestOpenJobOutputFileRefusesRegularReplacementDuringOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output.log")
	if err := os.WriteFile(path, []byte("original\n"), 0o600); err != nil {
		t.Fatalf("write original: %v", err)
	}

	originalLstat := lstatJobOutputFile
	t.Cleanup(func() { lstatJobOutputFile = originalLstat })
	lstatJobOutputFile = func(name string) (os.FileInfo, error) {
		info, err := originalLstat(name)
		if err != nil || name != path {
			return info, err
		}
		replacement := path + ".replacement"
		if err := os.WriteFile(replacement, []byte("replacement\n"), 0o600); err != nil {
			return nil, err
		}
		if err := os.Rename(replacement, path); err != nil {
			return nil, err
		}
		return info, nil
	}

	f, err := openJobOutputFile(path)
	if f != nil {
		_ = f.Close()
	}
	if err == nil {
		t.Fatal("openJobOutputFile accepted a regular replacement between Lstat and open")
	}
}

func TestReadLocalJobOutputSnapshotsRetryWithFreshDescriptor(t *testing.T) {
	for _, api := range []string{"snapshot", "window"} {
		t.Run(api, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "output.log")
			output, err := jobstore.CreateOutputNoSync(path, 4)
			if err != nil {
				t.Fatalf("create output: %v", err)
			}
			t.Cleanup(func() { _ = output.Close() })
			if _, err := output.Append([]byte("AAAA")); err != nil {
				t.Fatalf("append original: %v", err)
			}
			stale, err := os.Open(path)
			if err != nil {
				t.Fatalf("open pre-prune output: %v", err)
			}
			// Five bytes take the file past twice the cap, so this append
			// compacts and replaces the file the stale descriptor holds.
			if _, err := output.Append([]byte("BBBBB")); err != nil {
				_ = stale.Close()
				t.Fatalf("append replacement: %v", err)
			}

			originalOpen := openJobOutputFile
			t.Cleanup(func() { openJobOutputFile = originalOpen })
			opens := 0
			openJobOutputFile = func(name string) (*os.File, error) {
				opens++
				if opens == 1 {
					return stale, nil
				}
				return originalOpen(name)
			}

			var content []byte
			if api == "snapshot" {
				got, err := readLocalJobOutputSnapshot(path, 4, false)
				if err != nil {
					t.Fatalf("readLocalJobOutputSnapshot: %v", err)
				}
				content = got.Content
			} else {
				got, err := readLocalJobOutputWindowSnapshot(path, output.RetainedStart(), 4)
				if err != nil {
					t.Fatalf("readLocalJobOutputWindowSnapshot: %v", err)
				}
				content = got.Content
			}
			if opens != 2 || string(content) != "BBBB" {
				t.Fatalf("opens=%d content=%q, want two opens and post-prune bytes", opens, content)
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
