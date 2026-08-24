package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
)

// allocatedSessionScratch provisions the same per-session scratch directory
// that the command layer exports, then returns it for calls through the real
// file-tool registry. Calling ExecCommand is intentional: SessionScratchDir is
// a reporting accessor and must not manufacture an authorization grant merely
// because a caller asks for the path.
func allocatedSessionScratch(t *testing.T, dir string) (*Session, *execenv.LocalExecutionEnvironment, string) {
	t.Helper()
	s := newSession(t, withDir(dir), withoutGitSnapshot())
	env, ok := s.env.(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatalf("session env type %T, want *LocalExecutionEnvironment", s.env)
	}
	if _, err := env.ExecCommand(context.Background(), "true", 1000, "", nil); err != nil {
		t.Fatalf("allocate session scratch through real command path: %v", err)
	}
	scratch := env.SessionScratchDir()
	if scratch == "" {
		t.Fatal("real session did not report its allocated scratch directory")
	}
	t.Cleanup(func() {
		// Session teardown intentionally retains scratch for human inspection;
		// this fixture owns its allocation and must not leave test artifacts in
		// the shared harness scratch root.
		env.Cleanup()
		_ = os.RemoveAll(scratch)
	})
	return s, env, scratch
}

func fileToolCall(t *testing.T, s *Session, name string, args any) toolExecResult {
	t.Helper()
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal %s args: %v", name, err)
	}
	res := s3cov_exec(t, s, name, string(encoded))
	return toolExecResult{isError: res.IsError, output: res.Output}
}

// toolExecResult keeps the assertions below independent of the registry's
// provider-shaped result details while still using its real ExecuteCall path.
type toolExecResult struct {
	isError bool
	output  string
}

func TestFileToolsAllowOwnAllocatedScratch(t *testing.T) {
	workspace := t.TempDir()
	s, _, scratch := allocatedSessionScratch(t, workspace)
	target := filepath.Join(scratch, "probe.txt")

	write := fileToolCall(t, s, "write_file", map[string]string{
		"file_path": target,
		"content":   "before\n",
	})
	if write.isError {
		t.Fatalf("write_file must create a file in this session's allocated scratch: %s", write.output)
	}

	edit := fileToolCall(t, s, "edit_file", map[string]string{
		"file_path":  target,
		"old_string": "before",
		"new_string": "after",
	})
	if edit.isError {
		t.Fatalf("edit_file must edit a file in this session's allocated scratch: %s", edit.output)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read edited scratch file: %v", err)
	}
	if string(got) != "after\n" {
		t.Fatalf("edited scratch content = %q, want %q", got, "after\n")
	}
}

func TestFileToolsDenyUnallocatedAbsolutePaths(t *testing.T) {
	workspace := t.TempDir()
	s, _, scratch := allocatedSessionScratch(t, workspace)
	otherRoot := t.TempDir()
	otherEnv := execenv.NewLocalExecutionEnvironment(otherRoot)
	if _, err := otherEnv.ExecCommand(context.Background(), "true", 1000, "", nil); err != nil {
		t.Fatalf("allocate other session scratch: %v", err)
	}
	otherScratch := otherEnv.SessionScratchDir()
	if otherScratch == "" || otherScratch == scratch {
		t.Fatalf("other session scratch = %q, own = %q; want distinct allocated roots", otherScratch, scratch)
	}
	t.Cleanup(func() {
		otherEnv.Cleanup()
		_ = os.RemoveAll(otherScratch)
	})
	parentFixture, err := os.MkdirTemp(filepath.Dir(scratch), "issue-368-parent-*")
	if err != nil {
		t.Fatalf("create unallocated parent fixture: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(parentFixture) })

	cases := []struct {
		name   string
		target string
	}{
		{name: "arbitrary", target: filepath.Join(t.TempDir(), "issue-368-arbitrary.txt")},
		{name: "scratch-parent", target: filepath.Join(parentFixture, "issue-368-parent.txt")},
		{name: "other-session", target: filepath.Join(otherScratch, "issue-368-other.txt")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := tc.target
			write := fileToolCall(t, s, "write_file", map[string]string{
				"file_path": target,
				"content":   "must stay absent\n",
			})
			if !write.isError {
				t.Fatalf("write_file unexpectedly allowed unallocated absolute path %q", target)
			}
			if !strings.Contains(write.output, "outside working directory") {
				t.Fatalf("write_file denial for %q = %q, want confinement error", target, write.output)
			}
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Fatalf("denied write changed %q: stat err=%v", target, err)
			}

			if err := os.WriteFile(target, []byte("original\n"), 0o644); err != nil {
				t.Fatalf("seed unallocated edit target: %v", err)
			}
			edit := fileToolCall(t, s, "edit_file", map[string]string{
				"file_path":  target,
				"old_string": "original",
				"new_string": "must stay unchanged",
			})
			if !edit.isError {
				t.Fatalf("edit_file unexpectedly allowed unallocated absolute path %q", target)
			}
			got, err := os.ReadFile(target)
			if err != nil {
				t.Fatalf("read denied edit target %q: %v", target, err)
			}
			if string(got) != "original\n" {
				t.Fatalf("denied edit changed %q to %q", target, got)
			}
		})
	}
}

