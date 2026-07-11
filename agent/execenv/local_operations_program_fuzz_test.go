//go:build serffuzz

package execenv

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
	"primeradiant.com/serf/fuzz/fault"
)

// FuzzLocalFilesystemOperationProgram drives the public local filesystem tool
// surface as one deterministic program: ReadFile, WriteFile, EditFile,
// FileExists, ListDirectory, Glob, and Grep. Each run owns a fresh temp root.
// Grep is forced through its native fallback at the existing lookup seam, so
// this target never consults PATH or starts ripgrep, a shell, Git, or a network
// client. A separate injected-afero lane reaches write failure handling without
// touching the host filesystem.
func FuzzLocalFilesystemOperationProgram(f *testing.F) {
	for _, seed := range [][]byte{
		nil,
		{0},
		{1, 2, 3},
		{0xff, 0x00, 0x4a, 0x91},
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, program []byte) {
		// The program only supplies fixture content and a final grep-mode selector;
		// fixed operations below cover the public contract every replay.
		if len(program) > 48 {
			program = program[:48]
		}
		first := runLocalFilesystemOperationProgram(t, program)
		second := runLocalFilesystemOperationProgram(t, program)
		if !reflect.DeepEqual(first, second) {
			t.Fatalf("local filesystem operation program is not deterministic:\nfirst=%#v\nsecond=%#v", first, second)
		}
	})
}

type localFilesystemOperationTrace struct {
	Reads  []string
	Writes []string
	Edits  []string
	Exists []bool
	Lists  [][]DirEntry
	Globs  [][]string
	Greps  []string
	Faults []string
}

