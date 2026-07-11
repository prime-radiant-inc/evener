//go:build serffuzz

package execenv

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/sandbox"
)

// FuzzSecureFilesystemOperationProgram exercises the enforced file-tool and
// FileMutator surface against a small, real temp-tree model. The fixture is a
// structural main checkout only: sandbox.Resolve reads its .git layout directly,
// and no Git command, shell command, provider, network request, or host process
// path participates. The fake host facts only select the policy shape; this test
// never provisions or invokes a kernel wrapper.
//
// Every accepted mutation is reflected in pfsModel and checked against disk after
// the operation. The protected, outside, and masked sentinel files are deliberately
// outside that mutable model and must retain their exact bytes. Browse results are
// constrained to the permitted worktree surface, while masked content and paths
// must never be returned. A short-lived invocation grant has its own exact-leaf
// oracle so it cannot silently widen to its outside sibling.
func FuzzSecureFilesystemOperationProgram(f *testing.F) {
	for mode := range pfsModes {
		f.Add(pfsSeed(byte(mode)))
	}
	pfsAddFocusedSeeds(f)
	f.Add([]byte{0})
	f.Add([]byte{255, 255, 255, 255, 255, 255, 255})

	f.Fuzz(func(t *testing.T, data []byte) {
		// A fuzz case materializes a real filesystem, so cap the operation stream
		// rather than letting one oversized corpus input dominate a run.
		if len(data) > 97 {
			data = data[:97]
		}
		mode := pfsModes[0]
		if len(data) > 0 {
			mode = pfsModes[int(data[0])%len(pfsModes)]
		}

		fixture, env := pfsNewFixture(t, mode)
		model := pfsInitialModel()
		pfsAssertState(t, fixture, model)

		for step, off := 0, 1; off+2 < len(data) && step < 32; step, off = step+1, off+3 {
			pfsRunOperation(t, env, fixture, model, mode, pfsOperation(data[off]%byte(pfsOpCount)), data[off+1], data[off+2])
			pfsAssertState(t, fixture, model)
		}

		// These checks make the fixed safety boundary part of every corpus replay,
		// including a zero-length input, without relying on fuzzer exploration to
		// reach a sensitive path class.
		pfsAssertBrowseSurface(t, env, fixture, byte(len(data)))
		pfsAssertReadBoundary(t, env, fixture, model, mode)
		pfsAssertEditPolicy(t, env, fixture)
		pfsRunGrant(t, env, fixture, model, mode, byte(len(data)), 0)
		pfsAssertDeniedMutation(t, env, fixture, byte(len(data)%pfsDeniedCaseCount))
		pfsAssertState(t, fixture, model)
		if len(data) <= 1 {
			pfsAssertOffMutator(t)
		}
	})
}

type pfsOperation byte

const (
	pfsOpReadRaw pfsOperation = iota
	pfsOpReadRendered
	pfsOpWriteRaw
	pfsOpWriteRendered
	pfsOpEdit
	pfsOpRemove
	pfsOpRename
	pfsOpList
	pfsOpGlob
	pfsOpGrep
	pfsOpExists
	pfsOpMkdir
	pfsOpDenied
	pfsOpGrant
	pfsOpCount
)

const pfsDeniedCaseCount = 8

var pfsModes = []sandbox.Mode{
	sandbox.ModeRestricted,
	sandbox.ModeWorkspaceWrite,
	sandbox.ModeReadOnly,
}

var pfsManagedPaths = []string{
	"visible.txt",
	"docs/guide.txt",
	"scratch/seed.txt",
	"generated/alpha.txt",
	"generated/beta.txt",
	"rename/from.txt",
	"rename/to.txt",
}

const (
	pfsProtectedMarker = "PFS_PROTECTED_SENTINEL"
	pfsOutsideMarker   = "PFS_OUTSIDE_SENTINEL"
	pfsMaskedMarker    = "PFS_MASKED_SENTINEL"
	pfsReadableMarker  = "PFS_OUTSIDE_READABLE"
)

type pfsFixture struct {
	home            string
	worktree        string
	protected       string
	outsideSentinel string
	outsideReadable string
	maskedDir       string
	maskedSentinel  string
	escapeDir       string
	leafSymlink     string
	grant           string
}

type pfsModel struct {
	files map[string]string
	grant string
}

func pfsSeed(mode byte) []byte {
	seed := []byte{mode}
	for op := pfsOperation(0); op < pfsOpCount; op++ {
		seed = append(seed, byte(op), byte(op*3+1), byte(op*7+2))
	}
	return seed
}

