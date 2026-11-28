//go:build evenerfuzz

package sandbox

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"primeradiant.com/evener/identifier"
)

// structuralFixture is a complete Git metadata shape made directly under a test
// root. It deliberately never invokes Git: ClassifyWorkspace is specified to
// understand these on-disk forms without a subprocess, and the fuzzers must not
// depend on an ambient Git installation.
type structuralFixture struct {
	root      string
	main      string
	linked    string
	submodule string
	nonGit    string
	malformed string
	symlinked string
}

func newStructuralFixture(t testing.TB, includeVariant byte) structuralFixture {
	t.Helper()
	root := identifier.ResolveClean(t.TempDir())
	fixture := structuralFixture{
		root:      root,
		main:      filepath.Join(root, "main"),
		linked:    filepath.Join(root, "linked"),
		submodule: filepath.Join(root, "submodule"),
		nonGit:    filepath.Join(root, "plain"),
		malformed: filepath.Join(root, "malformed"),
		symlinked: filepath.Join(root, "symlinked"),
	}

	mainGit := filepath.Join(fixture.main, ".git")
	linkedGit := filepath.Join(mainGit, "worktrees", "lane")
	submoduleGit := filepath.Join(mainGit, "modules", "libs", "nested")
	for _, dir := range []string{
		fixture.main,
		fixture.linked,
		fixture.submodule,
		fixture.nonGit,
		fixture.malformed,
		fixture.symlinked,
		filepath.Join(mainGit, "objects"),
		filepath.Join(mainGit, "refs"),
		filepath.Join(mainGit, "logs"),
		filepath.Join(mainGit, "hooks"),
		linkedGit,
		submoduleGit,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir structural fixture %q: %v", dir, err)
		}
	}

	include := "../included.conf"
	if includeVariant%3 == 1 {
		include = "../outside.conf"
	}
	if includeVariant%3 == 2 {
		// This glob's literal base is the writable main checkout. The resolver
		// must refuse it rather than attempt a host filesystem glob.
		include = "../*.conf"
	}
	writeStructuralFile(t, filepath.Join(mainGit, "config"), "[include]\npath = "+include+"\n")
	writeStructuralFile(t, filepath.Join(mainGit, "config.worktree"), "[core]\nrepositoryformatversion = 0\n")
	writeStructuralFile(t, filepath.Join(fixture.main, "included.conf"), "[includeIf \"gitdir:*\"]\npath = nested.conf\n")
	writeStructuralFile(t, filepath.Join(fixture.main, "nested.conf"), "[core]\nhooksPath = hooks\n")
	writeStructuralFile(t, filepath.Join(fixture.root, "outside.conf"), "[core]\neditor = true\n")
	writeStructuralFile(t, filepath.Join(submoduleGit, "config"), "[core]\nrepositoryformatversion = 0\n")
	writeStructuralFile(t, filepath.Join(fixture.linked, ".git"), "gitdir: "+linkedGit+"\n")
	writeStructuralFile(t, filepath.Join(fixture.submodule, ".git"), "gitdir: "+submoduleGit+"\n")
	writeStructuralFile(t, filepath.Join(fixture.malformed, ".git"), "not a gitdir pointer\n")
	if err := os.Symlink(mainGit, filepath.Join(fixture.symlinked, ".git")); err != nil {
		t.Fatalf("symlink structural git dir: %v", err)
	}
	return fixture
}

func writeStructuralFile(t testing.TB, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write structural fixture %q: %v", path, err)
	}
}

// structuralReRootLanes creates the linked-worktree shape that ReRoot operates
// on. The parent main checkout and both lanes are siblings under one temp root;
// all git metadata is materialized directly, not through an external command.
func structuralReRootLanes(t testing.TB) (main, laneA, laneB string) {
	t.Helper()
	root := identifier.ResolveClean(t.TempDir())
	main = filepath.Join(root, "main")
	laneA = filepath.Join(root, "lane-a")
	laneB = filepath.Join(root, "lane-b")
	common := filepath.Join(main, ".git")
	for _, dir := range []string{
		filepath.Join(common, "objects"),
		filepath.Join(common, "refs"),
		filepath.Join(common, "logs"),
		filepath.Join(common, "hooks"),
		filepath.Join(common, "worktrees", "lane-a"),
		filepath.Join(common, "worktrees", "lane-b"),
		laneA,
		laneB,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir structural re-root fixture %q: %v", dir, err)
		}
	}
	writeStructuralFile(t, filepath.Join(common, "config"), "[core]\nrepositoryformatversion = 0\n")
	writeStructuralFile(t, filepath.Join(laneA, ".git"), "gitdir: "+filepath.Join(common, "worktrees", "lane-a")+"\n")
	writeStructuralFile(t, filepath.Join(laneB, ".git"), "gitdir: "+filepath.Join(common, "worktrees", "lane-b")+"\n")
	return main, laneA, laneB
}

func FuzzSandboxStructuralContract(f *testing.F) {
	for _, seed := range []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 255} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, variant byte) {
		fixture := newStructuralFixture(t, variant)
		cwd := []string{fixture.main, fixture.linked, fixture.submodule, fixture.nonGit, fixture.malformed, fixture.symlinked}[(int(variant)/3)%6]
		layout, err := ClassifyWorkspace(cwd, nil)
		if cwd == fixture.malformed {
			if err == nil {
				t.Fatalf("malformed .git pointer %q resolved as %+v", cwd, layout)
			}
			return
		}
		if variant%3 == 2 && (cwd == fixture.main || cwd == fixture.linked || cwd == fixture.symlinked) {
			if err == nil {
				t.Fatalf("writable include glob %q resolved as %+v", cwd, layout)
			}
			return
		}
		if err != nil {
			t.Fatalf("ClassifyWorkspace(%q): %v", cwd, err)
		}
		for _, protected := range layout.ProtectedPaths {
			for _, writable := range layout.WritablePaths {
				if protected == writable || identifier.PathUnder(writable, protected) {
					t.Fatalf("layout grants protected path %q through writable root %q", protected, writable)
				}
			}
		}

		net := variant&1 == 0
		mode := []Mode{ModeReadOnly, ModeWorkspaceWrite, ModeRestricted}[int(variant)%3]
		rp, rerr := Resolve(SandboxPolicy{Mode: mode, Network: &net}, HostFacts{
			OS: "linux", Home: filepath.Join(fixture.root, "home"), BwrapPath: "/fixture/bwrap", BwrapCapable: true,
		}, cwd)
		if rerr != nil {
			var refusal *RefusalError
			if !errors.As(rerr, &refusal) {
				t.Fatalf("Resolve returned non-refusal error %T: %v", rerr, rerr)
			}
			return
		}
		for _, root := range slices.Concat(rp.FileTool.ReadRoots, rp.FileTool.WriteRoots, rp.Spawned.ReadRoots, rp.Spawned.WriteRoots) {
			if rp.Masks(root) {
				t.Fatalf("Resolve granted masked path %q through %q", rp.MaskedPaths, root)
			}
		}
	})
}
