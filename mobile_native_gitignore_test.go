package evener_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A Release simulator build run from mobile-native with -derivedDataPath
// build/dd (the path #3322's screenshots used) leaves thousands of untracked
// files under mobile-native/build/, so a `git add -A` there would sweep them
// in. mobile-native/.gitignore ignored /ios but not /build; pin the ignore so
// the derived-data tree stays untracked (#3325).
func TestMobileNativeGitignoreIgnoresDerivedDataBuild(t *testing.T) {
	repoRoot, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	target := filepath.Join("mobile-native", "build", "dd", "Build", "x.app")
	cmd := exec.Command("git", "check-ignore", "--no-index", "-v", target)
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("mobile-native/build is not ignored: git check-ignore %s: %v", target, err)
	}
	if !strings.Contains(string(out), "mobile-native/.gitignore") {
		t.Fatalf("mobile-native/build is ignored by %q, want a rule in mobile-native/.gitignore", strings.TrimSpace(string(out)))
	}
}