// pfsAddFocusedSeeds makes each finite browse and policy branch reachable from
// deterministic corpus replay. Random fuzzing still mutates the operation stream,
// but coverage measurement only executes the committed seeds.
func pfsAddFocusedSeeds(f *testing.F) {
	add := func(mode int, op pfsOperation, a, b byte) {
		f.Add([]byte{byte(mode), byte(op), a, b})
	}
	for mode := range pfsModes {
		for selector := byte(0); selector < 4; selector++ {
			add(mode, pfsOpReadRaw, selector, selector)
		}
		for selector := byte(0); selector < 3; selector++ {
			add(mode, pfsOpList, selector, 0)
		}
		for selector := byte(0); selector < 5; selector++ {
			add(mode, pfsOpGlob, selector, 0)
		}
		for output := byte(0); output < 3; output++ {
			for options := byte(0); options < 4; options++ {
				add(mode, pfsOpGrep, output, options)
			}
		}
		for selector := byte(0); selector < 5; selector++ {
			add(mode, pfsOpExists, selector, selector)
			add(mode, pfsOpMkdir, selector, 0)
		}
		for selector := byte(0); selector < pfsDeniedCaseCount; selector++ {
			add(mode, pfsOpDenied, selector, 0)
		}
		// The first three model paths exist initially; generated/rename targets
		// exercise the corresponding missing-path behavior without a second setup.
		for _, selector := range []byte{0, 2, 3, 6} {
			add(mode, pfsOpEdit, selector, 1)
			add(mode, pfsOpRemove, selector, 2)
			add(mode, pfsOpRename, selector, (selector+1)%byte(len(pfsManagedPaths)))
		}
	}
}

func pfsNewFixture(t *testing.T, mode sandbox.Mode) (pfsFixture, *LocalExecutionEnvironment) {
	t.Helper()
	root := t.TempDir()
	fixture := pfsFixture{
		home:            filepath.Join(root, "home"),
		worktree:        filepath.Join(root, "home", "project"),
		outsideSentinel: filepath.Join(root, "home", "outside", "sentinel.txt"),
		outsideReadable: filepath.Join(root, "home", "outside", "readable.txt"),
		maskedDir:       filepath.Join(root, "home", "project", "masked"),
		escapeDir:       filepath.Join(root, "home", "project", "escape"),
		leafSymlink:     filepath.Join(root, "home", "project", "leaf-link.txt"),
		grant:           filepath.Join(root, "home", "outside", "grant.txt"),
	}
	fixture.protected = filepath.Join(fixture.worktree, ".git", "config")
	fixture.maskedSentinel = filepath.Join(fixture.maskedDir, "secret.txt")

	for _, dir := range []string{
		filepath.Join(fixture.worktree, ".git", "hooks"),
		filepath.Dir(fixture.outsideSentinel),
		fixture.maskedDir,
		filepath.Join(fixture.worktree, "docs"),
		filepath.Join(fixture.worktree, "scratch"),
		filepath.Join(fixture.worktree, "rename"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %q: %v", dir, err)
		}
	}
	pfsWriteFixtureFile(t, fixture.protected, pfsProtectedMarker+"\n", 0o600)
	pfsWriteFixtureFile(t, filepath.Join(fixture.worktree, ".git", "hooks", "pre-commit"), pfsProtectedMarker+"_HOOK\n", 0o755)
	pfsWriteFixtureFile(t, fixture.outsideSentinel, pfsOutsideMarker+"\n", 0o600)
	pfsWriteFixtureFile(t, fixture.outsideReadable, pfsReadableMarker+"\n", 0o644)
	pfsWriteFixtureFile(t, fixture.maskedSentinel, pfsMaskedMarker+"\n", 0o600)
	pfsWriteFixtureFile(t, fixture.grant, "PFS_GRANT_INITIAL\n", 0o600)
	pfsWriteFixtureFile(t, filepath.Join(fixture.worktree, "visible.txt"), "PFS_VISIBLE_INITIAL\n", 0o644)
	pfsWriteFixtureFile(t, filepath.Join(fixture.worktree, "docs", "guide.txt"), "PFS_GUIDE_INITIAL\n", 0o644)
	pfsWriteFixtureFile(t, filepath.Join(fixture.worktree, "scratch", "seed.txt"), "PFS_SEED_INITIAL\n", 0o644)
	pfsWriteFixtureFile(t, filepath.Join(fixture.worktree, "rename", "from.txt"), "PFS_RENAME_INITIAL\n", 0o644)
	if err := os.Symlink(filepath.Dir(fixture.outsideSentinel), fixture.escapeDir); err != nil {
		t.Fatalf("plant escape symlink: %v", err)
	}
	if err := os.Symlink(fixture.outsideSentinel, fixture.leafSymlink); err != nil {
		t.Fatalf("plant leaf symlink: %v", err)
	}

	// Resolve uses only the tree above and these fixed facts. It does not execute
	// the fake bwrap path; file-tool enforcement is the in-process sandboxFS.
	host := sandbox.HostFacts{
		OS:               "linux",
		Home:             fixture.home,
		BwrapPath:        "/pfs/fake-bwrap",
		BwrapCapable:     true,
		OverlaySupported: true,
	}
	policy, err := sandbox.Resolve(sandbox.SandboxPolicy{
		Mode:        mode,
		DenylistAdd: []string{fixture.maskedDir},
	}, host, fixture.worktree)
	if err != nil {
		t.Fatalf("resolve %v policy: %v", mode, err)
	}
	if !pfsHasPath(policy.Git.ProtectedPaths, fixture.protected) {
		t.Fatalf("resolved policy omitted protected config %q: %v", fixture.protected, policy.Git.ProtectedPaths)
	}
	if !pfsHasPath(policy.MaskedPaths, fixture.maskedDir) {
		t.Fatalf("resolved policy omitted masked tree %q: %v", fixture.maskedDir, policy.MaskedPaths)
	}

	env := NewLocalExecutionEnvironment(fixture.worktree)
	env.Sandbox = &policy
	t.Cleanup(env.Cleanup)
	return fixture, env
}

