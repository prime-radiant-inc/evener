package promptpath

import (
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/envvars"
)

// FuzzPromptPaths drives the pure prompt-path construction seam with supplied
// XDG and home values, then pins the production wrapper to explicit XDG state.
func FuzzPromptPaths(f *testing.F) {
	f.Add("/config", "/home/serf", "project", false)
	f.Add("", "/home/serf", "project", false)
	f.Add("", "", "project", true)
	f.Add("/config", "/home/serf", "", false)

	f.Fuzz(func(t *testing.T, xdgConfigHome, home, project string, homeErr bool) {
		if len(xdgConfigHome) > 4096 || len(home) > 4096 || len(project) > 4096 {
			return
		}

		homeCalls := 0
		gotGlobal := globalPromptsDir(xdgConfigHome, func() (string, error) {
			homeCalls++
			if homeErr {
				return home, errors.New("home lookup failed")
			}
			return home, nil
		})
		var wantGlobal string
		switch {
		case xdgConfigHome != "":
			wantGlobal = filepath.Join(xdgConfigHome, "serf", "prompts")
			if homeCalls != 0 {
				t.Fatalf("XDG path consulted home directory %d times", homeCalls)
			}
		case homeErr:
			if homeCalls != 1 {
				t.Fatalf("failed home lookup was called %d times, want 1", homeCalls)
			}
		case !homeErr:
			wantGlobal = filepath.Join(home, ".config", "serf", "prompts")
			if homeCalls != 1 {
				t.Fatalf("home lookup was called %d times, want 1", homeCalls)
			}
		}
		if gotGlobal != wantGlobal {
			t.Fatalf("global prompts path = %q, want %q", gotGlobal, wantGlobal)
		}

		projectRoot := ""
		if project != "" {
			projectRoot = filepath.Join(string(filepath.Separator)+"worktrees", hex.EncodeToString([]byte(project)))
		}
		gotProject := ProjectPromptsDir(projectRoot)
		if projectRoot == "" {
			if gotProject != "" {
				t.Fatalf("empty project root returned %q", gotProject)
			}
			return
		}
		wantProject := filepath.Join(projectRoot, ".serf", "prompts")
		if gotProject != wantProject {
			t.Fatalf("project prompts path = %q, want %q", gotProject, wantProject)
		}
		rel, err := filepath.Rel(projectRoot, gotProject)
		if err != nil {
			t.Fatalf("relativize project prompts path: %v", err)
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Fatalf("project prompts path escaped root: root=%q path=%q rel=%q", projectRoot, gotProject, rel)
		}

		// Pin the production wrapper to an explicit XDG directory so it cannot
		// depend on the caller's HOME while retaining wrapper coverage.
		wrapperXDG := filepath.Join(t.TempDir(), "xdg")
		t.Setenv(envvars.XDGConfigHome.Name, wrapperXDG)
		if got, want := GlobalPromptsDir(), filepath.Join(wrapperXDG, "serf", "prompts"); got != want {
			t.Fatalf("GlobalPromptsDir() = %q, want %q", got, want)
		}
	})
}
