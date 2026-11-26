package sandboxtest

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/identifier"
)

type recordingT struct {
	*testing.T
	errors int
}

func (r *recordingT) Errorf(string, ...any) { r.errors++ }
func (r *recordingT) Fatalf(string, ...any) { r.errors++ }

func writeStructuralFile(t TestingT, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write structural fixture %q: %v", path, err)
	}
}

func scriptedContractGit(t TestingT, dir string, args ...string) {
	t.Helper()
	if len(args) == 0 {
		t.Fatalf("scripted git called without args")
	}
	switch args[0] {
	case "init":
		gitDir := filepath.Join(dir, ".git")
		for _, path := range []string{
			filepath.Join(gitDir, "objects"),
			filepath.Join(gitDir, "refs"),
			filepath.Join(gitDir, "logs"),
			filepath.Join(gitDir, "hooks"),
			filepath.Join(gitDir, "worktrees"),
		} {
			if err := os.MkdirAll(path, 0o755); err != nil {
				t.Fatalf("scripted git init mkdir %q: %v", path, err)
			}
		}
		writeStructuralFile(t, filepath.Join(gitDir, "config"), "[core]\nrepositoryformatversion = 0\n")
	case "commit":
		return
	case "worktree":
		if len(args) < 2 || args[1] != "add" {
			t.Fatalf("unsupported scripted git worktree args: %v", args)
		}
		path := args[len(args)-1]
		id := filepath.Base(path)
		gitDir := filepath.Join(dir, ".git", "worktrees", id)
		if err := os.MkdirAll(gitDir, 0o755); err != nil {
			t.Fatalf("scripted git worktree metadata %q: %v", gitDir, err)
		}
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("scripted git worktree root %q: %v", path, err)
		}
		writeStructuralFile(t, filepath.Join(path, ".git"), "gitdir: "+gitDir+"\n")
	default:
		t.Fatalf("unsupported scripted git command: %v", args)
	}
}

func structuralWorkspace(t TestingT, kind sandbox.WorkspaceKind) string {
	t.Helper()
	return materializeWorkspaceWith(t, kind, scriptedContractGit)
}

func structuralReRootLanes(t TestingT) (main, laneA, laneB string) {
	t.Helper()
	return materializeReRootLanesWith(t, scriptedContractGit)
}

func TestContractMaterializersWithScriptedGit(t *testing.T) {
	t.Parallel()
	for _, kind := range []sandbox.WorkspaceKind{sandbox.NonGit, sandbox.MainCheckout, sandbox.LinkedWorktree} {
		cwd := materializeWorkspaceWith(t, kind, scriptedContractGit)
		layout, err := sandbox.ClassifyWorkspace(cwd, nil)
		if err != nil {
			t.Fatalf("scripted MaterializeWorkspace(%s): %v", kind, err)
		}
		if layout.Kind != kind {
			t.Fatalf("scripted MaterializeWorkspace(%s) kind = %s", kind, layout.Kind)
		}
	}
	main, laneA, laneB := materializeReRootLanesWith(t, scriptedContractGit)
	for _, lane := range []string{laneA, laneB} {
		layout, err := sandbox.ClassifyWorkspace(lane, nil)
		if err != nil || layout.Kind != sandbox.LinkedWorktree || layout.CommonDir != filepath.Join(main, ".git") {
			t.Fatalf("scripted re-root lane %q = (%+v, %v)", lane, layout, err)
		}
	}

	assertResolveWith(t, sandbox.Resolve, structuralWorkspace)
	assertReRootWith(t, ReRootCases(), structuralReRootLanes)

	rec := &recordingT{T: t}
	if got := MaterializeWorkspace(rec, sandbox.WorkspaceKind(99)); got != "" || rec.errors == 0 {
		t.Fatalf("MaterializeWorkspace unsupported kind = (%q, %d errors)", got, rec.errors)
	}
	if got := materializeWorkspaceWith(rec, sandbox.WorkspaceKind(99), scriptedContractGit); got != "" || rec.errors < 2 {
		t.Fatalf("materializeWorkspaceWith unsupported kind = (%q, %d errors)", got, rec.errors)
	}
}

