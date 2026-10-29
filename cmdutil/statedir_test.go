package cmdutil

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/envvars/userdirs"
	"primeradiant.com/evener/gittest"
	"primeradiant.com/evener/identifier"
)

// TestStateRootFromLookup pins the canonical state-root chain against a
// supplied environment lookup: XDG_STATE_HOME (verbatim), then the home
// directory in os.UserHomeDir's per-OS spellings, then ErrNoStateHome. The Windows arms are pinned from any host so the hub's launch-env
// resolution cannot regress off-Windows again (#1012).
func TestStateRootFromLookup(t *testing.T) {
	lookup := func(env map[string]string) func(string) (string, bool) {
		return func(key string) (string, bool) {
			value, ok := env[key]
			return value, ok
		}
	}
	tests := []struct {
		name      string
		goos      string
		env       map[string]string
		wantParts []string
		wantErr   bool
	}{
		{
			name:      "xdg state home wins over home",
			goos:      "linux",
			env:       map[string]string{envvars.XDGStateHome.Name: "/state", envvars.Home.Name: "/home/u"},
			wantParts: []string{"/state", "evener"},
		},
		{
			// os.UserHomeDir's callers read XDG_STATE_HOME raw (rendezvous,
			// appwire, agent/runtime_dir); the shared chain must not trim it
			// or it would diverge from them.
			name:      "xdg state home is used verbatim",
			goos:      "linux",
			env:       map[string]string{envvars.XDGStateHome.Name: "  /state  "},
			wantParts: []string{"  /state  ", "evener"},
		},
		{
			name:      "empty xdg state home falls through to home",
			goos:      "linux",
			env:       map[string]string{envvars.XDGStateHome.Name: "", envvars.Home.Name: "/home/u"},
			wantParts: []string{"/home/u", ".local", "state", "evener"},
		},
		{
			name:      "unix home fallback",
			goos:      "darwin",
			env:       map[string]string{envvars.Home.Name: "/home/u"},
			wantParts: []string{"/home/u", ".local", "state", "evener"},
		},
		{
			name:      "windows userprofile wins over msys home",
			goos:      "windows",
			env:       map[string]string{envvars.UserProfile.Name: `C:\Users\u`, envvars.Home.Name: `C:\msys\home\u`},
			wantParts: []string{`C:\Users\u`, ".local", "state", "evener"},
		},
		{
			// os.UserHomeDir has no HOMEDRIVE/HOMEPATH arm; the old hub did,
			// and that was the drift #1012 removed. Partial and full
			// combinations alike resolve no home.
			name:    "windows ignores homedrive and homepath",
			goos:    "windows",
			env:     map[string]string{"HOMEDRIVE": "D:", "HOMEPATH": `\Users\u`},
			wantErr: true,
		},
		{
			name:    "windows ignores a lone homedrive",
			goos:    "windows",
			env:     map[string]string{"HOMEDRIVE": "D:"},
			wantErr: true,
		},
		{
			name:    "windows ignores a lone homepath",
			goos:    "windows",
			env:     map[string]string{"HOMEPATH": `\Users\u`},
			wantErr: true,
		},
		{
			name:    "windows ignores a msys home with no profile",
			goos:    "windows",
			env:     map[string]string{envvars.Home.Name: `C:\msys\home\u`},
			wantErr: true,
		},
		{
			name:      "android uses the sdcard constant",
			goos:      "android",
			env:       map[string]string{},
			wantParts: []string{"/sdcard", ".local", "state", "evener"},
		},
		{
			name:      "ios uses the root constant",
			goos:      "ios",
			env:       map[string]string{},
			wantParts: []string{"/", ".local", "state", "evener"},
		},
		{
			name:    "no home is an error",
			goos:    "linux",
			env:     map[string]string{},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := StateRootFromLookup(tc.goos, lookup(tc.env))
			if tc.wantErr {
				if !errors.Is(err, userdirs.ErrNoStateHome) {
					t.Fatalf("StateRootFromLookup(%q, %v) error = %v, want ErrNoStateHome", tc.goos, tc.env, err)
				}
				return
			}
			want := filepath.Join(tc.wantParts...)
			if err != nil || got != want {
				t.Fatalf("StateRootFromLookup(%q, %v) = %q, %v, want %q", tc.goos, tc.env, got, err, want)
			}
		})
	}
}

