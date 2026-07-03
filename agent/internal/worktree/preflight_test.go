package worktree

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// fakeVersionRunner returns a GitRunner that ignores its args and always
// returns the canned stdout/err — CheckGitVersion only ever calls
// run("version"), so a fake here is testing the real input surface (git's
// stdout), not mocked internal behavior.
func fakeVersionRunner(stdout string, err error) GitRunner {
	return func(args ...string) (string, error) {
		return stdout, err
	}
}

func TestParseGitVersion(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		wantMajor int
		wantMinor int
		wantOK    bool
	}{
		{"plain", "git version 2.43.0", 2, 43, true},
		{"old patch", "git version 2.32.9", 2, 32, true},
		{"windows suffix", "git version 2.33.0.windows.1", 2, 33, true},
		{"boundary exact", "git version 2.33.0", 2, 33, true},
		{"major 3", "git version 3.0.0", 3, 0, true},
		{"garbage", "not a version string at all", 0, 0, false},
		{"empty", "", 0, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			major, minor, ok := parseGitVersion(tt.in)
			if ok != tt.wantOK {
				t.Fatalf("parseGitVersion(%q) ok = %v, want %v", tt.in, ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if major != tt.wantMajor || minor != tt.wantMinor {
				t.Fatalf("parseGitVersion(%q) = %d.%d, want %d.%d", tt.in, major, minor, tt.wantMajor, tt.wantMinor)
			}
		})
	}
}

func TestCheckGitVersion_OK(t *testing.T) {
	tests := []string{
		"git version 2.43.0",
		"git version 2.33.0.windows.1",
		"git version 2.33.0",
		"git version 3.0.0",
	}
	for _, out := range tests {
		t.Run(out, func(t *testing.T) {
			if err := CheckGitVersion(fakeVersionRunner(out, nil)); err != nil {
				t.Fatalf("CheckGitVersion(%q) = %v, want nil", out, err)
			}
		})
	}
}

func TestCheckGitVersion_TooOld(t *testing.T) {
	err := CheckGitVersion(fakeVersionRunner("git version 2.32.9", nil))
	if err == nil {
		t.Fatal("CheckGitVersion(2.32.9) = nil, want an error")
	}
	if !strings.Contains(err.Error(), "2.33") {
		t.Fatalf("error %q does not name the required floor 2.33", err.Error())
	}
	if !strings.Contains(err.Error(), "2.32") {
		t.Fatalf("error %q does not name the found version 2.32.9", err.Error())
	}
}

func TestCheckGitVersion_Unparseable(t *testing.T) {
	err := CheckGitVersion(fakeVersionRunner("garbage output\n", nil))
	if err == nil {
		t.Fatal("CheckGitVersion(garbage) = nil, want an error")
	}
	if !strings.Contains(err.Error(), "2.33") {
		t.Fatalf("error %q does not name the required floor 2.33", err.Error())
	}
	if !strings.Contains(err.Error(), "garbage output") {
		t.Fatalf("error %q does not name the unparseable output", err.Error())
	}
}

func TestCheckGitVersion_RunnerErrorPropagates(t *testing.T) {
	sentinel := errors.New("exec: git not found")
	err := CheckGitVersion(fakeVersionRunner("", sentinel))
	if err == nil {
		t.Fatal("CheckGitVersion with a runner error = nil, want an error")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("error %v does not wrap the runner error %v", err, sentinel)
	}
}

// TestCheckGitVersion_RealGit invokes the actual `git version` on PATH
// through a GitRunner built directly on os/exec (not the fake above) — the
// happy-path proof that CheckGitVersion works against real git output, not
// just canned strings.
func TestCheckGitVersion_RealGit(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	run := func(args ...string) (string, error) {
		out, err := exec.Command(gitPath, args...).Output()
		return string(out), err
	}
	if err := CheckGitVersion(run); err != nil {
		t.Fatalf("CheckGitVersion against the real git binary = %v, want nil (test host git must be >= 2.33)", err)
	}
}
