package evener_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMakeBuildUsesMakefileAnchoredIncludesFromForeignDirectory(t *testing.T) {
	repoRoot, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	foreign := t.TempDir()
	makePath, err := exec.LookPath("make")
	if err != nil {
		t.Fatalf("required make executable is unavailable: %v", err)
	}
	command := exec.Command(makePath, "-f", filepath.Join(repoRoot, "Makefile"), "-n", "build")
	command.Dir = foreign
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("foreign-cwd make -n build: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "scripts/ops/build-runtime-pair.sh") {
		t.Fatalf("foreign-cwd dry run did not reach the included build rule:\n%s", output)
	}
}

func TestMakeMergeApprovalGateIsOrderedAndFailClosed(t *testing.T) {
	repoRoot, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	makePath, err := exec.LookPath("make")
	if err != nil {
		t.Fatalf("required make executable is unavailable: %v", err)
	}
	foreign := t.TempDir()
	if err := os.WriteFile(filepath.Join(foreign, "Makefile"), []byte(".PHONY: lint build test test-dev-tooling\n"+"lint build test test-dev-tooling:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(makePath, "-f", filepath.Join(repoRoot, "Makefile"), "-n", "merge-approval-gate")
	command.Dir = foreign
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("make -n merge-approval-gate: %v\n%s", err, output)
	}
	text := string(output)
	phases := []string{"lint", "build", "ROOT_FULL=1", "test-dev-tooling"}
	previous := -1
	for _, phase := range phases {
		position := strings.Index(text, phase)
		if position < 0 {
			t.Fatalf("dry run missing phase %q:\n%s", phase, text)
		}
		if position <= previous {
			t.Fatalf("dry run phases out of order at %q:\n%s", phase, text)
		}
		previous = position
	}
	if strings.Count(text, "&&") < 3 {
		t.Fatalf("dry run does not join all four phases with &&:\n%s", text)
	}

	raw, err := os.ReadFile(filepath.Join(repoRoot, "make", "testing.mk"))
	if err != nil {
		t.Fatal(err)
	}
	marker := "merge-approval-gate:\n"
	start := strings.Index(string(raw), marker)
	if start < 0 {
		t.Fatal("merge-approval-gate recipe is missing")
	}
	gate, _, ok := strings.Cut(string(raw)[start+len(marker):], "\n\n")
	if !ok {
		t.Fatal("merge-approval-gate recipe is not delimited")
	}
	recipeLines := strings.Split(strings.TrimSpace(gate), "\n")
	if len(recipeLines) != 4 {
		t.Fatalf("merge-approval-gate has %d recipe lines, want four: %q", len(recipeLines), gate)
	}
	for i, line := range recipeLines {
		trimmed := strings.TrimSpace(line)
		control := strings.TrimPrefix(trimmed, "@")
		if strings.HasPrefix(control, "-") {
			t.Fatalf("merge-approval-gate ignores failure with a leading -: %q", line)
		}
		if i < len(recipeLines)-1 && !strings.HasSuffix(trimmed, "&& \\") {
			t.Fatalf("merge-approval-gate phase %d is not &&-joined: %q", i, line)
		}
	}
}
