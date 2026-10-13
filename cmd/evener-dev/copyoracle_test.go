package dev

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/internal/devtool/copyonly"
)

// copyOracleRepo is a throwaway git repository holding one Go copy-only test.
func copyOracleRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"pkg/x.go": "package x\nconst usage = \"Usage: evener\"\n",
		"pkg/x_test.go": "package x\nfunc TestHelpShowsUsage(t *testing.T) {\n" +
			"\tif !strings.Contains(help(), \"Usage: evener\") {\n\t\tt.Fatal(1)\n\t}\n}\n",
		"other/y_test.go": "package y\nfunc TestY(t *testing.T) {\n\tif out() != \"only in this test\" {\n\t\tt.Fatal(1)\n\t}\n}\n",
	}
	for path, body := range files {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The developer's git config (a global excludes file, templates) must not
	// decide which fixture files get tracked.
	for _, args := range [][]string{{"init", "-q", "--template="}, {"config", "core.excludesFile", ""}, {"add", "--", "."}} {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return root
}

// TestCopyOracleRepoTracksItsFilesDespiteTheDevelopersGitConfig gives the
// process a global excludes file that ignores every Go file and checks the
// fixture repository still tracks all of its files.
func TestCopyOracleRepoTracksItsFilesDespiteTheDevelopersGitConfig(t *testing.T) {
	home := t.TempDir()
	excludes := filepath.Join(home, "excludes")
	if err := os.WriteFile(excludes, []byte("*.go\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(home, "gitconfig")
	if err := os.WriteFile(config, []byte("[core]\n\texcludesFile = "+excludes+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", config)

	root := copyOracleRepo(t)
	out, err := exec.Command("git", "-C", root, "ls-files").Output()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"other/y_test.go", "pkg/x.go", "pkg/x_test.go"}
	if got := strings.Fields(string(out)); !slices.Equal(got, want) {
		t.Fatalf("tracked files = %q, want %q", got, want)
	}
}

// fakeTS stands in for the TypeScript half (a node process) and records the
// arguments it was asked for.
type fakeTS struct {
	report       copyonly.Report
	err          error
	calls        int
	root, prefix string
}

func (f *fakeTS) inventory(root, prefix string) (copyonly.Report, error) {
	f.calls++
	f.root, f.prefix = root, prefix
	return f.report, f.err
}

func tsReport() copyonly.Report {
	return copyonly.Report{
		Hits:     []copyonly.Hit{{File: "cmd/evener-hub/frontend/src/a.test.tsx", Line: 3, Category: copyonly.CategoryTSAssert, Literal: "No sessions yet"}},
		Fixtures: map[string]int{"cmd/evener-hub/frontend/src": 2},
		CopyOnly: []copyonly.CopyOnlyTest{{File: "cmd/evener-hub/frontend/src/a.test.tsx", Line: 1, Test: "shows the title", Purpose: copyonly.PurposeCopy}},
	}
}

func TestCopyOracleMergesBothLanguages(t *testing.T) {
	t.Parallel()
	root := copyOracleRepo(t)
	ts := &fakeTS{report: tsReport()}
	hits := filepath.Join(t.TempDir(), "hits.tsv")
	var stdout, stderr bytes.Buffer
	if code := copyOracleRun([]string{"--root", root, "--hits", hits}, &stdout, &stderr, ts.inventory); code != 0 {
		t.Fatalf("exit %d, stderr %s", code, stderr.String())
	}
	want := "| package | total | ts-assert | ts-locator | ts-constant | go-errstring | go-copy | go-asset | fixture (excluded) |\n" +
		"|---|---:|---:|---:|---:|---:|---:|---:|---:|\n" +
		"| `cmd/evener-hub/frontend/src` | 1 | 1 |  |  |  |  |  | 2 |\n" +
		"| `pkg` | 1 |  |  |  |  | 1 |  |  |\n" +
		"| **all (2 packages)** | **2** | **1** | **0** | **0** | **0** | **1** | **0** | **3** |\n"
	if stdout.String() != want {
		t.Errorf("table:\n%s\nwant:\n%s", stdout.String(), want)
	}
	tsv, err := os.ReadFile(hits)
	if err != nil {
		t.Fatal(err)
	}
	wantTSV := "file\tline\tcategory\tliteral\n" +
		"cmd/evener-hub/frontend/src/a.test.tsx\t3\tts-assert\tNo sessions yet\n" +
		"pkg/x_test.go\t3\tgo-copy\tUsage: evener\n"
	if string(tsv) != wantTSV {
		t.Errorf("hits TSV:\n%s\nwant:\n%s", tsv, wantTSV)
	}
	if !strings.Contains(stderr.String(), "2 hits written to "+hits) {
		t.Errorf("stderr = %q, want the hits summary", stderr.String())
	}
}

func TestCopyOracleCopyOnlyReport(t *testing.T) {
	t.Parallel()
	root := copyOracleRepo(t)
	ts := &fakeTS{report: tsReport()}
	out := filepath.Join(t.TempDir(), "copy-only.tsv")
	var stdout, stderr bytes.Buffer
	if code := copyOracleRun([]string{"--root", root, "--copy-only", out}, &stdout, &stderr, ts.inventory); code != 0 {
		t.Fatalf("exit %d, stderr %s", code, stderr.String())
	}
	wantTable := "| area | copy-only (targets) | behavior, copy-only oracle (incidental) |\n" +
		"|---|---:|---:|\n" +
		"| `cmd/evener-hub/frontend` | 1 |  |\n" +
		"| `pkg` | 1 |  |\n" +
		"| **all** | **2** | **0** |\n"
	if stdout.String() != wantTable {
		t.Errorf("table:\n%s\nwant:\n%s", stdout.String(), wantTable)
	}
	tsv, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	wantTSV := "file\tline\ttest\tpurpose\n" +
		"cmd/evener-hub/frontend/src/a.test.tsx\t1\tshows the title\tcopy\n" +
		"pkg/x_test.go\t2\tTestHelpShowsUsage\tcopy\n"
	if string(tsv) != wantTSV {
		t.Errorf("TSV:\n%s\nwant:\n%s", tsv, wantTSV)
	}
}

func TestCopyOracleOnlyAndPath(t *testing.T) {
	t.Parallel()
	root := copyOracleRepo(t)

	goOnly := &fakeTS{report: tsReport()}
	var stdout, stderr bytes.Buffer
	if code := copyOracleRun([]string{"--root", root, "--only", "go", "--path", "other/"}, &stdout, &stderr, goOnly.inventory); code != 0 {
		t.Fatalf("--only go: exit %d, stderr %s", code, stderr.String())
	}
	if goOnly.calls != 0 {
		t.Errorf("--only go ran the TypeScript inventory")
	}
	// other/ has no hits, only the one fixture literal.
	if !strings.Contains(stdout.String(), "| **all (0 packages)** | **0** |") || !strings.HasSuffix(stdout.String(), "| **1** |\n") {
		t.Errorf("--only go --path other/ table:\n%s", stdout.String())
	}

	tsOnly := &fakeTS{report: tsReport()}
	stdout.Reset()
	if code := copyOracleRun([]string{"--root", root, "--only", "ts", "--path", "cmd/"}, &stdout, &stderr, tsOnly.inventory); code != 0 {
		t.Fatalf("--only ts: exit %d, stderr %s", code, stderr.String())
	}
	if tsOnly.calls != 1 || tsOnly.root != root || tsOnly.prefix != "cmd/" {
		t.Errorf("TypeScript inventory called %d times with (%q, %q)", tsOnly.calls, tsOnly.root, tsOnly.prefix)
	}
	if strings.Contains(stdout.String(), "`pkg`") {
		t.Errorf("--only ts reported Go packages:\n%s", stdout.String())
	}
}

func TestCopyOracleRefusesBadArguments(t *testing.T) {
	t.Parallel()
	root := copyOracleRepo(t)
	for name, args := range map[string][]string{
		"unknown language": {"--root", root, "--only", "py"},
		"extra argument":   {"--root", root, "stray"},
	} {
		ts := &fakeTS{}
		var stdout, stderr bytes.Buffer
		if code := copyOracleRun(args, &stdout, &stderr, ts.inventory); code != 2 {
			t.Errorf("%s: exit %d, want 2", name, code)
		}
	}
}

// partwayWriter accepts room bytes, then fails every write.
type partwayWriter struct{ room int }

func (w *partwayWriter) Write(p []byte) (int, error) {
	if len(p) > w.room {
		n := w.room
		w.room = 0
		return n, errors.New("disk full")
	}
	w.room -= len(p)
	return len(p), nil
}

func TestCopyOracleFailsWhenTheReportCannotBeWritten(t *testing.T) {
	t.Parallel()
	root := copyOracleRepo(t)
	for name, args := range map[string][]string{
		"package table":   {"--root", root},
		"copy-only table": {"--root", root, "--copy-only", filepath.Join(t.TempDir(), "co.tsv")},
	} {
		ts := &fakeTS{report: tsReport()}
		var stderr bytes.Buffer
		if code := copyOracleRun(args, &partwayWriter{room: 40}, &stderr, ts.inventory); code != 1 {
			t.Errorf("%s: exit %d, want 1 when stdout fails partway", name, code)
		}
		if !strings.Contains(stderr.String(), "disk full") {
			t.Errorf("%s: stderr = %q, want the write failure", name, stderr.String())
		}
	}
}

func TestWriteFileReportsAFailedWrite(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "out.tsv")
	err := writeFile(path, func(w io.Writer) error {
		if _, err := io.WriteString(w, "header\n"); err != nil {
			return err
		}
		return errors.New("disk full")
	})
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Errorf("writeFile = %v, want the write failure", err)
	}
	if err := writeFile(filepath.Join(path, "not-a-dir", "x.tsv"), func(io.Writer) error { return nil }); err == nil {
		t.Errorf("writeFile under a file succeeded")
	}
}

func TestCopyOracleReportsATypeScriptFailure(t *testing.T) {
	t.Parallel()
	root := copyOracleRepo(t)
	ts := &fakeTS{err: errors.New("node: not found")}
	var stdout, stderr bytes.Buffer
	if code := copyOracleRun([]string{"--root", root}, &stdout, &stderr, ts.inventory); code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "node: not found") {
		t.Errorf("stdout %q, stderr %q: want no report and the cause", stdout.String(), stderr.String())
	}
}