func pfsWriteFixtureFile(t *testing.T, path, content string, perm os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		t.Fatalf("write fixture %q: %v", path, err)
	}
}

func pfsInitialModel() *pfsModel {
	return &pfsModel{
		files: map[string]string{
			"visible.txt":      "PFS_VISIBLE_INITIAL\n",
			"docs/guide.txt":   "PFS_GUIDE_INITIAL\n",
			"scratch/seed.txt": "PFS_SEED_INITIAL\n",
			"rename/from.txt":  "PFS_RENAME_INITIAL\n",
		},
		grant: "PFS_GRANT_INITIAL\n",
	}
}

func pfsRunOperation(t *testing.T, env *LocalExecutionEnvironment, fixture pfsFixture, model *pfsModel, mode sandbox.Mode, op pfsOperation, a, b byte) {
	t.Helper()
	switch op {
	case pfsOpReadRaw:
		pfsReadRaw(t, env, fixture, model, mode, a, b)
	case pfsOpReadRendered:
		pfsReadRendered(t, env, fixture, model, a, b)
	case pfsOpWriteRaw:
		pfsWriteRaw(t, env, fixture, model, mode, a, b)
	case pfsOpWriteRendered:
		pfsWriteRendered(t, env, fixture, model, mode, a, b)
	case pfsOpEdit:
		pfsEdit(t, env, fixture, model, mode, a, b)
	case pfsOpRemove:
		pfsRemove(t, env, fixture, model, mode, a, b)
	case pfsOpRename:
		pfsRename(t, env, fixture, model, mode, a, b)
	case pfsOpList:
		pfsAssertList(t, env, fixture, int(a%3)+1)
	case pfsOpGlob:
		pfsAssertGlob(t, env, fixture, a)
	case pfsOpGrep:
		pfsAssertGrep(t, env, fixture, a, b)
	case pfsOpExists:
		pfsAssertExists(t, env, fixture, model, a, b)
	case pfsOpMkdir:
		pfsMkdir(t, env, fixture, mode, a)
	case pfsOpDenied:
		pfsAssertDeniedMutation(t, env, fixture, a%pfsDeniedCaseCount)
	case pfsOpGrant:
		pfsRunGrant(t, env, fixture, model, mode, a, b)
	default:
		t.Fatalf("unknown secure filesystem operation %d", op)
	}
}

