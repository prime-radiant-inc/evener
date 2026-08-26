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
		t.Skip("make is unavailable")
	}
	command := exec.Command(makePath, "-f", filepath.Join(repoRoot, "Makefile"), "-n", "build")
	command.Dir = foreign
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("foreign-cwd make -n build: %v\n%s", err, output)
	}
}

func TestMakeMergeApprovalGateIsOrderedAndFailClosed(t *testing.T) {
	repoRoot, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	makePath, err := exec.LookPath("make")
	if err != nil {
		t.Skip("make is unavailable")
	}
	command := exec.Command(makePath, "-f", filepath.Join(repoRoot, "Makefile"), "-n", "MAKE=echo", "merge-approval-gate")
	command.Dir = repoRoot
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("make -n merge-approval-gate: %v\n%s", err, output)
	}
	text := string(output)
	phases := []string{"echo lint", "echo build", "ROOT_FULL=1 echo test", "echo test-dev-tooling"}
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
	if strings.Contains(text, "||") || strings.Contains(text, "; ") {
		t.Fatalf("dry run contains failure-ignoring/non-serial composition:\n%s", text)
	}
	if !strings.Contains(text, "&&") {
		t.Fatalf("dry run is not visibly &&-joined:\n%s", text)
	}
}