func runLocalFilesystemOperationProgram(t *testing.T, program []byte) localFilesystemOperationTrace {
	t.Helper()
	root := t.TempDir()
	token := localFilesystemProgramToken(program)
	env := NewLocalExecutionEnvironment(root)
	trace := localFilesystemOperationTrace{}

	readme := "alpha " + token + "\nneedle " + token + "\nthird line\n"
	localFilesystemProgramWrite(t, env, root, &trace, "docs/readme.txt", readme)
	localFilesystemProgramWrite(t, env, root, &trace, "pkg/main.go", "package fixture\n// NEEDLE "+token+"\n")
	localFilesystemProgramWrite(t, env, root, &trace, "pkg/deep/note.md", "needle deep "+token+"\n")
	localFilesystemProgramWrite(t, env, root, &trace, "repeat.txt", "duplicate\nduplicate\n")
	localFilesystemProgramWrite(t, env, root, &trace, "fuzzy.txt", "first line\n  fuzzy    target  \nlast line\n")
	localFilesystemProgramWrite(t, env, root, &trace, "fuzzy-multi.txt", "first line\n  multi    fuzzy\nsecond   line\nlast line\n")
	localFilesystemProgramWrite(t, env, root, &trace, ".hidden.txt", "needle hidden\n")
	localFilesystemProgramWrite(t, env, root, &trace, ".hidden/nested.txt", "needle hidden nested\n")
	localFilesystemProgramWrite(t, env, root, &trace, "binary.bin", "needle\x00binary")
	localFilesystemProgramWrite(t, env, root, &trace, "graphic.png", string([]byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x01}))
	localFilesystemProgramWrite(t, env, root, &trace, "report.data", "%PDF-1.7\n"+token)
	localFilesystemProgramWrite(t, env, root, &trace, "run.sh", "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(root, "run.sh"), 0o755); err != nil {
		t.Fatalf("chmod fixture: %v", err)
	}
	localFilesystemProgramWrite(t, env, root, &trace, filepath.Join(root, "absolute.txt"), "absolute "+token)

	offset, limit := 2, 1
	window, err := env.ReadFile(filepath.Join(root, "docs/readme.txt"), &offset, &limit)
	if err != nil || window != fmt.Sprintf("   2\tneedle %s\n", token) {
		t.Fatalf("ReadFile window = %q, %v", window, err)
	}
	trace.Reads = append(trace.Reads, window)
	image, err := env.ReadFile("graphic.png", nil, nil)
	if err != nil || !strings.HasPrefix(image, "[image: png, 9 bytes, base64 data follows]\n") {
		t.Fatalf("ReadFile image = %q, %v", image, err)
	}
	trace.Reads = append(trace.Reads, image)
	document, err := env.ReadFile("report.data", nil, nil)
	if err != nil || !strings.HasPrefix(document, "[document: pdf,") {
		t.Fatalf("ReadFile document = %q, %v", document, err)
	}
	trace.Reads = append(trace.Reads, document)
	if _, err := env.ReadFile("binary.bin", nil, nil); err == nil || !strings.Contains(err.Error(), "binary file") {
		t.Fatalf("ReadFile binary error = %v", err)
	}
	if out, err := env.ReadFile("docs/readme.txt", intPtr(99), intPtr(1)); err != nil || out != "" {
		t.Fatalf("ReadFile past-end = %q, %v", out, err)
	}
	if _, err := env.ReadFile("missing.txt", nil, nil); err == nil {
		t.Fatal("ReadFile missing path unexpectedly succeeded")
	}

	exact, err := env.EditFile("docs/readme.txt", "alpha "+token, "updated "+token, false)
	if err != nil || !strings.Contains(exact, "1 replacement") {
		t.Fatalf("EditFile exact = %q, %v", exact, err)
	}
	trace.Edits = append(trace.Edits, exact)
	if _, err := env.EditFile("repeat.txt", "duplicate", "seen", false); err == nil || !strings.Contains(err.Error(), "not unique") {
		t.Fatalf("EditFile non-unique error = %v", err)
	}
	replaced, err := env.EditFile("repeat.txt", "duplicate", "seen", true)
	if err != nil || !strings.Contains(replaced, "2 replacements") {
		t.Fatalf("EditFile replace-all = %q, %v", replaced, err)
	}
	trace.Edits = append(trace.Edits, replaced)
	fuzzy, err := env.EditFile("fuzzy.txt", "fuzzy target", "normalized", false)
	if err != nil || !strings.Contains(fuzzy, "whitespace normalization") {
		t.Fatalf("EditFile fuzzy = %q, %v", fuzzy, err)
	}
	trace.Edits = append(trace.Edits, fuzzy)
	multi, err := env.EditFile("fuzzy-multi.txt", "multi fuzzy\nsecond line", "multi-normalized", false)
	if err != nil || !strings.Contains(multi, "whitespace normalization") {
		t.Fatalf("EditFile multi-line fuzzy = %q, %v", multi, err)
	}
	trace.Edits = append(trace.Edits, multi)
	if _, err := env.EditFile("docs/readme.txt", "updated "+token+" extra", "unused", false); err == nil || !strings.Contains(err.Error(), "nearest text") {
		t.Fatalf("EditFile nearest-region error = %v", err)
	}

	outside := filepath.Join(t.TempDir(), "outside.txt")
	if _, err := env.WriteFile("", "unused"); err == nil || !strings.Contains(err.Error(), "empty path") {
		t.Fatalf("WriteFile empty path error = %v", err)
	}
	if _, err := env.WriteFile(outside, "unsafe"); err == nil || !strings.Contains(err.Error(), "outside working directory") {
		t.Fatalf("WriteFile outside path error = %v", err)
	}
	if _, err := env.EditFile(outside, "old", "new", false); err == nil || !strings.Contains(err.Error(), "outside working directory") {
		t.Fatalf("EditFile outside path error = %v", err)
	}

	trace.Exists = append(trace.Exists,
		env.FileExists("docs/readme.txt"),
		env.FileExists(filepath.Join(root, "docs/readme.txt")),
		env.FileExists(""),
		env.FileExists("missing.txt"),
	)
	if !trace.Exists[0] || !trace.Exists[1] || !trace.Exists[2] || trace.Exists[3] {
		t.Fatalf("FileExists = %#v", trace.Exists)
	}

	shallow, err := env.ListDirectory("", 0)
	if err != nil || !localFilesystemProgramHasEntry(shallow, "docs") || localFilesystemProgramHasEntry(shallow, filepath.Join("pkg", "deep")) {
		t.Fatalf("ListDirectory shallow = %#v, %v", shallow, err)
	}
	deep, err := env.ListDirectory("pkg", 3)
	if err != nil || !localFilesystemProgramHasEntry(deep, filepath.Join("deep", "note.md")) || !localFilesystemProgramHasEntry(deep, "main.go") {
		t.Fatalf("ListDirectory deep = %#v, %v", deep, err)
	}
	if _, err := env.ListDirectory("missing", 1); err == nil {
		t.Fatal("ListDirectory missing path unexpectedly succeeded")
	}
	trace.Lists = append(trace.Lists, shallow, deep)

	for i, name := range []string{"order-a.txt", "order-b.txt", "order-new.txt"} {
		localFilesystemProgramWrite(t, env, root, &trace, name, "order "+name)
		stamp := time.Unix(1_700_000_000+int64(i), 0)
		if err := os.Chtimes(filepath.Join(root, name), stamp, stamp); err != nil {
			t.Fatalf("set fixture mtime: %v", err)
		}
	}
	ordered, err := env.Glob("order-*.txt", "")
	if err != nil {
		t.Fatalf("Glob ordered = %v", err)
	}
	if got, want := localFilesystemProgramRelativePaths(t, root, ordered), []string{"order-new.txt", "order-b.txt", "order-a.txt"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Glob mtime order = %v, want %v", got, want)
	}
	relativeGo, err := env.Glob("*.go", "pkg")
	if err != nil || !reflect.DeepEqual(localFilesystemProgramRelativePaths(t, root, relativeGo), []string{filepath.Join("pkg", "main.go")}) {
		t.Fatalf("Glob relative base = %v, %v", relativeGo, err)
	}
	absoluteGo, err := env.Glob("*.go", filepath.Join(root, "pkg"))
	if err != nil || !reflect.DeepEqual(localFilesystemProgramRelativePaths(t, root, absoluteGo), []string{filepath.Join("pkg", "main.go")}) {
		t.Fatalf("Glob absolute base = %v, %v", absoluteGo, err)
	}
	if matches, err := env.Glob("no-match-*.txt", ""); err != nil || len(matches) != 0 {
		t.Fatalf("Glob no-match = %v, %v", matches, err)
	}
	if _, err := env.Glob("[", ""); err == nil {
		t.Fatal("Glob malformed pattern unexpectedly succeeded")
	}
	trace.Globs = append(trace.Globs,
		localFilesystemProgramRelativePaths(t, root, ordered),
		localFilesystemProgramRelativePaths(t, root, relativeGo),
		localFilesystemProgramRelativePaths(t, root, absoluteGo),
	)

	trace.Greps = localFilesystemProgramGrep(t, env, token, program)
	trace.Faults = localFilesystemProgramWriteFaults(t, token)
	return trace
}

