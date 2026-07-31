//go:build serffuzz && linux

package execenv

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
	"primeradiant.com/serf/agent/sandbox"
)

// FuzzSecurePathEdgeContractProgram covers the fd-anchored security boundary's
// error and cleanup contracts that a normal success-path program cannot force:
// masked/protected redaction, symlink/escape classification, invalid-fd safety,
// no escape through a ".." component, and best-effort directory removal. Every
// filesystem path is under t.TempDir; no process, network, Git command, or host
// path is used.
func FuzzSecurePathEdgeContractProgram(f *testing.F) {
	for _, seed := range [][]byte{
		nil,
		{0},
		{1, 2, 3},
		{0xff, 0x00, 0x7f},
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, program []byte) {
		if len(program) > 48 {
			program = program[:48]
		}
		first := runSecurePathEdgeContractProgram(t, program)
		second := runSecurePathEdgeContractProgram(t, program)
		if !reflect.DeepEqual(first, second) {
			t.Fatalf("secure-path edge contracts are not deterministic:\nfirst=%#v\nsecond=%#v", first, second)
		}
	})
}

type securePathEdgeTrace struct {
	Denials   []sandbox.DenialReason
	Redacted  []string
	Removed   []string
	Contained []string
}

func runSecurePathEdgeContractProgram(t *testing.T, program []byte) securePathEdgeTrace {
	t.Helper()
	base := t.TempDir()
	home := filepath.Join(base, "home")
	worktree := filepath.Join(home, "worktree")
	outside := filepath.Join(base, "outside")
	masked := filepath.Join(worktree, "masked")
	maskedFile := filepath.Join(worktree, "masked-file.txt")
	protected := filepath.Join(worktree, ".git", "config")
	for _, dir := range []string{filepath.Join(worktree, ".git"), masked, filepath.Join(worktree, "remove-dir"), filepath.Join(worktree, "rootfd"), outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("make edge fixture %q: %v", dir, err)
		}
	}
	if err := os.WriteFile(protected, []byte("protected"), 0o600); err != nil {
		t.Fatalf("write protected fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(masked, "secret.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatalf("write masked fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "remove-dir", "child.txt"), []byte("child"), 0o600); err != nil {
		t.Fatalf("write removable fixture: %v", err)
	}
	if err := os.WriteFile(maskedFile, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write masked-file fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".hidden.txt"), []byte("needle hidden"), 0o600); err != nil {
		t.Fatalf("write hidden fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "binary.bin"), []byte("needle\x00binary"), 0o600); err != nil {
		t.Fatalf("write binary fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "visible.txt"), []byte("needle visible\n"), 0o600); err != nil {
		t.Fatalf("write visible fixture: %v", err)
	}

	policy, err := sandbox.Resolve(sandbox.SandboxPolicy{
		Mode:        sandbox.ModeWorkspaceWrite,
		DenylistAdd: []string{masked, maskedFile},
	}, sandbox.HostFacts{OS: "linux", Home: home, BwrapCapable: true, BwrapPath: "/fixture/bwrap"}, worktree)
	if err != nil {
		t.Fatalf("resolve edge policy: %v", err)
	}
	s := newSandboxFS(&policy, "")
	defer s.close()
	trace := securePathEdgeTrace{}

	// The audit sink receives only redacted paths. These calls drive all typed
	// denial reasons, including the less common root-target and escape cases.
	originalSink := auditSink
	defer func() { auditSink = originalSink }()
	auditSink = nil
	auditDenial(policy.Mode, "read", "relative", denyReasonOutsideRead)
	var records []AuditRecord
	auditSink = func(record AuditRecord) { records = append(records, record) }
	denialCases := []struct {
		reason string
		path   string
		kind   sandbox.DenialReason
		want   string
	}{
		{denyReasonOutsideRead, filepath.Join(base, "outside", "visible.txt"), sandbox.DenialOutsideReadRoots, "visible.txt"},
		{denyReasonOutsideWrite, "relative.txt", sandbox.DenialOutsideWriteRoots, "relative.txt"},
		{denyReasonWriteDenied, filepath.Join(base, "readonly.txt"), sandbox.DenialWritesDisabled, "readonly.txt"},
		{denyReasonMasked, filepath.Join(masked, "secret.txt"), sandbox.DenialMasked, "<denied>"},
		{denyReasonProtected, protected, sandbox.DenialGitProtected, "<denied>"},
		{denyReasonSymlink, filepath.Join(worktree, "link"), sandbox.DenialSymlink, "link"},
		{denyReasonEscape, filepath.Join(worktree, "escape"), sandbox.DenialEscape, "escape"},
		{denyReasonRootTarget, worktree, sandbox.DenialRootTarget, "worktree"},
		{"unknown reason", "", sandbox.DenialUnspecified, ""},
	}
	for _, tc := range denialCases {
		denied := s.deny("edge", tc.path, tc.reason)
		if denied.ReasonKind != tc.kind {
			t.Fatalf("denial %q kind=%v, want %v", tc.reason, denied.ReasonKind, tc.kind)
		}
		if tc.reason == denyReasonMasked && !denied.Sensitive {
			t.Fatalf("masked denial was not sensitive: %+v", denied)
		}
		trace.Denials = append(trace.Denials, denied.ReasonKind)
	}
	if len(records) != len(denialCases) {
		t.Fatalf("audit records=%d, want %d", len(records), len(denialCases))
	}
	for i, tc := range denialCases {
		if records[i].Path != tc.want || records[i].Reason != tc.reason || records[i].Tool != "edge" {
			t.Fatalf("audit record %d=%+v, want redacted path %q", i, records[i], tc.want)
		}
		trace.Redacted = append(trace.Redacted, records[i].Path)
	}
	if got := redactAuditPath("", denyReasonOutsideRead); got != "" {
		t.Fatalf("empty audit path = %q", got)
	}

	// Resolver error mapping must reveal a safe typed denial, never raw path-walk
	// internals. An ordinary ENOENT remains ordinary I/O so callers retain that
	// distinction.
	for _, tc := range []struct {
		err  error
		kind sandbox.DenialReason
	}{
		{unix.ELOOP, sandbox.DenialSymlink},
		{errSymlinkComponent, sandbox.DenialSymlink},
		{unix.EXDEV, sandbox.DenialEscape},
		{errEscapesRoot, sandbox.DenialEscape},
	} {
		mapped := s.mapOpenErr("edge", filepath.Join(worktree, "candidate"), tc.err)
		var denied *sandbox.DeniedError
		if !errors.As(mapped, &denied) || denied.ReasonKind != tc.kind {
			t.Fatalf("mapOpenErr(%v)=%v, want denial %v", tc.err, mapped, tc.kind)
		}
	}
	if mapped := s.mapOpenErr("edge", "missing", unix.ENOENT); !errors.Is(mapped, unix.ENOENT) {
		t.Fatalf("mapOpenErr ENOENT = %v, want preserved ENOENT", mapped)
	}
	for _, err := range []error{errSymlinkComponent, errEscapesRoot, unix.ELOOP, unix.EXDEV} {
		if got := toFsErr(err); !errors.Is(got, fs.ErrNotExist) {
			t.Fatalf("toFsErr(%v)=%v, want fs.ErrNotExist", err, got)
		}
	}
	if got := toFsErr(unix.ENOENT); !errors.Is(got, unix.ENOENT) {
		t.Fatalf("toFsErr ENOENT=%v, want preserved", got)
	}

	// Invalid descriptors are a deterministic no-host boundary: every primitive
	// must return an error rather than panic or create an escape path.
	if err := atomicWriteAt(-1, "never", []byte("data"), 0o600); err == nil {
		t.Fatal("atomicWriteAt invalid fd unexpectedly succeeded")
	}
	if err := writeAllFd(-1, []byte("data")); err == nil {
		t.Fatal("writeAllFd invalid fd unexpectedly succeeded")
	}
	if _, err := readDirEntries(-1); err == nil {
		t.Fatal("readDirEntries invalid fd unexpectedly succeeded")
	}
	if _, err := openat2Retry(-1, "never", &unix.OpenHow{Flags: unix.O_RDONLY}); err == nil {
		t.Fatal("openat2Retry invalid fd unexpectedly succeeded")
	}
	if err := s.recheckMaskedFd("edge", "missing", -1); err != nil {
		t.Fatalf("linux recheck missing fd = %v, want best-effort nil", err)
	}
	if err := s.recheckWriteTargetFd("edge", "missing", -1, "leaf"); err != nil {
		t.Fatalf("linux write recheck missing fd = %v, want best-effort nil", err)
	}

	// Every fd-anchored resolver failure below is kept inside the fixture. These
	// assertions distinguish ordinary missing paths from the typed policy denials
	// that prevent traversal through a writable root.
	missingRoot := filepath.Join(base, "missing-root")
	if _, err := s.rootFd(missingRoot); err == nil {
		t.Fatal("rootFd missing root unexpectedly succeeded")
	}
	if _, err := s.openAnywhereMinusMasked("edge", filepath.Join(base, "missing-anywhere"), unix.O_RDONLY); err == nil {
		t.Fatal("openAnywhereMinusMasked missing path unexpectedly succeeded")
	}
	if _, err := s.openInRoot("edge", filepath.Join(missingRoot, "leaf"), missingRoot, "leaf", unix.O_RDONLY); err == nil {
		t.Fatal("openInRoot missing root unexpectedly succeeded")
	}
	if _, err := s.openInRoot("edge", filepath.Join(worktree, "missing-leaf"), worktree, "missing-leaf", unix.O_RDONLY); err == nil {
		t.Fatal("openInRoot missing leaf unexpectedly succeeded")
	}
	if _, _, err := s.openWriteParent("edge", worktree, false); !securePathEdgeDenied(err, sandbox.DenialRootTarget) {
		t.Fatalf("openWriteParent root target = %v, want root-target denial", err)
	}

	symlinkOutside := filepath.Join(outside, "symlink-target")
	if err := os.MkdirAll(symlinkOutside, 0o755); err != nil {
		t.Fatalf("make symlink target: %v", err)
	}
	symlinkDir := filepath.Join(worktree, "symlink-dir")
	if err := os.Symlink(symlinkOutside, symlinkDir); err != nil {
		t.Fatalf("make write symlink: %v", err)
	}
	if _, _, err := s.openWriteParent("edge", filepath.Join(symlinkDir, "leaf"), true); !securePathEdgeDenied(err, sandbox.DenialSymlink) {
		t.Fatalf("openWriteParent symlink intermediate = %v, want symlink denial", err)
	}

	// A grant widens containment for exactly one leaf, not masking, git protection,
	// root targeting, or parent creation. Exercise success and each refusal directly.
	s.grant = filepath.Join(masked, "secret.txt")
	if _, _, err := s.grantedWriteParent("edge", s.grant); !securePathEdgeDenied(err, sandbox.DenialMasked) {
		t.Fatalf("granted masked write = %v, want masked denial", err)
	}
	s.grant = protected
	if _, _, err := s.grantedWriteParent("edge", s.grant); !securePathEdgeDenied(err, sandbox.DenialGitProtected) {
		t.Fatalf("granted protected write = %v, want protected denial", err)
	}
	s.grant = string(filepath.Separator)
	if _, _, err := s.grantedWriteParent("edge", s.grant); !securePathEdgeDenied(err, sandbox.DenialRootTarget) {
		t.Fatalf("granted root write = %v, want root-target denial", err)
	}
	s.grant = filepath.Join(outside, "missing-parent", "leaf")
	if _, _, err := s.grantedWriteParent("edge", s.grant); err == nil {
		t.Fatal("granted missing parent unexpectedly succeeded")
	}
	s.grant = filepath.Join(outside, "granted.txt")
	grantFD, grantLeaf, err := s.grantedWriteParent("edge", s.grant)
	if err != nil || grantLeaf != "granted.txt" {
		t.Fatalf("granted existing parent = (fd=%d leaf=%q err=%v)", grantFD, grantLeaf, err)
	}
	if err := unix.Close(grantFD); err != nil {
		t.Fatalf("close granted parent: %v", err)
	}
	s.grant = ""

	missingRootPolicy := policy
	missingRootPolicy.FileTool.WriteRoots = []string{missingRoot}
	missingRootFS := newSandboxFS(&missingRootPolicy, "")
	if err := missingRootFS.mkdirAll("edge", filepath.Join(missingRoot, "child")); err == nil {
		missingRootFS.close()
		t.Fatal("mkdirAll missing root unexpectedly succeeded")
	}
	missingRootFS.close()
	if err := s.mkdirAll("edge", filepath.Join(symlinkDir, "created")); !securePathEdgeDenied(err, sandbox.DenialSymlink) {
		t.Fatalf("mkdirAll symlink intermediate = %v, want symlink denial", err)
	}
	if s.exists("edge", filepath.Join(worktree, "missing-parent", "leaf")) {
		t.Fatal("sandbox exists missing parent unexpectedly returned true")
	}
	if _, err := s.listDir("edge", filepath.Join(worktree, "missing-dir"), 1); err == nil {
		t.Fatal("sandbox listDir missing directory unexpectedly succeeded")
	}
	entries := []DirEntry{}
	if err := s.walkDirFd(-1, "", worktree, 1, &entries); err == nil {
		t.Fatal("walkDirFd invalid descriptor unexpectedly succeeded")
	}
	if _, _, err := s.openReadBaseFd("edge", filepath.Join(worktree, "missing-dir")); err == nil {
		t.Fatal("openReadBaseFd missing directory unexpectedly succeeded")
	}

	rootFD, err := openRootDir(filepath.Join(worktree, "rootfd"))
	if err != nil {
		t.Fatalf("open root fd: %v", err)
	}
	defer unix.Close(rootFD) //nolint:errcheck
	if err := ensureDirsBeneath(rootFD, "nested/"+securePathEdgeComponent(program)); err != nil {
		t.Fatalf("ensure in-root dirs: %v", err)
	}
	if err := ensureDirsBeneath(rootFD, "../outside"); !errors.Is(err, errEscapesRoot) {
		t.Fatalf("ensureDirsBeneath escape = %v, want errEscapesRoot", err)
	}
	if err := ensureDirsBeneath(-1, "child"); err == nil {
		t.Fatal("ensureDirsBeneath invalid fd unexpectedly succeeded")
	}
	if _, err := os.Stat(filepath.Join(worktree, "outside")); !os.IsNotExist(err) {
		t.Fatalf("ensureDirsBeneath created escaped path: %v", err)
	}

	// secureDirFS rejects invalid virtual paths and invalid root fds with ordinary
	// fs.PathError values rather than allowing a browse escape.
	dirFS := &secureDirFS{baseFd: -1, basePath: worktree, fs: s}
	if _, err := dirFS.Open("../escape"); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("secureDirFS invalid Open = %v", err)
	}
	if _, err := dirFS.Open("."); err == nil {
		t.Fatal("secureDirFS valid-path Open with invalid fd unexpectedly succeeded")
	}
	if _, err := dirFS.ReadDir("."); err == nil {
		t.Fatal("secureDirFS invalid-fd ReadDir unexpectedly succeeded")
	}
	if _, err := dirFS.Stat("."); err == nil {
		t.Fatal("secureDirFS invalid-fd Stat unexpectedly succeeded")
	}

	// Directory removal is deliberately best-effort: a real empty/non-empty
	// directory is removed, and a missing parent is equivalent to already gone.
	removeTarget := filepath.Join(worktree, "remove-dir")
	if err := os.Remove(filepath.Join(removeTarget, "child.txt")); err != nil {
		t.Fatalf("prepare empty removable directory: %v", err)
	}
	if err := s.remove("remove", removeTarget); err != nil {
		t.Fatalf("sandbox remove directory: %v", err)
	}
	if _, err := os.Stat(removeTarget); !os.IsNotExist(err) {
		t.Fatalf("sandbox remove left directory: %v", err)
	}
	if err := s.remove("remove", filepath.Join(worktree, "missing-parent", "leaf")); err != nil {
		t.Fatalf("sandbox remove missing parent = %v, want no-op", err)
	}
	trace.Removed = append(trace.Removed, "remove-dir", "missing-parent")

	// Browsing preserves the same denied-base/error contracts as direct reads.
	// A masked file, hidden file, and binary file are all absent from native grep,
	// while a visible text hit remains discoverable.
	if _, err := s.glob("glob", filepath.Join(worktree, "missing-glob-base"), "*"); err == nil {
		t.Fatal("sandbox glob missing base unexpectedly succeeded")
	}
	if _, err := s.glob("glob", worktree, "["); err == nil {
		t.Fatal("sandbox glob malformed pattern unexpectedly succeeded")
	}
	if _, err := s.grepNative("[", worktree, "", false, 10, ""); err == nil {
		t.Fatal("sandbox grep invalid regex unexpectedly succeeded")
	}
	if _, err := s.grepNative("needle", filepath.Join(worktree, "missing-grep-base"), "", false, 10, ""); err == nil {
		t.Fatal("sandbox grep missing base unexpectedly succeeded")
	}
	grep, err := s.grepNative("needle", worktree, "", false, 10, "")
	if err != nil || grep != "visible.txt:1:needle visible" {
		t.Fatalf("sandbox grep filtered entries = %q, %v", grep, err)
	}

	// atomicWriteAt must both write under the passed directory fd and clean its
	// temporary file if the final rename cannot reach the requested leaf.
	atomicFD, err := openRootDir(worktree)
	if err != nil {
		t.Fatalf("open atomic root fd: %v", err)
	}
	if err := atomicWriteAt(atomicFD, "atomic-ok.txt", []byte("atomic"), 0o600); err != nil {
		_ = unix.Close(atomicFD)
		t.Fatalf("atomicWriteAt success: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(worktree, "atomic-ok.txt")); err != nil || string(got) != "atomic" {
		_ = unix.Close(atomicFD)
		t.Fatalf("atomicWriteAt output = %q, %v", got, err)
	}
	if err := atomicWriteAt(atomicFD, "missing-parent/leaf", []byte("never"), 0o600); err == nil {
		_ = unix.Close(atomicFD)
		t.Fatal("atomicWriteAt bad rename target unexpectedly succeeded")
	}
	if err := unix.Close(atomicFD); err != nil {
		t.Fatalf("close atomic root fd: %v", err)
	}
	atomicEntries, err := os.ReadDir(worktree)
	if err != nil {
		t.Fatalf("read atomic fixture: %v", err)
	}
	for _, ent := range atomicEntries {
		if strings.HasPrefix(ent.Name(), ".serf-sbtmp-") {
			t.Fatalf("atomicWriteAt leaked temporary entry %q", ent.Name())
		}
	}

	// A symlinked ancestor spelling may be tolerated, but a component inside the
	// root remains literal for the fd walk. The returned relation demonstrates the
	// former without weakening the latter.
	realParent := filepath.Join(base, "real-parent")
	aliasParent := filepath.Join(base, "alias-parent")
	realRoot := filepath.Join(realParent, "project")
	if err := os.MkdirAll(realRoot, 0o755); err != nil {
		t.Fatalf("make real ancestor fixture: %v", err)
	}
	if err := os.Symlink(realParent, aliasParent); err != nil {
		t.Fatalf("make ancestor alias: %v", err)
	}
	aliasRoot := filepath.Join(aliasParent, "project")
	if root, rel, ok := containingRoot([]string{aliasRoot}, filepath.Join(realRoot, "file.txt")); !ok || root != aliasRoot || rel != "file.txt" {
		t.Fatalf("ancestor-spelling containment = (%q,%q,%t), want (%q,file.txt,true)", root, rel, ok, aliasRoot)
	}
	if rel, ok := relUnderRealAncestor(aliasRoot, realRoot); !ok || rel != "." {
		t.Fatalf("exact ancestor alias relation = (%q,%t), want (.,true)", rel, ok)
	}
	if !pathUnder(filepath.Join(worktree, "inside"), worktree) || pathUnder(filepath.Join(base, "outside"), worktree) || !pathUnder(worktree, worktree) {
		t.Fatal("pathUnder containment contract failed")
	}
	if dir, leaf := splitLeaf("nested/file.txt"); dir != "nested" || leaf != "file.txt" {
		t.Fatalf("splitLeaf = (%q,%q)", dir, leaf)
	}
	if dir, leaf := splitLeaf("."); dir != "" || leaf != "" || dirOrDot("") != "." || len(relComponents(".")) != 0 {
		t.Fatal("relative-path primitive contract failed")
	}
	if _, _, ok := containingRoot([]string{"relative/root"}, filepath.Join(base, "absolute")); ok {
		t.Fatal("mixed relative/absolute roots unexpectedly matched")
	}
	if pathUnder(filepath.Join(base, "absolute"), "relative/root") {
		t.Fatal("mixed relative/absolute pathUnder unexpectedly matched")
	}
	if _, ok := relUnderRealAncestor(filepath.Join(base, "missing-real-root"), worktree); ok {
		t.Fatal("missing real ancestor unexpectedly matched")
	}
	trace.Contained = append(trace.Contained, "ancestor:file.txt", "root:true")

	// The sort fallback must be deterministic even when stat fails for every path.
	missing := []string{filepath.Join(base, "missing-b"), filepath.Join(base, "missing-a")}
	sortPathsByMtimeDesc(missing)
	if !sort.StringsAreSorted(missing) {
		t.Fatalf("missing-path sort = %v, want lexical order", missing)
	}
	return trace
}

func securePathEdgeComponent(program []byte) string {
	if len(program) == 0 {
		return "empty"
	}
	return "part-" + strings.NewReplacer("/", "_", "\\", "_", "\x00", "_").Replace(string(program[:1]))
}

func securePathEdgeDenied(err error, want sandbox.DenialReason) bool {
	var denied *sandbox.DeniedError
	return errors.As(err, &denied) && denied.ReasonKind == want
}