func pfsReadRaw(t *testing.T, env *LocalExecutionEnvironment, fixture pfsFixture, model *pfsModel, mode sandbox.Mode, a, b byte) {
	t.Helper()
	switch a % 4 {
	case 0:
		rel := pfsManagedPaths[int(b)%len(pfsManagedPaths)]
		want, ok := model.files[rel]
		got, err := env.ReadFileRaw(pfsWorkArg(fixture, rel, a))
		if !ok {
			if err == nil {
				t.Fatalf("read of absent model file %q succeeded with %q", rel, got)
			}
			return
		}
		if err != nil {
			t.Fatalf("read model file %q: %v", rel, err)
		}
		if string(got) != want {
			t.Fatalf("read model file %q = %q, want %q", rel, got, want)
		}
	case 1:
		got, err := env.ReadFileRaw(fixture.outsideReadable)
		if mode == sandbox.ModeRestricted {
			pfsMustDenied(t, err, "restricted read outside readable file")
			return
		}
		if err != nil || string(got) != pfsReadableMarker+"\n" {
			t.Fatalf("%v read-anywhere file = %q, %v", mode, got, err)
		}
	case 2:
		_, err := env.ReadFileRaw(fixture.maskedSentinel)
		pfsMustDenied(t, err, "read masked sentinel")
	case 3:
		target := filepath.Join(fixture.escapeDir, "sentinel.txt")
		if b&1 != 0 {
			target = fixture.leafSymlink
		}
		_, err := env.ReadFileRaw(target)
		pfsMustDenied(t, err, "read through escape symlink")
	}
}

func pfsReadRendered(t *testing.T, env *LocalExecutionEnvironment, fixture pfsFixture, model *pfsModel, a, b byte) {
	t.Helper()
	rel := pfsManagedPaths[int(a)%len(pfsManagedPaths)]
	want, ok := model.files[rel]
	got, err := env.ReadFile(pfsWorkArg(fixture, rel, b), nil, nil)
	if !ok {
		if err == nil {
			t.Fatalf("rendered read of absent model file %q succeeded with %q", rel, got)
		}
		return
	}
	if err != nil {
		t.Fatalf("rendered read %q: %v", rel, err)
	}
	if !strings.Contains(got, want) {
		t.Fatalf("rendered read %q omitted model content %q: %q", rel, want, got)
	}
}

func pfsWriteRaw(t *testing.T, env *LocalExecutionEnvironment, fixture pfsFixture, model *pfsModel, mode sandbox.Mode, a, b byte) {
	t.Helper()
	rel := pfsManagedPaths[int(a)%len(pfsManagedPaths)]
	content := pfsContent("RAW", a, b)
	err := env.WriteFileRaw(pfsWorkArg(fixture, rel, b), []byte(content), 0o640)
	if mode == sandbox.ModeReadOnly {
		pfsMustDenied(t, err, "read-only raw write %q", rel)
		return
	}
	if err != nil {
		t.Fatalf("raw write %q: %v", rel, err)
	}
	model.files[rel] = content
}

func pfsWriteRendered(t *testing.T, env *LocalExecutionEnvironment, fixture pfsFixture, model *pfsModel, mode sandbox.Mode, a, b byte) {
	t.Helper()
	rel := pfsManagedPaths[int(a)%len(pfsManagedPaths)]
	content := pfsContent("WRITE", a, b)
	message, err := env.WriteFile(pfsWorkArg(fixture, rel, b), content)
	if mode == sandbox.ModeReadOnly {
		pfsMustDenied(t, err, "read-only write_file %q", rel)
		return
	}
	if err != nil {
		t.Fatalf("write_file %q: %v", rel, err)
	}
	if !strings.Contains(message, fmt.Sprintf("wrote %d bytes", len(content))) {
		t.Fatalf("write_file %q summary = %q", rel, message)
	}
	model.files[rel] = content
}

func pfsEdit(t *testing.T, env *LocalExecutionEnvironment, fixture pfsFixture, model *pfsModel, mode sandbox.Mode, a, b byte) {
	t.Helper()
	rel := pfsManagedPaths[int(a)%len(pfsManagedPaths)]
	old, exists := model.files[rel]
	if mode == sandbox.ModeReadOnly {
		_, err := env.EditFile(pfsWorkArg(fixture, rel, b), old, pfsContent("EDIT", a, b), false)
		pfsMustDenied(t, err, "read-only edit %q", rel)
		return
	}
	if !exists {
		_, err := env.EditFile(pfsWorkArg(fixture, rel, b), "missing", "replacement", false)
		if err == nil {
			t.Fatalf("edit of absent model file %q succeeded", rel)
		}
		return
	}
	next := pfsContent("EDIT", a, b)
	if _, err := env.EditFile(pfsWorkArg(fixture, rel, b), old, next, false); err != nil {
		t.Fatalf("edit %q: %v", rel, err)
	}
	model.files[rel] = next
}

func pfsRemove(t *testing.T, env *LocalExecutionEnvironment, fixture pfsFixture, model *pfsModel, mode sandbox.Mode, a, b byte) {
	t.Helper()
	rel := pfsManagedPaths[int(a)%len(pfsManagedPaths)]
	err := env.RemovePath(pfsWorkArg(fixture, rel, b))
	if mode == sandbox.ModeReadOnly {
		pfsMustDenied(t, err, "read-only remove %q", rel)
		return
	}
	if err != nil {
		t.Fatalf("remove %q: %v", rel, err)
	}
	delete(model.files, rel)
}

