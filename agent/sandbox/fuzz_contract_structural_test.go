//go:build serffuzz

package sandbox

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
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

func newStructuralFixture(t TestingT, includeVariant byte) structuralFixture {
	t.Helper()
	root := resolveCleanPath(t.TempDir())
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

func writeStructuralFile(t TestingT, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write structural fixture %q: %v", path, err)
	}
}

func (f structuralFixture) workspace(kind WorkspaceKind) string {
	switch kind {
	case MainCheckout:
		return f.main
	case LinkedWorktree:
		return f.linked
	case Submodule:
		return f.submodule
	default:
		return f.nonGit
	}
}

// structuralReRootLanes creates the linked-worktree shape that ReRoot operates
// on. The parent main checkout and both lanes are siblings under one temp root;
// all git metadata is materialized directly, not through an external command.
func structuralReRootLanes(t TestingT) (main, laneA, laneB string) {
	t.Helper()
	root := resolveCleanPath(t.TempDir())
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
		// The golden contract itself must pass through a fixture that has no
		// unresolved include glob. It exercises every backend/mode matrix cell
		// without invoking Git or a real sandbox backend.
		assertResolveWith(t, Resolve, func(t TestingT, kind WorkspaceKind) string {
			return newStructuralFixture(t, 0).workspace(kind)
		})
		assertReRootWith(t, ReRootCases(), structuralReRootLanes)

		fixture := newStructuralFixture(t, variant)
		cwd := []string{fixture.main, fixture.linked, fixture.submodule, fixture.nonGit, fixture.malformed, fixture.symlinked}[(int(variant)/3)%6]
		layout, err := ClassifyWorkspace(cwd)
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
				if protected == writable || pathUnder(writable, protected) {
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
			for _, masked := range rp.MaskedPaths {
				if root == masked || pathUnder(root, masked) {
					t.Fatalf("Resolve granted masked path %q through %q", masked, root)
				}
			}
		}
	})
}

func FuzzSandboxContractMaterializers(f *testing.F) {
	for _, seed := range []byte{0, 1, 2} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, _ byte) {
		for _, kind := range []WorkspaceKind{NonGit, MainCheckout, LinkedWorktree} {
			cwd := materializeWorkspaceWith(t, kind, scriptedContractGit)
			layout, err := ClassifyWorkspace(cwd)
			if err != nil {
				t.Fatalf("scripted MaterializeWorkspace(%s): %v", kind, err)
			}
			if layout.Kind != kind {
				t.Fatalf("scripted MaterializeWorkspace(%s) kind = %s", kind, layout.Kind)
			}
		}
		main, laneA, laneB := materializeReRootLanesWith(t, scriptedContractGit)
		for _, lane := range []string{laneA, laneB} {
			layout, err := ClassifyWorkspace(lane)
			if err != nil || layout.Kind != LinkedWorktree || layout.CommonDir != filepath.Join(main, ".git") {
				t.Fatalf("scripted re-root lane %q = (%+v, %v)", lane, layout, err)
			}
		}
	})
}

// scriptedGitTestingT supplies the contract's narrowly-scoped Git materializer
// protocol while forwarding TempDir and reporting to the real test. The public
// contract front doors therefore exercise their normal matrix/oracle behavior
// without an ambient Git binary.
type scriptedGitTestingT struct{ TestingT }

var _ contractGitRunnerProvider = scriptedGitTestingT{}

func (scriptedGitTestingT) contractGitRunner() contractGitRunner { return scriptedContractGit }

func FuzzSandboxPublicContractHarness(f *testing.F) {
	f.Add(byte(0))
	f.Fuzz(func(t *testing.T, _ byte) {
		harness := scriptedGitTestingT{TestingT: t}
		AssertResolve(harness, Resolve)
		AssertReRoot(harness, ReRootCases())

		rec := &recordingT{T: t}
		if got := MaterializeWorkspace(scriptedGitTestingT{TestingT: rec}, WorkspaceKind(99)); got != "" || rec.errors == 0 {
			t.Fatalf("MaterializeWorkspace unsupported kind = (%q, %d errors)", got, rec.errors)
		}
		if got := materializeWorkspaceWith(rec, WorkspaceKind(99), scriptedContractGit); got != "" || rec.errors < 2 {
			t.Fatalf("materializeWorkspaceWith unsupported kind = (%q, %d errors)", got, rec.errors)
		}
		fuzzSandboxResidualBranches(t)
	})
}