func TestFileToolsDenySymlinkEscapeFromAllocatedScratch(t *testing.T) {
	workspace := t.TempDir()
	s, _, scratch := allocatedSessionScratch(t, workspace)
	outside := t.TempDir()
	link := filepath.Join(scratch, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("create scratch escape symlink: %v", err)
	}

	writeTarget := filepath.Join(link, "write.txt")
	write := fileToolCall(t, s, "write_file", map[string]string{
		"file_path": writeTarget,
		"content":   "must stay absent\n",
	})
	if !write.isError {
		t.Fatalf("write_file followed scratch symlink to %q", outside)
	}
	if _, err := os.Stat(filepath.Join(outside, "write.txt")); !os.IsNotExist(err) {
		t.Fatalf("symlink-denied write changed outside target: stat err=%v", err)
	}

	editTarget := filepath.Join(outside, "edit.txt")
	if err := os.WriteFile(editTarget, []byte("original\n"), 0o644); err != nil {
		t.Fatalf("seed outside edit target: %v", err)
	}
	edit := fileToolCall(t, s, "edit_file", map[string]string{
		"file_path":  filepath.Join(link, "edit.txt"),
		"old_string": "original",
		"new_string": "changed",
	})
	if !edit.isError {
		t.Fatalf("edit_file followed scratch symlink to %q", outside)
	}
	got, err := os.ReadFile(editTarget)
	if err != nil {
		t.Fatalf("read outside edit target: %v", err)
	}
	if string(got) != "original\n" {
		t.Fatalf("symlink-denied edit changed outside target to %q", got)
	}
}

func TestFileToolsPinAllocatedScratchAcrossRootSwap(t *testing.T) {
	workspace := t.TempDir()
	s, _, scratch := allocatedSessionScratch(t, workspace)
	first := filepath.Join(scratch, "first.txt")
	write := fileToolCall(t, s, "write_file", map[string]string{
		"file_path": first,
		"content":   "before root swap\n",
	})
	if write.isError {
		t.Fatalf("initial scratch write: %s", write.output)
	}

	moved := scratch + "-moved"
	if err := os.Rename(scratch, moved); err != nil {
		t.Fatalf("move allocated scratch root: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Remove(scratch)
		_ = os.RemoveAll(moved)
	})
	outside := t.TempDir()
	if err := os.Symlink(outside, scratch); err != nil {
		t.Fatalf("replace scratch root with symlink: %v", err)
	}

	second := fileToolCall(t, s, "write_file", map[string]string{
		"file_path": filepath.Join(scratch, "after.txt"),
		"content":   "must stay in pinned root\n",
	})
	if second.isError {
		t.Fatalf("write through swapped scratch spelling: %s", second.output)
	}
	if _, err := os.Stat(filepath.Join(outside, "after.txt")); !os.IsNotExist(err) {
		t.Fatalf("root swap redirected write outside the allocated root: stat err=%v", err)
	}
	got, err := os.ReadFile(filepath.Join(moved, "after.txt"))
	if err != nil {
		t.Fatalf("read pinned-root write: %v", err)
	}
	if string(got) != "must stay in pinned root\n" {
		t.Fatalf("pinned-root content = %q", got)
	}
}