func pfsRename(t *testing.T, env *LocalExecutionEnvironment, fixture pfsFixture, model *pfsModel, mode sandbox.Mode, a, b byte) {
	t.Helper()
	oldRel := pfsManagedPaths[int(a)%len(pfsManagedPaths)]
	newRel := pfsManagedPaths[int(b)%len(pfsManagedPaths)]
	err := env.RenamePath(pfsWorkArg(fixture, oldRel, a), pfsWorkArg(fixture, newRel, b))
	if mode == sandbox.ModeReadOnly {
		pfsMustDenied(t, err, "read-only rename %q to %q", oldRel, newRel)
		return
	}
	content, exists := model.files[oldRel]
	if !exists {
		if err == nil {
			t.Fatalf("rename absent model file %q to %q succeeded", oldRel, newRel)
		}
		return
	}
	if err != nil {
		t.Fatalf("rename %q to %q: %v", oldRel, newRel, err)
	}
	if oldRel != newRel {
		delete(model.files, oldRel)
		model.files[newRel] = content
	}
}

func pfsAssertExists(t *testing.T, env *LocalExecutionEnvironment, fixture pfsFixture, model *pfsModel, a, b byte) {
	t.Helper()
	switch a % 5 {
	case 0:
		if !env.FileExists(fixture.worktree) {
			t.Fatal("sandbox worktree root must exist")
		}
	case 1:
		rel := pfsManagedPaths[int(b)%len(pfsManagedPaths)]
		_, want := model.files[rel]
		if got := env.FileExists(pfsWorkArg(fixture, rel, a)); got != want {
			t.Fatalf("exists(%q) = %v, want %v", rel, got, want)
		}
	case 2:
		if env.FileExists(fixture.maskedSentinel) {
			t.Fatal("masked sentinel existence leaked true")
		}
	case 3:
		target := filepath.Join(fixture.escapeDir, "sentinel.txt")
		if b&1 != 0 {
			target = fixture.leafSymlink
		}
		if env.FileExists(target) {
			t.Fatal("symlinked outside sentinel existence leaked true")
		}
	case 4:
		if env.FileExists(pfsWorkArg(fixture, "absent.txt", b)) {
			t.Fatal("absent in-root file reported as existing")
		}
	}
}

func pfsMkdir(t *testing.T, env *LocalExecutionEnvironment, fixture pfsFixture, mode sandbox.Mode, selector byte) {
	t.Helper()
	sfs := env.sandbox()
	if sfs == nil {
		t.Fatal("enforced fixture did not create a sandbox filesystem")
	}
	var target string
	writable := false
	switch selector % 5 {
	case 0:
		target = filepath.Join(fixture.worktree, "mkdir", "nested", "leaf")
		writable = true
	case 1:
		target = filepath.Join(fixture.maskedDir, "new")
	case 2:
		target = filepath.Join(fixture.worktree, ".git", "hooks", "new")
	case 3:
		target = filepath.Join(filepath.Dir(fixture.outsideSentinel), "new")
	case 4:
		target = fixture.worktree
		writable = true
	}
	err := sfs.mkdirAll("apply_patch", target)
	if mode == sandbox.ModeReadOnly || !writable {
		pfsMustDenied(t, err, "mkdir %q", target)
		return
	}
	if err != nil {
		t.Fatalf("mkdir %q: %v", target, err)
	}
	info, err := os.Stat(target)
	if err != nil || !info.IsDir() {
		t.Fatalf("mkdir %q did not create a directory: info=%v err=%v", target, info, err)
	}
}

func pfsAssertDeniedMutation(t *testing.T, env *LocalExecutionEnvironment, fixture pfsFixture, selector byte) {
	t.Helper()
	switch selector % pfsDeniedCaseCount {
	case 0:
		pfsMustDenied(t, env.WriteFileRaw(fixture.protected, []byte("PFS_ATTACK\n"), 0o600), "write protected config")
	case 1:
		pfsMustDenied(t, env.RemovePath(fixture.protected), "remove protected config")
	case 2:
		source := pfsWorkArg(fixture, pfsManagedPaths[0], selector)
		pfsMustDenied(t, env.RenamePath(source, fixture.protected), "rename into protected config")
	case 3:
		pfsMustDenied(t, env.WriteFileRaw(fixture.outsideSentinel, []byte("PFS_ATTACK\n"), 0o600), "write outside sentinel")
	case 4:
		pfsMustDenied(t, env.RemovePath(fixture.outsideSentinel), "remove outside sentinel")
	case 5:
		pfsMustDenied(t, env.WriteFileRaw(fixture.maskedSentinel, []byte("PFS_ATTACK\n"), 0o600), "write masked sentinel")
	case 6:
		pfsMustDenied(t, env.WriteFileRaw(filepath.Join(fixture.escapeDir, "pwned.txt"), []byte("PFS_ATTACK\n"), 0o600), "write through escape symlink")
	case 7:
		pfsMustDenied(t, env.WriteFileRaw(fixture.leafSymlink, []byte("PFS_ATTACK\n"), 0o600), "write symlink leaf")
	}
}