func localFilesystemProgramToken(program []byte) string {
	if len(program) == 0 {
		return "empty"
	}
	return base64.RawURLEncoding.EncodeToString(program)
}

func localFilesystemProgramWrite(t *testing.T, env *LocalExecutionEnvironment, root string, trace *localFilesystemOperationTrace, path, content string) {
	t.Helper()
	result, err := env.WriteFile(path, content)
	if err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
	want := fmt.Sprintf("wrote %d bytes to %s", len(content), path)
	if result != want {
		t.Fatalf("WriteFile(%q) = %q, want %q", path, result, want)
	}
	trace.Writes = append(trace.Writes, strings.ReplaceAll(result, root, "$ROOT"))
}

func localFilesystemProgramHasEntry(entries []DirEntry, name string) bool {
	for _, entry := range entries {
		if entry.Name == name {
			return true
		}
	}
	return false
}

func localFilesystemProgramRelativePaths(t *testing.T, root string, paths []string) []string {
	t.Helper()
	result := make([]string, len(paths))
	for i, path := range paths {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatalf("relative glob path %q: %v", path, err)
		}
		result[i] = rel
	}
	return result
}

func localFilesystemProgramGrep(t *testing.T, env *LocalExecutionEnvironment, token string, program []byte) []string {
	t.Helper()
	// This temporary lookup seam is intentionally scoped to one non-parallel fuzz
	// case. It makes Grep take its native-Go implementation before any PATH lookup
	// or command construction occurs.
	original := execLookPath
	execLookPath = func(string) (string, error) { return "", errors.New("local filesystem program disables ripgrep") }
	defer func() { execLookPath = original }()

	content, err := env.Grep("needle", "", "", false, 0, "")
	if err != nil || !strings.Contains(content, "docs/readme.txt:2:needle "+token) || strings.Contains(content, ".hidden") || strings.Contains(content, "binary.bin") {
		t.Fatalf("Grep content fallback = %q, %v", content, err)
	}
	files, err := env.Grep("needle", "pkg", "*.go", true, 10, "files_with_matches")
	if err != nil || files != "main.go" {
		t.Fatalf("Grep files-with-matches fallback = %q, %v", files, err)
	}
	counts, err := env.Grep("needle", "", "*.txt", false, 100, "count")
	if err != nil || !strings.Contains(counts, "docs/readme.txt:1") || strings.Contains(counts, ".hidden") {
		t.Fatalf("Grep count fallback = %q, %v", counts, err)
	}
	capped, err := env.Grep("needle", "", "", true, 1, "")
	if err != nil || capped == "" || strings.Count(capped, "\n") != 0 {
		t.Fatalf("Grep capped fallback = %q, %v", capped, err)
	}
	if _, err := env.Grep("[", "", "", false, 100, ""); err == nil || !strings.Contains(err.Error(), "invalid regex") {
		t.Fatalf("Grep invalid regex error = %v", err)
	}

	modes := []string{"", "files_with_matches", "count"}
	mode := modes[0]
	if len(program) > 0 {
		mode = modes[int(program[0])%len(modes)]
	}
	selected, err := env.Grep("needle", "", "*.txt", false, 3, mode)
	if err != nil {
		t.Fatalf("Grep selected mode %q: %v", mode, err)
	}
	return []string{content, files, counts, capped, selected}
}

func localFilesystemProgramWriteFaults(t *testing.T, token string) []string {
	t.Helper()
	results := make([]string, 0, 2)
	for _, tc := range []struct {
		name string
		plan []byte
	}{
		{name: "mkdir", plan: []byte{0}},
		{name: "write", plan: []byte{1, 0}},
	} {
		base := afero.NewMemMapFs()
		root := filepath.Join(t.TempDir(), "injected-filesystem")
		if err := base.MkdirAll(root, 0o755); err != nil {
			t.Fatalf("fault fixture root: %v", err)
		}
		env := NewLocalExecutionEnvironment(root).SetFs(fault.FS(base, fault.FromBytes(tc.plan)))
		if _, err := env.WriteFile("nested/result.txt", token); err == nil {
			t.Fatalf("WriteFile injected %s fault unexpectedly succeeded", tc.name)
		}
		results = append(results, tc.name)
	}
	return results
}

func intPtr(value int) *int { return &value }
