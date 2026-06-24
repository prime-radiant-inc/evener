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
	t.Parallel()
	dir := t.TempDir()
	ws := ScanWorkspace(dir)

	if ws.Tree != "" {
		t.Errorf("expected empty tree for empty dir, got %q", ws.Tree)
	}
	if ws.BuildInfo != "" {
		t.Errorf("expected empty build info, got %q", ws.BuildInfo)
	}
}

func TestScanWorkspace_BasicTree(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	touchFile(t, filepath.Join(dir, "main.py"), "print('hello')\n")
	touchFile(t, filepath.Join(dir, "utils.py"), "def foo(): pass\n")
	touchFile(t, filepath.Join(dir, "src", "lib.py"), "class Lib: pass\n")

	ws := ScanWorkspace(dir)

	// Tree should be rooted at the directory and use indented basenames.
	if !strings.Contains(ws.Tree, dir+"/") {
		t.Errorf("tree missing root path: %s", ws.Tree)
	}
	if !strings.Contains(ws.Tree, "main.py") {
		t.Errorf("tree missing main.py: %s", ws.Tree)
	}
	if !strings.Contains(ws.Tree, "utils.py") {
		t.Errorf("tree missing utils.py: %s", ws.Tree)
	}
	if !strings.Contains(ws.Tree, "src/") {
		t.Errorf("tree missing src/ directory: %s", ws.Tree)
	}
	if !strings.Contains(ws.Tree, "lib.py") {
		t.Errorf("tree missing lib.py: %s", ws.Tree)
	}
}

func TestScanWorkspace_DepthLimit(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Walk depths: level1=0, level2=1, level3=2, level4=3
	touchFile(t, filepath.Join(dir, "level1", "level2", "shallow.txt"), "shallow")              // walk depth 2 — visible (maxFileDepth=2)
	touchFile(t, filepath.Join(dir, "level1", "level2", "level3", "deep.txt"), "deep")          // walk depth 3 — NOT visible (>maxFileDepth)
	touchFile(t, filepath.Join(dir, "level1", "level2", "level3", "level4", "vdeep.txt"), "vd") // walk depth 4 — NOT visible

	ws := ScanWorkspace(dir)

	// Walk-depth-2 file should be visible (within maxFileDepth=2).
	if !strings.Contains(ws.Tree, "shallow.txt") {
		t.Errorf("tree missing file at depth 2: %s", ws.Tree)
	}
	// Walk-depth-2 directory (level3) should be visible (within maxDirDepth=3).
	if !strings.Contains(ws.Tree, "level3") {
		t.Errorf("tree missing directory at depth 2: %s", ws.Tree)
	}
	// Walk-depth-3 directory (level4) should be visible (maxDirDepth=3).
	if !strings.Contains(ws.Tree, "level4") {
		t.Errorf("tree missing directory at depth 3: %s", ws.Tree)
	}
	// Walk-depth-3 file should NOT be visible (>maxFileDepth=2).
	if strings.Contains(ws.Tree, "deep.txt") {
		t.Errorf("tree should not contain file at depth 3: %s", ws.Tree)
	}
}

func TestScanWorkspace_MaxEntries(t *testing.T) {
	t.Parallel()
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

func TestScanWorkspace_MakefileTargets(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	ws := ScanWorkspace("/nonexistent/path/12345")

	if ws.Tree != "" {
		t.Errorf("expected empty tree for nonexistent dir, got %q", ws.Tree)
	}
}

func TestScanWorkspace_BuildInfoDetection(t *testing.T) {
	t.Parallel()
	type file struct {
		name, content string
	}
	cases := []struct {
		name  string
		files []file
		want  string
	}{
		{
			name: "GoMod",
			files: []file{
				{"go.mod", "module example.com/app\n\ngo 1.21\n"},
				{"main.go", "package main\n"},
			},
			want: "go.mod",
		},
		{
			name: "PytestIni",
			files: []file{
				{"pytest.ini", "[pytest]\ntestpaths = tests\n"},
				{"main.py", "print('hi')\n"},
			},
			want: "pytest",
		},
		{
			name: "Dockerfile",
			files: []file{
				{"Dockerfile", "FROM python:3.11\nRUN pip install flask\n"},
				{"docker-compose.yml", "version: '3'\n"},
			},
			want: "Dockerfile",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			for _, f := range c.files {
				touchFile(t, filepath.Join(dir, f.name), f.content)
			}

			ws := ScanWorkspace(dir)

			if !strings.Contains(ws.BuildInfo, c.want) {
				t.Errorf("build info should mention %s: %s", c.want, ws.BuildInfo)
			}
		})
	}
}

func TestScanWorkspace_CargoTomlDetection(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	touchFile(t, filepath.Join(dir, "Cargo.toml"), "[package]\nname = \"myapp\"\nversion = \"0.1.0\"\n")

	ws := ScanWorkspace(dir)

	if !strings.Contains(ws.BuildInfo, "Cargo.toml") {
		t.Errorf("build info should mention Cargo.toml: %s", ws.BuildInfo)
	}
}

func TestScanWorkspace_TreeFormat_Indented(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	touchFile(t, filepath.Join(dir, "README.md"), "# Hello\n")
	touchFile(t, filepath.Join(dir, "src", "main.py"), "print('hello')\n")
	touchFile(t, filepath.Join(dir, "src", "utils.py"), "def foo(): pass\n")

	ws := ScanWorkspace(dir)

	// First line should be the root with trailing slash.
	lines := strings.Split(ws.Tree, "\n")
	if len(lines) == 0 || !strings.HasSuffix(lines[0], "/") {
		t.Errorf("first line should be root path with trailing slash, got %q", lines[0])
	}

	// Subsequent lines should be indented basenames.
	if !strings.Contains(ws.Tree, "  src/") {
		t.Errorf("tree missing indented src/: %s", ws.Tree)
	}
	if !strings.Contains(ws.Tree, "    main.py") {
		t.Errorf("tree missing indented main.py under src: %s", ws.Tree)
	}
	if !strings.Contains(ws.Tree, "  README.md") {
		t.Errorf("tree missing indented README.md: %s", ws.Tree)
	}
}

func TestScanWorkspace_CMakeDetection(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	touchFile(t, filepath.Join(dir, "CMakeLists.txt"), "cmake_minimum_required(VERSION 3.10)\nproject(myapp)\n")

	ws := ScanWorkspace(dir)

	if !strings.Contains(ws.BuildInfo, "CMake") {
		t.Errorf("build info should mention CMake: %s", ws.BuildInfo)
	}
}