// TestDefaultProjectStateDir_LinkedWorktreeSameAsMain proves the fix for the
// bug described in
// docs/superpowers/specs/2026-07-02-native-worktree-tools-design.md §1
// ("Runtime state keying at launch"): for an origin-less repo, RuntimeDir
// used to key off the raw workDir, so launching from a linked worktree
// computed a different project state dir than launching from the main repo
// root. DefaultProjectStateDir must key both the same.
func TestDefaultProjectStateDir_LinkedWorktreeSameAsMain(t *testing.T) {
	main, wt := gittest.NewLinkedWorktree(t)

	_, mainDir, err := DefaultProjectStateDir(main)
	if err != nil {
		t.Fatal(err)
	}
	_, wtDir, err := DefaultProjectStateDir(wt)
	if err != nil {
		t.Fatal(err)
	}

	if mainDir != wtDir {
		t.Errorf("state dir differs between main root and linked worktree:\n  main = %q\n  wt   = %q", mainDir, wtDir)
	}
}

func TestDefaultProjectStateDir_ClonesWithSameOriginDiffer(t *testing.T) {
	base := t.TempDir()
	remote := filepath.Join(base, "remote.git")
	gittest.Run(t, base, "init", "-q", "--bare", remote)
	first := filepath.Join(base, "first")
	second := filepath.Join(base, "second")
	gittest.Run(t, base, "clone", "-q", remote, first)
	gittest.Run(t, base, "clone", "-q", remote, second)

	_, firstDir, err := DefaultProjectStateDir(first)
	if err != nil {
		t.Fatal(err)
	}
	_, secondDir, err := DefaultProjectStateDir(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstDir == secondDir {
		t.Fatalf("same-origin clones share project state dir: %q", firstDir)
	}
}

// TestDefaultProjectStateDir_NotInRepo_FallsBackToWorkDir covers the
// not-in-a-repo case: ResolveMainRepoRootLocal returns "" and the state dir
// must key off workDir unchanged, matching pre-existing behavior for
// non-git directories.
func TestDefaultProjectStateDir_NotInRepo_FallsBackToWorkDir(t *testing.T) {
	workDir := t.TempDir()

	_, got, err := DefaultProjectStateDir(workDir)
	if err != nil {
		t.Fatal(err)
	}
	_, want, err := DefaultProjectStateDir(workDir) // deterministic given the same input
	if err != nil {
		t.Fatal(err)
	}

	if got != want {
		t.Errorf("DefaultProjectStateDir(%q) not deterministic: %q vs %q", workDir, got, want)
	}
	// The state dir must be keyed by workDir itself (no git ancestor to
	// resolve), i.e. it must differ across two distinct non-repo dirs.
	other := t.TempDir()
	_, otherDir, err := DefaultProjectStateDir(other)
	if err != nil {
		t.Fatal(err)
	}
	if got == otherDir {
		t.Errorf("DefaultProjectStateDir collided for distinct non-repo workDirs %q and %q: %q", workDir, other, got)
	}
}

func TestResolveStateKeyDir_NonGitSymlinkUsesSharedCanonicalPath(t *testing.T) {
	target := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(alias)
	if err != nil {
		t.Fatal(err)
	}
	if got := ResolveStateKeyDir(alias); got != project.CanonicalPath {
		t.Fatalf("ResolveStateKeyDir(%q) = %q, want shared canonical path %q", alias, got, project.CanonicalPath)
	}
}
