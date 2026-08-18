package main

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunNamingResultsAndMain(t *testing.T) {
	oldAbs, oldRun, oldExit, oldArgs := filepathAbs, namingRun, osExit, os.Args
	t.Cleanup(func() { filepathAbs, namingRun, osExit, os.Args = oldAbs, oldRun, oldExit, oldArgs })
	var out, errOut bytes.Buffer
	if got := runNaming([]string{"--bad"}, &out, &errOut); got != 2 {
		t.Fatalf("flags = %d", got)
	}
	filepathAbs = func(string) (string, error) { return "", errors.New("abs") }
	if got := runNaming(nil, &out, &errOut); got != 2 {
		t.Fatalf("abs = %d", got)
	}
	filepathAbs = func(s string) (string, error) { return s, nil }
	namingRun = func(string, bool) ([]Violation, error) { return nil, errors.New("walk") }
	if got := runNaming(nil, &out, &errOut); got != 2 {
		t.Fatalf("walk = %d", got)
	}
	namingRun = func(string, bool) ([]Violation, error) { return []Violation{{File: "x", Line: 2, Message: "bad"}}, nil }
	if got := runNaming([]string{"-v"}, &out, &errOut); got != 1 || !strings.Contains(out.String(), "x:2: bad") {
		t.Fatalf("violation = %d %q", got, out.String())
	}
	namingRun = func(string, bool) ([]Violation, error) { return nil, nil }
	if got := runNaming(nil, &out, &errOut); got != 0 {
		t.Fatalf("clean = %d", got)
	}
	os.Args = []string{"serf-namingcheck"}
	exit := -1
	osExit = func(code int) { exit = code }
	main()
	if exit != 0 {
		t.Fatalf("exit = %d", exit)
	}
}

func TestRunInjectedWalkerFailures(t *testing.T) {
	oldWalk, oldRel, oldGo, oldTOML := filepathWalkDir, filepathRel, goFileChecker, tomlFileChecker
	t.Cleanup(func() { filepathWalkDir, filepathRel, goFileChecker, tomlFileChecker = oldWalk, oldRel, oldGo, oldTOML })
	boom := errors.New("boom")
	filepathWalkDir = func(_ string, fn fs.WalkDirFunc) error { return fn("x", nil, boom) }
	if _, err := Run(".", false); !errors.Is(err, boom) {
		t.Fatalf("walk callback err = %v", err)
	}
	info, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := fs.FileInfoToDirEntry(info)
	filepathWalkDir = func(_ string, fn fs.WalkDirFunc) error { return fn("x", dir, nil) }
	filepathRel = func(string, string) (string, error) { return "", boom }
	if _, err := Run(".", false); !errors.Is(err, boom) {
		t.Fatalf("rel err = %v", err)
	}
	filepathRel = oldRel
	filePath := filepath.Join(t.TempDir(), "x.go")
	if err := os.WriteFile(filePath, []byte("package p"), 0o600); err != nil {
		t.Fatal(err)
	}
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		t.Fatal(err)
	}
	file := fs.FileInfoToDirEntry(fileInfo)
	filepathWalkDir = func(_ string, fn fs.WalkDirFunc) error { return fn(filePath, file, nil) }
	goFileChecker = func(string, string) ([]Violation, error) { return nil, boom }
	if _, err := Run(filepath.Dir(filePath), false); !errors.Is(err, boom) {
		t.Fatalf("go err = %v", err)
	}
	filePath = strings.TrimSuffix(filePath, ".go") + ".toml"
	tomlFileChecker = func(string, string) ([]Violation, error) { return nil, boom }
	filepathWalkDir = func(_ string, fn fs.WalkDirFunc) error { return fn(filePath, file, nil) }
	if _, err := Run(filepath.Dir(filePath), false); !errors.Is(err, boom) {
		t.Fatalf("toml err = %v", err)
	}
	filepathWalkDir = func(string, fs.WalkDirFunc) error { return boom }
	if _, err := Run(".", false); !errors.Is(err, boom) {
		t.Fatalf("walk err = %v", err)
	}
}

func TestRunFilesystemFailuresAndSorting(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := Run(missing, false); err == nil {
		t.Fatal("missing root")
	}
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "bad.toml"), []byte("badKey = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "bad.go"), []byte("package p\ntype X struct { A string `json:\"badKey\"` }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "plain.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Run(d, true)
	if err != nil || len(got) != 2 || got[0].File > got[1].File {
		t.Fatalf("got=%v err=%v", got, err)
	}
	if _, err := checkTOMLFile(filepath.Join(d, "missing.toml"), "x"); err == nil {
		t.Fatal("missing toml")
	}
}

func TestRemainingParserBranches(t *testing.T) {
	if got := parseStructTag(""); got != "" {
		t.Fatal("empty struct tag accepted")
	}
	d := t.TempDir()
	src := "package p\ntype X struct {\n A string\n B string ``\n C string `json:\"ok\"`\n}\n"
	p := filepath.Join(d, "x.go")
	if err := os.WriteFile(p, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := checkGoFile(p, "x.go"); err != nil {
		t.Fatal(err)
	}
	if got := toCamelCase("a__b"); got != "aB" {
		t.Fatalf("camel = %q", got)
	}
	toml := "# ignore resets on blank\n\nnot a key\n"
	if err := os.WriteFile(filepath.Join(d, "x.toml"), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := checkTOMLFile(filepath.Join(d, "x.toml"), "x.toml"); err != nil {
		t.Fatal(err)
	}
}

func TestRunExcludedFileAndSameFileSort(t *testing.T) {
	oldWalk, oldRel, oldGo := filepathWalkDir, filepathRel, goFileChecker
	t.Cleanup(func() { filepathWalkDir, filepathRel, goFileChecker = oldWalk, oldRel, oldGo })
	p := filepath.Join(t.TempDir(), "x.go")
	if err := os.WriteFile(p, []byte("package p"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	entry := fs.FileInfoToDirEntry(info)
	filepathWalkDir = func(_ string, fn fs.WalkDirFunc) error { return fn(p, entry, nil) }
	filepathRel = func(string, string) (string, error) { return "vendor/x.go", nil }
	called := false
	goFileChecker = func(string, string) ([]Violation, error) { called = true; return nil, nil }
	if _, err := Run(".", false); err != nil || called {
		t.Fatalf("excluded called=%v err=%v", called, err)
	}
	filepathRel = func(string, string) (string, error) { return "x.go", nil }
	goFileChecker = func(string, string) ([]Violation, error) {
		return []Violation{{File: "x.go", Line: 9}, {File: "x.go", Line: 1}}, nil
	}
	got, err := Run(".", false)
	if err != nil || got[0].Line != 1 {
		t.Fatalf("sort=%v err=%v", got, err)
	}
}
