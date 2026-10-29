package evener_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// scripts/ops/tracked-file-size-check.sh keeps large blobs out of history. Its
// whole job is a negative, so each case commits a file of a chosen size into a
// throwaway git repository and runs the gate against it.

func runTrackedFileSizeCheck(t *testing.T, files map[string]int) (bool, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("scripts/ops/tracked-file-size-check.sh gate runs on Unix CI")
	}
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	for path, size := range files {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", path, err)
		}
		if err := os.WriteFile(full, make([]byte, size), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	if out, err := exec.Command("git", "-C", root, "add", ".").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	script := filepath.Join(wd, "scripts", "ops", "tracked-file-size-check.sh")
	output, err := exec.Command(script, "--root", root).CombinedOutput()
	if err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			t.Fatalf("running %s: %v", script, err)
		}
		return false, string(output)
	}
	return true, string(output)
}

func TestTrackedFileSizePassesFilesUnderBudget(t *testing.T) {
	passed, output := runTrackedFileSizeCheck(t, map[string]int{"small.bin": 1 << 20, "dir/tiny.txt": 10})
	if !passed {
		t.Fatalf("files at or under 1 MiB must pass; the gate said:\n%s", output)
	}
}

func TestTrackedFileSizeRejectsOversizedFile(t *testing.T) {
	passed, output := runTrackedFileSizeCheck(t, map[string]int{"small.txt": 10, "assets/huge.bin": 1<<20 + 1})
	if passed {
		t.Fatal("the gate passed a file over 1 MiB")
	}
	if !strings.Contains(output, "assets/huge.bin") {
		t.Fatalf("the gate never named the oversized file:\n%s", output)
	}
	if strings.Contains(output, "small.txt") {
		t.Fatalf("the gate named a file under budget:\n%s", output)
	}
}

func TestTrackedFileSizeAllowsListedFile(t *testing.T) {
	passed, output := runTrackedFileSizeCheck(t, map[string]int{"mobile-native/src/generated/mermaidPage.ts": 2 << 20})
	if !passed {
		t.Fatalf("an allowlisted file must pass; the gate said:\n%s", output)
	}
}
