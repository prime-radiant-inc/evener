package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/llm"
)

func TestS3Cov_FormatDirListing(t *testing.T) {
	t.Parallel()

	t.Run("markers and full-count footer", func(t *testing.T) {
		t.Parallel()
		r := listDirResult{
			Entries: []execenv.DirEntry{
				{Name: "dir", IsDir: true},
				{Name: "link", IsSymlink: true, Size: 3},
				{Name: "prog", IsExec: true, Size: 9},
				{Name: "plain.txt", Size: 12},
			},
			Total: 4,
		}
		got := formatDirListing(r)
		for _, want := range []string{"dir/", "link@", "prog*", "plain.txt\t12", "4 entries"} {
			if !strings.Contains(got, want) {
				t.Errorf("listing missing %q:\n%s", want, got)
			}
		}
	})

	t.Run("truncated footer", func(t *testing.T) {
		t.Parallel()
		got := formatDirListing(listDirResult{
			Entries:   []execenv.DirEntry{{Name: "a"}},
			Total:     10,
			Returned:  1,
			Offset:    0,
			Truncated: true,
		})
		if !strings.Contains(got, "1 of 10 entries") || !strings.Contains(got, "list_dir(offset=1)") {
			t.Fatalf("truncated footer wrong:\n%s", got)
		}
	})

	t.Run("offset footer", func(t *testing.T) {
		t.Parallel()
		got := formatDirListing(listDirResult{
			Entries:  []execenv.DirEntry{{Name: "a"}},
			Total:    10,
			Returned: 1,
			Offset:   5,
		})
		if !strings.Contains(got, "1 of 10 entries (offset 5)") {
			t.Fatalf("offset footer wrong:\n%s", got)
		}
	})
}

func TestS3Cov_PaginateDirEntries(t *testing.T) {
	t.Parallel()

	entries := make([]execenv.DirEntry, 5)
	for i := range entries {
		entries[i] = execenv.DirEntry{Name: string(rune('a' + i))}
	}

	// Offset past the end yields an empty non-nil page.
	r := paginateDirEntries("p", entries, 100, 0)
	if r.Returned != 0 || r.Entries == nil || r.Truncated {
		t.Fatalf("offset-past-end: %+v", r)
	}

	// A small limit truncates.
	r = paginateDirEntries("p", entries, 0, 2)
	if r.Returned != 2 || !r.Truncated {
		t.Fatalf("limited page: %+v", r)
	}

	// Negative offset normalizes to 0.
	r = paginateDirEntries("p", entries, -3, 0)
	if r.Offset != 0 || r.Returned != 5 {
		t.Fatalf("negative offset: %+v", r)
	}
}

func s3cov_exec(t *testing.T, s *Session, name, argsJSON string) tool.ExecResult {
	t.Helper()
	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "c",
		Name:      name,
		Arguments: json.RawMessage(argsJSON),
	})
	return res
}

func TestS3Cov_ShellTools_ListGrepGlob(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"alpha.go":     "package main\nfunc needleFn() {}\n",
		"beta.txt":     "no match here\n",
		"sub/gamma.go": "package sub\n// needleFn referenced\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s := newSession(t, withDir(dir))

	t.Run("list_dir", func(t *testing.T) {
		res := s3cov_exec(t, s, "list_dir", `{"path":".","depth":1}`)
		if res.IsError {
			t.Fatalf("list_dir error: %v", res.Output)
		}
		out := res.Output
		if !strings.Contains(out, "alpha.go") || !strings.Contains(out, "sub/") {
			t.Fatalf("list_dir output: %s", out)
		}
	})

	t.Run("grep", func(t *testing.T) {
		res := s3cov_exec(t, s, "grep", `{"pattern":"needleFn","path":"."}`)
		if res.IsError {
			t.Fatalf("grep error: %v", res.Output)
		}
		if !strings.Contains(res.Output, "needleFn") {
			t.Fatalf("grep output: %s", res.Output)
		}
	})

	t.Run("glob", func(t *testing.T) {
		res := s3cov_exec(t, s, "glob", `{"pattern":"**/*.go","path":"."}`)
		if res.IsError {
			t.Fatalf("glob error: %v", res.Output)
		}
		if !strings.Contains(res.Output, ".go") {
			t.Fatalf("glob output: %s", res.Output)
		}
	})
}