func pfsAssertBrowseSurface(t *testing.T, env *LocalExecutionEnvironment, fixture pfsFixture, selector byte) {
	t.Helper()
	pfsAssertList(t, env, fixture, int(selector%3)+1)
	pfsAssertGlob(t, env, fixture, selector)
	pfsAssertGrep(t, env, fixture, selector, selector>>1)
	_, err := env.ListDirectory(fixture.maskedDir, 1)
	pfsMustDenied(t, err, "list masked directory")
}

func pfsAssertList(t *testing.T, env *LocalExecutionEnvironment, fixture pfsFixture, depth int) {
	t.Helper()
	entries, err := env.ListDirectory(".", depth)
	if err != nil {
		t.Fatalf("list worktree depth=%d: %v", depth, err)
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		if seen[entry.Name] {
			t.Fatalf("list returned duplicate entry %q", entry.Name)
		}
		seen[entry.Name] = true
		pfsAssertReturnedRel(t, fixture, entry.Name, "list")
	}
}

func pfsAssertGlob(t *testing.T, env *LocalExecutionEnvironment, fixture pfsFixture, selector byte) {
	t.Helper()
	patterns := []string{"*.txt", "**/*.txt", "docs/**/*.txt", "masked/**/*.txt", "escape/**/*.txt"}
	pattern := patterns[int(selector)%len(patterns)]
	matches, err := env.Glob(pattern, ".")
	if err != nil {
		t.Fatalf("glob(%q): %v", pattern, err)
	}
	again, err := env.Glob(pattern, ".")
	if err != nil {
		t.Fatalf("repeat glob(%q): %v", pattern, err)
	}
	if strings.Join(matches, "\x00") != strings.Join(again, "\x00") {
		t.Fatalf("glob(%q) was not deterministic: %v then %v", pattern, matches, again)
	}
	for _, match := range matches {
		if pfsForbiddenReturnedPath(match, fixture) {
			t.Fatalf("glob(%q) returned impermitted path %q", pattern, match)
		}
		if _, err := os.Lstat(match); err != nil {
			t.Fatalf("glob(%q) returned missing path %q: %v", pattern, match, err)
		}
	}
}

func pfsAssertGrep(t *testing.T, env *LocalExecutionEnvironment, fixture pfsFixture, a, b byte) {
	t.Helper()
	outputModes := []string{"content", "files_with_matches", "count"}
	outputMode := outputModes[int(a)%len(outputModes)]
	filter := ""
	if b&1 != 0 {
		filter = "*.txt"
	}
	maxResults := int(b%4) + 1
	out, err := env.Grep("PFS_", ".", filter, b&2 != 0, maxResults, outputMode)
	if err != nil {
		t.Fatalf("grep(%q, %q): %v", outputMode, filter, err)
	}
	again, err := env.Grep("PFS_", ".", filter, b&2 != 0, maxResults, outputMode)
	if err != nil {
		t.Fatalf("repeat grep(%q, %q): %v", outputMode, filter, err)
	}
	if out != again {
		t.Fatalf("grep(%q, %q) was not deterministic: %q then %q", outputMode, filter, out, again)
	}
	for _, marker := range []string{pfsProtectedMarker, pfsOutsideMarker, pfsMaskedMarker} {
		if strings.Contains(out, marker) {
			t.Fatalf("grep(%q, %q) leaked sentinel marker %q in %q", outputMode, filter, marker, out)
		}
	}
	if out == "" {
		return
	}
	for _, line := range strings.Split(out, "\n") {
		rel := line
		if before, _, ok := strings.Cut(line, ":"); ok {
			rel = before
		}
		pfsAssertReturnedRel(t, fixture, rel, "grep")
	}
}