func TestContractOracleRejections(t *testing.T) {
	t.Parallel()
	rec := &recordingT{T: t}
	caseRefusal := ContractCase{Name: "fixture-refusal", WantRequiredBackend: "bwrap"}
	assertRefusal(rec, caseRefusal, nil)
	assertRefusal(rec, caseRefusal, errors.New("not a refusal"))
	assertRefusal(rec, caseRefusal, &sandbox.RefusalError{RequiredBackend: "seatbelt"})
	if rec.errors < 3 {
		t.Fatalf("assertRefusal failed to reject invalid outcomes: %d", rec.errors)
	}

	home := filepath.Join(identifier.ResolveClean(t.TempDir()), "home")
	rec.errors = 0
	assertResolution(rec, ContractCase{
		Name: "fixture-resolution", Mode: sandbox.ModeRestricted, WantBackend: sandbox.BackendBwrap, WantNetwork: true,
		WantCache: sandbox.CacheSessionPrivate, WantFileRead: sandbox.ReadWorktreeOnly, WantSpawnRead: sandbox.ReadWorktreeOnly,
		WantWorktreeWrite: true,
	}, sandbox.ResolvedPolicy{
		Mode:        sandbox.ModeRestricted,
		MaskedPaths: []string{"/proc"},
		FileTool: sandbox.AccessScope{
			Read: sandbox.ReadWorktreeOnly, ReadRoots: []string{"/usr", "/"}, WriteRoots: []string{"/proc"},
		},
		Spawned: sandbox.AccessScope{Read: sandbox.ReadWorktreeOnly, WriteRoots: []string{"/proc"}},
	}, "/work", home)
	if rec.errors == 0 {
		t.Fatal("assertResolution accepted an incomplete enforced policy")
	}

	rec.errors = 0
	assertResolveWith(rec, func(sandbox.SandboxPolicy, sandbox.HostFacts, string) (sandbox.ResolvedPolicy, error) {
		return sandbox.ResolvedPolicy{}, errors.New("resolver failed")
	}, structuralWorkspace)
	if rec.errors == 0 {
		t.Fatal("assertResolveWith accepted a failing resolver")
	}

	rec.errors = 0
	assertRerooted(rec, ReRootCase{Name: "nil"}, nil, "/main", "/a", "/b")
	assertRerooted(rec, ReRootCase{Name: "off"}, &sandbox.ResolvedPolicy{}, "/main", "/a", "/b")
	badReroot := &sandbox.ResolvedPolicy{
		Mode: sandbox.ModeWorkspaceWrite, Backend: sandbox.BackendBwrap,
		FileTool: sandbox.AccessScope{WriteRoots: []string{"/main", "/a"}},
		Spawned:  sandbox.AccessScope{WriteRoots: []string{"/main", "/a"}},
	}
	assertRerooted(rec, ReRootCase{Name: "bad-write", WantWorktreeWrite: true}, badReroot, "/main", "/a", "/b")
	badReadOnly := *badReroot
	badReadOnly.FileTool.WriteRoots = []string{"/b"}
	badReadOnly.Spawned.WriteRoots = []string{"/b"}
	assertRerooted(rec, ReRootCase{Name: "bad-read-only"}, &badReadOnly, "/main", "/a", "/b")
	if rec.errors < 4 {
		t.Fatalf("assertRerooted failed to reject invalid re-roots: %d", rec.errors)
	}

	rec.errors = 0
	assertReRootWith(rec, []ReRootCase{{
		Name: "unavailable", Mode: sandbox.ModeRestricted, Net: true, Host: sandbox.HostFacts{OS: "linux", Home: home},
	}}, structuralReRootLanes)
	if rec.errors == 0 {
		t.Fatal("assertReRootWith accepted an unavailable backend")
	}

	rec.errors = 0
	assertReRootWith(rec, []ReRootCase{{
		Name: "off", Mode: sandbox.ModeOff, Net: true, Host: sandbox.HostFacts{OS: "linux", Home: home},
	}}, structuralReRootLanes)
	if rec.errors == 0 {
		t.Fatal("assertReRootWith accepted an off re-root result")
	}

	rec.errors = 0
	assertReRootWith(rec, []ReRootCase{{
		Name: "malformed-target", Mode: sandbox.ModeRestricted, Net: true, Host: sandbox.HostFacts{
			OS: "linux", Home: home, BwrapPath: "/fixture/bwrap", BwrapCapable: true,
		},
	}}, func(t TestingT) (string, string, string) {
		main, laneA, _ := structuralReRootLanes(t)
		bad := filepath.Join(filepath.Dir(main), "bad-lane")
		if err := os.MkdirAll(bad, 0o755); err != nil {
			t.Fatalf("mkdir malformed re-root lane: %v", err)
		}
		writeStructuralFile(t, filepath.Join(bad, ".git"), "invalid pointer\n")
		return main, laneA, bad
	})
	if rec.errors == 0 {
		t.Fatal("assertReRootWith accepted a malformed target workspace")
	}
}