func fuzzSandboxResidualBranches(t *testing.T) {
	t.Helper()
	rec := &recordingT{T: t}
	_ = MaterializeWorkspace(rec, NonGit)
	_ = MaterializeWorkspace(rec, MainCheckout)
	_ = MaterializeWorkspace(rec, LinkedWorktree)
	_, _, _ = materializeReRootLanes(rec)
	refusal := ContractCase{Name: "bad-refusal", WantRefusal: true, WantRequiredBackend: "wanted"}
	assertRefusal(rec, refusal, nil)
	assertRefusal(rec, refusal, errors.New("wrong type"))
	assertRefusal(rec, refusal, &RefusalError{RequiredBackend: "other"})
	assertResolveWith(rec, func(policy SandboxPolicy, host HostFacts, cwd string) (ResolvedPolicy, error) {
		if policy.Mode == ModeOff {
			return ResolvedPolicy{}, errors.New("forced")
		}
		return Resolve(policy, host, cwd)
	}, func(t TestingT, kind WorkspaceKind) string { return newStructuralFixture(t, 0).workspace(kind) })
	bad := ContractCase{Name: "bad", Mode: ModeRestricted, WantBackend: BackendSeatbelt, WantNetwork: true,
		WantCache: CacheOverlay, WantFileRead: ReadWorktreeOnly, WantSpawnRead: ReadWorktreeOnly, WantWorktreeWrite: true}
	assertResolution(rec, bad, ResolvedPolicy{
		Backend: BackendBwrap, Network: false, CacheStrategy: CacheNone,
		FileTool:   AccessScope{Read: ReadAnywhere, ReadRoots: []string{"/"}, WriteRoots: []string{"/"}},
		Spawned:    AccessScope{Read: ReadAnywhere, ReadRoots: []string{"/"}, WriteRoots: []string{"/"}},
		SessionTmp: false, MaskedPaths: []string{"/"},
	}, "/work", "/home")
	assertRerooted(rec, ReRootCase{Name: "nil"}, nil, "/main", "/a", "/b")
	assertRerooted(rec, ReRootCase{Name: "off"}, &ResolvedPolicy{}, "/main", "/a", "/b")
	badReroot := &ResolvedPolicy{
		Mode: ModeWorkspaceWrite, Backend: BackendBwrap,
		FileTool: AccessScope{WriteRoots: []string{"/main", "/a"}},
		Spawned:  AccessScope{WriteRoots: []string{"/main", "/a"}},
	}
	assertRerooted(rec, ReRootCase{Name: "bad-write", WantWorktreeWrite: true}, badReroot, "/main", "/a", "/b")
	badReadOnly := *badReroot
	badReadOnly.FileTool.WriteRoots = []string{"/b"}
	badReadOnly.Spawned.WriteRoots = []string{"/b"}
	assertRerooted(rec, ReRootCase{Name: "bad-read-only"}, &badReadOnly, "/main", "/a", "/b")
	assertReRootWith(rec, []ReRootCase{{Name: "default-home", Mode: ModeReadOnly, Net: true, Host: HostFacts{OS: "linux", BwrapCapable: true}}}, structuralReRootLanes)
	assertReRootWith(rec, []ReRootCase{{Name: "bad-source", Mode: ModeReadOnly, Net: true, Host: HostFacts{OS: "linux", Home: "/home", BwrapCapable: true}}}, func(t TestingT) (string, string, string) {
		fixture := newStructuralFixture(t, 0)
		return fixture.main, fixture.malformed, fixture.main
	})
	assertReRootWith(rec, []ReRootCase{{Name: "bad-target", Mode: ModeReadOnly, Net: true, Host: HostFacts{OS: "linux", Home: "/home", BwrapCapable: true}}}, func(t TestingT) (string, string, string) {
		fixture := newStructuralFixture(t, 0)
		return fixture.main, fixture.main, fixture.malformed
	})

	oldSeatbelt := wrapSeatbelt
	wrapSeatbelt = func(argv []string, _ ResolvedPolicy, _, _ string) ([]string, error) {
		return append([]string{"seatbelt"}, argv...), nil
	}
	w := &Wrapper{policy: ResolvedPolicy{Backend: BackendSeatbelt}}
	if got := w.Wrap([]string{"cmd"}, "/work"); len(got) != 2 || got[0] != "seatbelt" {
		t.Fatalf("seatbelt wrap seam = %v", got)
	}
	wrapSeatbelt = oldSeatbelt

	root := t.TempDir()
	session := filepath.Join(root, "session")
	if err := os.MkdirAll(session, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = buildBwrapArgv(ResolvedPolicy{Spawned: AccessScope{Read: ReadAnywhere, ReadRoots: []string{session}}}, session, root)
	oldEval, oldStat := bwrapEvalSymlinks, bwrapStat
	bwrapEvalSymlinks = func(string) (string, error) { return "/fixture/resolved", nil }
	bwrapStat = func(string) (os.FileInfo, error) { return nil, errors.New("stat") }
	maskInvisible(&[]string{}, map[string]bool{}, "fixture")
	bwrapEvalSymlinks, bwrapStat = oldEval, oldStat

	oldAbs := resolveAbs
	resolveAbs = func(string) (string, error) { return "", errors.New("abs") }
	_, _ = Resolve(SandboxPolicy{Mode: ModeReadOnly}, HostFacts{OS: "linux", Home: "/home", BwrapCapable: true}, "relative")
	resolveAbs = oldAbs
	_, _ = ResolveNamed("read-only", nil, HostFacts{OS: "linux", Home: "relative"}, root)
	_, _ = chooseBackend(SandboxPolicy{Mode: ModeReadOnly}, HostFacts{OS: "plan9"}, true)
	_ = filterMasked([]string{"/secret"}, []string{"/secret"})
	faultPolicy := ResolvedPolicy{resolveInputs: SandboxPolicy{Mode: ModeReadOnly}, resolveHost: HostFacts{OS: "linux"}}
	_, _ = faultPolicy.ControlPolicy(root)
	_, _ = (&Wrapper{policy: faultPolicy}).ReRoot(root)
	_ = includePathValue("not-a-path-line")
	oldWalk, oldRel := gitWalkDir, gitRel
	_ = os.MkdirAll(filepath.Join(root, "modules"), 0o755)
	gitWalkDir = func(root string, fn fs.WalkDirFunc) error { return fn(root, nil, errors.New("walk")) }
	_ = moduleConfigProtections(root)
	gitRel = func(string, string) (string, error) { return "", errors.New("rel") }
	_ = pathUnder("/a", "/b")
	gitWalkDir, gitRel = oldWalk, oldRel
	nestedConfig := filepath.Join(root, "nested.conf")
	if err := os.WriteFile(nestedConfig, []byte("[include]\npath = *.conf\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rootConfig := filepath.Join(root, "root.conf")
	if err := os.WriteFile(rootConfig, []byte("[include]\npath = nested.conf\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _ = scanConfigIncludes(rootConfig, []string{root}, map[string]bool{}, 0)

	oldHome := probeUserHomeDir
	probeUserHomeDir = func() (string, error) { return "/fixture/home", nil }
	if home, err := (hostProbeSystem{}).userHomeDir(); err != nil || home != "/fixture/home" {
		t.Fatalf("home seam = %q, %v", home, err)
	}
	probeUserHomeDir = oldHome
	_ = (RealProber{system: nil}).Probe()
	_ = bwrapSupportsOverlay(scriptedProbeSystem{bwrapHelpErr: errors.New("help")}, "/fixture/bwrap")

	oldTemp, oldCache, oldRead := sessionScratchTempDir, sessionScratchUserCacheDir, sessionScratchReadDir
	sessionScratchTempDir = func() string { return root }
	sessionScratchUserCacheDir = func() (string, error) { return root, nil }
	workspace := filepath.Join(root, "different-workspace")
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	tmp, err := NewSessionScratch("", workspace)
	if err != nil {
		t.Fatal(err)
	}
	_ = tmp.Cleanup()
	sessionScratchReadDir = func(string) ([]os.DirEntry, error) { return []os.DirEntry{faultDirEntry{}}, nil }
	sweepCrashedSessionScratch(root)
	sessionScratchTempDir, sessionScratchUserCacheDir, sessionScratchReadDir = oldTemp, oldCache, oldRead
}

type faultDirEntry struct{}

func (faultDirEntry) Name() string               { return sessionScratchPrefix + "fault" }
func (faultDirEntry) IsDir() bool                { return true }
func (faultDirEntry) Type() fs.FileMode          { return fs.ModeDir }
func (faultDirEntry) Info() (fs.FileInfo, error) { return nil, errors.New("info") }

// scriptedContractGit is the smallest external Git boundary needed by the
// contract materializers. It creates the precise .git shapes those helpers need
// and never consults PATH or starts a process.
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
