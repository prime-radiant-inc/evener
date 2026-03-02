package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// helper to create a file with optional content.
func touchFile(t *testing.T, path string, content ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	c := ""
	if len(content) > 0 {
		c = content[0]
	}
	if err := os.WriteFile(path, []byte(c), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanWorkspace_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	ws := ScanWorkspace(dir)

	if ws.Tree != "" {
		t.Errorf("expected empty tree for empty dir, got %q", ws.Tree)
	}
	if len(ws.TestFiles) != 0 {
		t.Errorf("expected no test files, got %v", ws.TestFiles)
	}
	if ws.BuildInfo != "" {
		t.Errorf("expected empty build info, got %q", ws.BuildInfo)
	}
}

func TestScanWorkspace_BasicTree(t *testing.T) {
	dir := t.TempDir()
	touchFile(t, filepath.Join(dir, "main.py"), "print('hello')\n")
	touchFile(t, filepath.Join(dir, "utils.py"), "def foo(): pass\n")
	touchFile(t, filepath.Join(dir, "src", "lib.py"), "class Lib: pass\n")

	ws := ScanWorkspace(dir)

	// Tree should contain absolute paths for all files.
	if !strings.Contains(ws.Tree, filepath.Join(dir, "main.py")) {
		t.Errorf("tree missing main.py: %s", ws.Tree)
	}
	if !strings.Contains(ws.Tree, filepath.Join(dir, "utils.py")) {
		t.Errorf("tree missing utils.py: %s", ws.Tree)
	}
	if !strings.Contains(ws.Tree, filepath.Join(dir, "src")+"/") {
		t.Errorf("tree missing src/ directory: %s", ws.Tree)
	}
	if !strings.Contains(ws.Tree, filepath.Join(dir, "src", "lib.py")) {
		t.Errorf("tree missing src/lib.py: %s", ws.Tree)
	}
}

func TestScanWorkspace_DepthLimit(t *testing.T) {
	dir := t.TempDir()
	// Create a deeply nested file: level1/level2/level3/level4/deep.txt
	touchFile(t, filepath.Join(dir, "level1", "level2", "level3", "level4", "deep.txt"), "deep")
	// Also a file at depth 3 (should be included).
	touchFile(t, filepath.Join(dir, "level1", "level2", "level3", "shallow.txt"), "shallow")

	ws := ScanWorkspace(dir)

	// Depth 3 file should be visible.
	if !strings.Contains(ws.Tree, "shallow.txt") {
		t.Errorf("tree missing depth-3 file: %s", ws.Tree)
	}
	// Depth 4 file should NOT be visible (maxDepth=3).
	if strings.Contains(ws.Tree, "deep.txt") {
		t.Errorf("tree should not contain depth-4 file: %s", ws.Tree)
	}
}

func TestScanWorkspace_MaxEntries(t *testing.T) {
	dir := t.TempDir()
	// Create more files than maxEntries (200).
	for i := 0; i < 250; i++ {
		touchFile(t, filepath.Join(dir, "file_"+string(rune('a'+i/26))+string(rune('a'+i%26))+".txt"), "x")
	}

	ws := ScanWorkspace(dir)

	// Should be truncated with a note.
	if !strings.Contains(ws.Tree, "truncated") {
		t.Errorf("expected truncation note in tree: %s", ws.Tree)
	}
}

func TestScanWorkspace_TestFileDetection(t *testing.T) {
	dir := t.TempDir()

	// Various test file patterns.
	touchFile(t, filepath.Join(dir, "test.sh"), "#!/bin/bash\nexit 0\n")
	touchFile(t, filepath.Join(dir, "tests", "test_main.py"), "def test_main(): pass\n")
	touchFile(t, filepath.Join(dir, "src", "main_test.go"), "func TestMain(t *testing.T) {}\n")
	touchFile(t, filepath.Join(dir, "spec", "app.test.js"), "test('app', () => {})\n")
	touchFile(t, filepath.Join(dir, "tests", "test_outputs.py"), "import pytest\n")
	touchFile(t, filepath.Join(dir, "test_runner.sh"), "#!/bin/bash\n")

	// Non-test files that shouldn't be flagged.
	touchFile(t, filepath.Join(dir, "main.py"), "print('hello')\n")
	touchFile(t, filepath.Join(dir, "utils_test_helper.py"), "# helper\n") // ambiguous but has _test_ in it

	ws := ScanWorkspace(dir)

	// These should all be detected.
	expected := []string{
		"test.sh",
		"tests/test_main.py",
		"src/main_test.go",
		"spec/app.test.js",
		"tests/test_outputs.py",
		"test_runner.sh",
	}
	for _, e := range expected {
		found := false
		for _, tf := range ws.TestFiles {
			if tf == e {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected test file %q not found in %v", e, ws.TestFiles)
		}
	}

	// main.py should NOT be flagged.
	for _, tf := range ws.TestFiles {
		if tf == "main.py" {
			t.Errorf("main.py should not be flagged as a test file")
		}
	}
}

func TestScanWorkspace_MakefileTargets(t *testing.T) {
	dir := t.TempDir()
	makefile := `CC=gcc
CFLAGS=-Wall

.PHONY: all test clean install

all: main.o
	$(CC) -o app main.o

main.o: main.c
	$(CC) $(CFLAGS) -c main.c

test: all
	./run_tests.sh

clean:
	rm -f *.o app

install: all
	cp app /usr/local/bin/
`
	touchFile(t, filepath.Join(dir, "Makefile"), makefile)

	ws := ScanWorkspace(dir)

	if !strings.Contains(ws.BuildInfo, "Makefile") {
		t.Errorf("build info should mention Makefile: %s", ws.BuildInfo)
	}
	// Should list targets.
	for _, target := range []string{"all", "test", "clean", "install"} {
		if !strings.Contains(ws.BuildInfo, target) {
			t.Errorf("build info missing target %q: %s", target, ws.BuildInfo)
		}
	}
}

func TestScanWorkspace_PackageJsonScripts(t *testing.T) {
	dir := t.TempDir()
	pkg := `{
  "name": "myapp",
  "scripts": {
    "build": "tsc",
    "test": "jest",
    "lint": "eslint .",
    "start": "node dist/index.js"
  }
}`
	touchFile(t, filepath.Join(dir, "package.json"), pkg)

	ws := ScanWorkspace(dir)

	if !strings.Contains(ws.BuildInfo, "package.json") {
		t.Errorf("build info should mention package.json: %s", ws.BuildInfo)
	}
	for _, script := range []string{"build", "test", "lint", "start"} {
		if !strings.Contains(ws.BuildInfo, script) {
			t.Errorf("build info missing script %q: %s", script, ws.BuildInfo)
		}
	}
}

func TestScanWorkspace_HiddenDirsExcluded(t *testing.T) {
	dir := t.TempDir()
	touchFile(t, filepath.Join(dir, ".git", "config"), "git config")
	touchFile(t, filepath.Join(dir, ".hidden", "secret.txt"), "secret")
	touchFile(t, filepath.Join(dir, "visible.txt"), "visible")
	touchFile(t, filepath.Join(dir, "node_modules", "pkg", "index.js"), "module")
	touchFile(t, filepath.Join(dir, "__pycache__", "main.cpython-39.pyc"), "cache")

	ws := ScanWorkspace(dir)

	if strings.Contains(ws.Tree, ".git") {
		t.Errorf("tree should exclude .git: %s", ws.Tree)
	}
	if strings.Contains(ws.Tree, ".hidden") {
		t.Errorf("tree should exclude hidden dirs: %s", ws.Tree)
	}
	if strings.Contains(ws.Tree, "node_modules") {
		t.Errorf("tree should exclude node_modules: %s", ws.Tree)
	}
	if strings.Contains(ws.Tree, "__pycache__") {
		t.Errorf("tree should exclude __pycache__: %s", ws.Tree)
	}
	if !strings.Contains(ws.Tree, "visible.txt") {
		t.Errorf("tree should include visible.txt: %s", ws.Tree)
	}
}

func TestScanWorkspace_NonexistentDir(t *testing.T) {
	ws := ScanWorkspace("/nonexistent/path/12345")

	if ws.Tree != "" {
		t.Errorf("expected empty tree for nonexistent dir, got %q", ws.Tree)
	}
	if len(ws.TestFiles) != 0 {
		t.Errorf("expected no test files, got %v", ws.TestFiles)
	}
}

func TestScanWorkspace_GoModDetection(t *testing.T) {
	dir := t.TempDir()
	touchFile(t, filepath.Join(dir, "go.mod"), "module example.com/app\n\ngo 1.21\n")
	touchFile(t, filepath.Join(dir, "main.go"), "package main\n")

	ws := ScanWorkspace(dir)

	if !strings.Contains(ws.BuildInfo, "go.mod") {
		t.Errorf("build info should mention go.mod: %s", ws.BuildInfo)
	}
}

func TestScanWorkspace_CargoTomlDetection(t *testing.T) {
	dir := t.TempDir()
	touchFile(t, filepath.Join(dir, "Cargo.toml"), "[package]\nname = \"myapp\"\nversion = \"0.1.0\"\n")

	ws := ScanWorkspace(dir)

	if !strings.Contains(ws.BuildInfo, "Cargo.toml") {
		t.Errorf("build info should mention Cargo.toml: %s", ws.BuildInfo)
	}
}

func TestScanWorkspace_PytestIniDetection(t *testing.T) {
	dir := t.TempDir()
	touchFile(t, filepath.Join(dir, "pytest.ini"), "[pytest]\ntestpaths = tests\n")
	touchFile(t, filepath.Join(dir, "main.py"), "print('hi')\n")

	ws := ScanWorkspace(dir)

	if !strings.Contains(ws.BuildInfo, "pytest") {
		t.Errorf("build info should mention pytest: %s", ws.BuildInfo)
	}
}

func TestScanWorkspace_TreeFormat_AbsolutePaths(t *testing.T) {
	dir := t.TempDir()
	touchFile(t, filepath.Join(dir, "README.md"), "# Hello\n")
	touchFile(t, filepath.Join(dir, "src", "main.py"), "print('hello')\n")
	touchFile(t, filepath.Join(dir, "src", "utils.py"), "def foo(): pass\n")

	ws := ScanWorkspace(dir)

	lines := strings.Split(ws.Tree, "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		// Every line should be an absolute path (starts with /).
		if !filepath.IsAbs(line) {
			t.Errorf("expected absolute path, got %q", line)
		}
	}

	// Should contain the full paths.
	if !strings.Contains(ws.Tree, filepath.Join(dir, "src")+"/") {
		t.Errorf("tree missing absolute path for src/: %s", ws.Tree)
	}
	if !strings.Contains(ws.Tree, filepath.Join(dir, "src", "main.py")) {
		t.Errorf("tree missing absolute path for src/main.py: %s", ws.Tree)
	}
	if !strings.Contains(ws.Tree, filepath.Join(dir, "README.md")) {
		t.Errorf("tree missing absolute path for README.md: %s", ws.Tree)
	}
}

func TestScanWorkspace_DockerfileDetection(t *testing.T) {
	dir := t.TempDir()
	touchFile(t, filepath.Join(dir, "Dockerfile"), "FROM python:3.11\nRUN pip install flask\n")
	touchFile(t, filepath.Join(dir, "docker-compose.yml"), "version: '3'\n")

	ws := ScanWorkspace(dir)

	if !strings.Contains(ws.BuildInfo, "Dockerfile") {
		t.Errorf("build info should mention Dockerfile: %s", ws.BuildInfo)
	}
}

func TestScanWorkspace_CMakeDetection(t *testing.T) {
	dir := t.TempDir()
	touchFile(t, filepath.Join(dir, "CMakeLists.txt"), "cmake_minimum_required(VERSION 3.10)\nproject(myapp)\n")

	ws := ScanWorkspace(dir)

	if !strings.Contains(ws.BuildInfo, "CMake") {
		t.Errorf("build info should mention CMake: %s", ws.BuildInfo)
	}
}

func TestScanWorkspace_TestFilesRelativePaths(t *testing.T) {
	dir := t.TempDir()
	touchFile(t, filepath.Join(dir, "tests", "test_main.py"), "def test_main(): pass\n")
	touchFile(t, filepath.Join(dir, "test.sh"), "#!/bin/bash\n")

	ws := ScanWorkspace(dir)

	// Paths should be relative to the workspace root.
	for _, tf := range ws.TestFiles {
		if filepath.IsAbs(tf) {
			t.Errorf("test file path should be relative, got %q", tf)
		}
	}
}