func pfsAssertReadBoundary(t *testing.T, env *LocalExecutionEnvironment, fixture pfsFixture, model *pfsModel, mode sandbox.Mode) {
	t.Helper()
	pfsReadRaw(t, env, fixture, model, mode, 2, 0) // masked
	pfsReadRaw(t, env, fixture, model, mode, 3, 0) // symlink
	pfsReadRaw(t, env, fixture, model, mode, 1, 0) // read-anywhere branch
}

func pfsAssertEditPolicy(t *testing.T, env *LocalExecutionEnvironment, fixture pfsFixture) {
	t.Helper()
	for _, target := range []string{fixture.protected, fixture.maskedSentinel, fixture.outsideSentinel} {
		_, err := env.EditFile(target, "PFS_ATTACK", "PFS_REPLACEMENT", false)
		pfsMustDenied(t, err, "edit immutable path %q", target)
	}
}

// pfsAssertOffMutator covers mutator.go's non-sandboxed containment fallback.
// It deliberately uses fresh temp roots and no injected process seam: the same
// relative/absolute contract must hold before a policy is attached.
func pfsAssertOffMutator(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	outsideRoot := t.TempDir()
	env := NewLocalExecutionEnvironment(root)
	t.Cleanup(env.Cleanup)

	if err := env.WriteFileRaw("nested/source.txt", []byte("PFS_OFF_SOURCE\n"), 0o640); err != nil {
		t.Fatalf("off mutator write: %v", err)
	}
	got, err := env.ReadFileRaw("nested/source.txt")
	if err != nil || string(got) != "PFS_OFF_SOURCE\n" {
		t.Fatalf("off mutator read = %q, %v", got, err)
	}
	if err := env.RenamePath("nested/source.txt", "moved/destination.txt"); err != nil {
		t.Fatalf("off mutator rename: %v", err)
	}
	if err := env.RemovePath("moved/destination.txt"); err != nil {
		t.Fatalf("off mutator remove: %v", err)
	}
	if err := env.RemovePath("missing/parent/file.txt"); err != nil {
		t.Fatalf("off mutator missing remove: %v", err)
	}

	outside := filepath.Join(outsideRoot, "sentinel.txt")
	pfsWriteFixtureFile(t, outside, "PFS_OFF_OUTSIDE\n", 0o600)
	if _, err := env.ReadFileRaw(outside); err == nil {
		t.Fatal("off mutator read escaped its root")
	}
	if err := env.WriteFileRaw(outside, []byte("PFS_ATTACK\n"), 0o600); err == nil {
		t.Fatal("off mutator write escaped its root")
	}
	if err := env.RemovePath(outside); err == nil {
		t.Fatal("off mutator remove escaped its root")
	}
	if err := env.RenamePath("moved/destination.txt", outside); err == nil {
		t.Fatal("off mutator rename escaped its root")
	}
	pfsAssertFixtureBytes(t, outside, "PFS_OFF_OUTSIDE\n", "off outside")
}

func pfsRunGrant(t *testing.T, env *LocalExecutionEnvironment, fixture pfsFixture, model *pfsModel, mode sandbox.Mode, a, b byte) {
	t.Helper()
	grant, ok := env.WithSandboxInvocationGrant(fixture.grant).(*LocalExecutionEnvironment)
	if !ok {
		t.Fatal("local environment grant did not return a local environment")
	}
	t.Cleanup(grant.Cleanup)

	got, err := grant.ReadFileRaw(fixture.grant)
	if err != nil || string(got) != model.grant {
		t.Fatalf("grant read = %q, %v; want %q", got, err, model.grant)
	}
	next := pfsContent("GRANT", a, b)
	if err := grant.WriteFileRaw(fixture.grant, []byte(next), 0o600); err != nil {
		t.Fatalf("grant write: %v", err)
	}
	model.grant = next

	// A grant is one exact leaf. It enables no outside sibling mutation, in every
	// mode, even though read-only/workspace-write may independently read elsewhere.
	pfsMustDenied(t, grant.WriteFileRaw(fixture.outsideSentinel, []byte("PFS_ATTACK\n"), 0o600), "grant write outside sibling")
	if mode == sandbox.ModeRestricted {
		_, err := grant.ReadFileRaw(fixture.outsideSentinel)
		pfsMustDenied(t, err, "restricted grant read outside sibling")
	}

	maskedGrant, ok := env.WithSandboxInvocationGrant(fixture.maskedSentinel).(*LocalExecutionEnvironment)
	if !ok {
		t.Fatal("masked local grant did not return a local environment")
	}
	t.Cleanup(maskedGrant.Cleanup)
	_, err = maskedGrant.ReadFileRaw(fixture.maskedSentinel)
	pfsMustDenied(t, err, "grant read masked sentinel")

	protectedGrant, ok := env.WithSandboxInvocationGrant(fixture.protected).(*LocalExecutionEnvironment)
	if !ok {
		t.Fatal("protected local grant did not return a local environment")
	}
	t.Cleanup(protectedGrant.Cleanup)
	pfsMustDenied(t, protectedGrant.WriteFileRaw(fixture.protected, []byte("PFS_ATTACK\n"), 0o600), "grant write protected config")
}

