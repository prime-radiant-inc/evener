package repoaudit

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
)

// mobileNativeGitignoreIgnoresDerivedDataBuild keeps mobile-native/build
// ignored by mobile-native/.gitignore. A Release simulator build run from
// mobile-native with -derivedDataPath build/dd leaves thousands of untracked
// files under mobile-native/build/, so a `git add -A` there would sweep them
// in unless the ignore rule holds (#3325).
func mobileNativeGitignoreIgnoresDerivedDataBuild(a *audit) {
	target := filepath.Join("mobile-native", "build", "dd", "Build", "x.app")
	out, err := exec.CommandContext(context.Background(), "git", "check-ignore", "--no-index", "-v", target).Output()
	if err != nil {
		a.Fatalf("mobile-native/build is not ignored: git check-ignore %s: %v", target, err)
	}
	if !strings.Contains(string(out), "mobile-native/.gitignore") {
		a.Fatalf("mobile-native/build is ignored by %q, want a rule in mobile-native/.gitignore", strings.TrimSpace(string(out)))
	}
}