func pfsAssertState(t *testing.T, fixture pfsFixture, model *pfsModel) {
	t.Helper()
	for _, rel := range pfsManagedPaths {
		path := filepath.Join(fixture.worktree, filepath.FromSlash(rel))
		want, exists := model.files[rel]
		got, err := os.ReadFile(path)
		if !exists {
			if err == nil {
				t.Fatalf("model says %q is absent, disk has %q", rel, got)
			}
			if !os.IsNotExist(err) {
				t.Fatalf("model says %q is absent, disk read failed with %v", rel, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("model file %q missing from disk: %v", rel, err)
		}
		if string(got) != want {
			t.Fatalf("model file %q = %q, want %q", rel, got, want)
		}
	}
	pfsAssertFixtureBytes(t, fixture.protected, pfsProtectedMarker+"\n", "protected")
	pfsAssertFixtureBytes(t, fixture.outsideSentinel, pfsOutsideMarker+"\n", "outside")
	pfsAssertFixtureBytes(t, fixture.maskedSentinel, pfsMaskedMarker+"\n", "masked")
	pfsAssertFixtureBytes(t, fixture.grant, model.grant, "grant")
	pfsAssertSymlink(t, fixture.escapeDir, "escape directory")
	pfsAssertSymlink(t, fixture.leafSymlink, "leaf")
	pfsAssertNoAtomicTemps(t, fixture.worktree)
}

func pfsAssertFixtureBytes(t *testing.T, path, want, label string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s sentinel %q: %v", label, path, err)
	}
	if string(got) != want {
		t.Fatalf("%s sentinel %q = %q, want %q", label, path, got, want)
	}
}

func pfsAssertSymlink(t *testing.T, path, label string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %s symlink %q: %v", label, path, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s %q is no longer a symlink: mode=%v", label, path, info.Mode())
	}
}

func pfsAssertNoAtomicTemps(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(entry.Name(), ".serf-sbtmp-") {
			return fmt.Errorf("stray atomic temp %q", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func pfsAssertReturnedRel(t *testing.T, fixture pfsFixture, rel, tool string) {
	t.Helper()
	if rel == "" || filepath.IsAbs(rel) {
		t.Fatalf("%s returned invalid relative path %q", tool, rel)
	}
	abs := filepath.Clean(filepath.Join(fixture.worktree, filepath.FromSlash(rel)))
	if pfsForbiddenReturnedPath(abs, fixture) {
		t.Fatalf("%s returned impermitted path %q", tool, rel)
	}
	if _, err := os.Lstat(abs); err != nil {
		t.Fatalf("%s returned missing path %q: %v", tool, rel, err)
	}
}

func pfsWorkArg(fixture pfsFixture, rel string, variant byte) string {
	rel = filepath.FromSlash(rel)
	switch variant % 3 {
	case 0:
		return rel
	case 1:
		return filepath.Join(".", rel)
	default:
		return filepath.Join(fixture.worktree, "nested", "..", rel)
	}
}

func pfsContent(kind string, a, b byte) string {
	return fmt.Sprintf("PFS_%s_%02X_%02X\n", kind, a, b)
}

func pfsMustDenied(t *testing.T, err error, format string, args ...any) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected a sandbox denial", fmt.Sprintf(format, args...))
	}
	var denied *sandbox.DeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("%s: expected *sandbox.DeniedError, got %T: %v", fmt.Sprintf(format, args...), err, err)
	}
}

func pfsHasPath(paths []string, want string) bool {
	for _, path := range paths {
		if path == want {
			return true
		}
	}
	return false
}

func pfsUnder(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// pfsForbiddenReturnedPath allows an in-root symlink leaf to be reported (the
// list contract exposes it as IsSymlink) but rejects any descendant that would
// imply traversal through that leaf. Masked paths are never reportable at all.
func pfsForbiddenReturnedPath(path string, fixture pfsFixture) bool {
	path = filepath.Clean(path)
	if !pfsUnder(path, fixture.worktree) || pfsUnder(path, fixture.maskedDir) {
		return true
	}
	return pfsUnder(path, fixture.escapeDir) && path != filepath.Clean(fixture.escapeDir)
}
