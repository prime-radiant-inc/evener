package execenv

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/sandbox"
)

// asDenied reports whether err is (or wraps) a *sandbox.DeniedError, binding it
// into target for further assertions.
func asDenied(err error, target **sandbox.DeniedError) bool {
	return errors.As(err, target)
}

// sbTestHost returns HostFacts for a bwrap-capable linux host anchored at the
// given home, so the resolver produces an enforceable policy in unit tests without
// touching the real host (the file tools never consult HostFacts — only the
// resolved policy — so this is only about producing a ResolvedPolicy to enforce).
func sbTestHost(home string) sandbox.HostFacts {
	return sandbox.HostFacts{
		OS: "linux", Home: home,
		BwrapPath: "/usr/bin/bwrap", BwrapCapable: true, OverlaySupported: true,
	}
}

// resolvePolicy resolves a sandbox policy of the given mode over cwd with home,
// failing the test on refusal. It is the shared setup for the securepath suite.
func resolvePolicy(t *testing.T, mode sandbox.Mode, home, cwd string) *sandbox.ResolvedPolicy {
	t.Helper()
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: mode}, sbTestHost(home), cwd)
	if err != nil {
		t.Fatalf("Resolve(%v): unexpected refusal: %v", mode, err)
	}
	return &rp
}

// newSB builds a sandboxFS for mode over a fresh worktree beneath a fake home,
// returning the fs, the home dir, and the worktree root. The worktree is NOT a
// git repo (NonGit layout) so the whole temp tree is the writable/readable root
// without git-metadata carve-outs muddying the base cases.
func newSB(t *testing.T, mode sandbox.Mode) (*sandboxFS, string, string) {
	t.Helper()
	home := t.TempDir()
	worktree := filepath.Join(home, "project")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	rp := resolvePolicy(t, mode, home, worktree)
	s := newSandboxFS(rp)
	t.Cleanup(s.close)
	return s, home, worktree
}

func TestConfinedResolvesUnderRoot(t *testing.T) {
	t.Parallel()
	s, _, worktree := newSB(t, sandbox.ModeRestricted)

	want := []byte("hello beneath the root\n")
	target := filepath.Join(worktree, "sub", "dir", "file.txt")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, want, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := s.readFile("read_file", target)
	if err != nil {
		t.Fatalf("readFile beneath root: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("readFile = %q, want %q", got, want)
	}
}

func TestOpenBeneathRefusesSymlinkComponent(t *testing.T) {
	t.Parallel()
	s, home, worktree := newSB(t, sandbox.ModeRestricted)

	// A secret outside the worktree, reachable only by following a planted symlink.
	outside := filepath.Join(home, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("TOP SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	// worktree/link -> home/outside (a directory symlink escaping the root).
	link := filepath.Join(worktree, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	_, err := s.readFile("read_file", filepath.Join(link, "secret.txt"))
	if err == nil {
		t.Fatal("reading through a symlink component must be refused, not followed")
	}
	var denied *sandbox.DeniedError
	if !asDenied(err, &denied) {
		t.Fatalf("symlink refusal must be a *sandbox.DeniedError, got %T: %v", err, err)
	}
}

func TestOpenBeneathRefusesDotDotEscape(t *testing.T) {
	t.Parallel()
	s, home, worktree := newSB(t, sandbox.ModeRestricted)

	secret := filepath.Join(home, "outside-secret.txt")
	if err := os.WriteFile(secret, []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A ../ path that textually escapes the worktree root.
	escape := filepath.Join(worktree, "..", "outside-secret.txt")
	_, err := s.readFile("read_file", escape)
	if err == nil {
		t.Fatal("a ../ escape out of the root must be refused")
	}
	var denied *sandbox.DeniedError
	if !asDenied(err, &denied) {
		t.Fatalf("dotdot escape must be a *sandbox.DeniedError, got %T: %v", err, err)
	}
}

func TestDenylistRefusesPseudoFS(t *testing.T) {
	t.Parallel()
	// read-only mode has an anywhere-minus-denylist read shape, which is where the
	// pseudo-fs / secrets denylist is load-bearing.
	s, home, _ := newSB(t, sandbox.ModeReadOnly)

	denied := []string{
		"/proc/1/environ",
		"/proc/self/environ",
		"/sys/kernel",
		"/dev/fd/0",
		"/dev/mem",
		"/run/user/1000/bus",
		filepath.Join(home, ".ssh", "id_rsa"),
		filepath.Join(home, ".aws", "credentials"),
	}
	for _, p := range denied {
		_, err := s.readFile("read_file", p)
		if err == nil {
			t.Errorf("read of denylisted path %q must be refused", p)
			continue
		}
		var de *sandbox.DeniedError
		if !asDenied(err, &de) {
			t.Errorf("denylist refusal for %q must be *sandbox.DeniedError, got %T: %v", p, err, err)
		}
	}
}

func TestDenylistAllowsNonDenied(t *testing.T) {
	t.Parallel()
	// read-only reads anywhere except the denylist: a plain file outside the
	// worktree but not under any masked path is readable.
	s, home, _ := newSB(t, sandbox.ModeReadOnly)
	elsewhere := filepath.Join(home, "notes.txt")
	if err := os.WriteFile(elsewhere, []byte("readable"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := s.readFile("read_file", elsewhere)
	if err != nil {
		t.Fatalf("read-only should allow a non-denylisted out-of-worktree read: %v", err)
	}
	if string(got) != "readable" {
		t.Errorf("got %q", got)
	}
}

func TestWriteFilePrimitiveConfinedAndAtomic(t *testing.T) {
	t.Parallel()
	s, home, worktree := newSB(t, sandbox.ModeWorkspaceWrite)

	// In-root write with a missing intermediate dir: created + written atomically.
	target := filepath.Join(worktree, "a", "b", "made.txt")
	if err := s.writeFile("write_file", target, []byte("payload"), 0o644); err != nil {
		t.Fatalf("in-root writeFile: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "payload" {
		t.Fatalf("writeFile result: got %q err %v", got, err)
	}
	// No stray temp file left beside it.
	ents, _ := os.ReadDir(filepath.Dir(target))
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".serf-sbtmp-") {
			t.Errorf("stray temp file left behind: %s", e.Name())
		}
	}

	// Out-of-root write is denied and creates nothing.
	outside := filepath.Join(home, "escape.txt")
	if err := s.writeFile("write_file", outside, []byte("x"), 0o644); err == nil {
		t.Fatal("out-of-root writeFile must be denied")
	}
	if _, err := os.Stat(outside); err == nil {
		t.Error("denied writeFile must not create the file")
	}
}

func TestEnsureDirsBeneathRefusesSymlinkComponent(t *testing.T) {
	t.Parallel()
	s, home, worktree := newSB(t, sandbox.ModeWorkspaceWrite)

	// worktree/link -> home (an escaping directory symlink). A write whose parent
	// path traverses it must be refused, and must not create anything under home.
	if err := os.Symlink(home, filepath.Join(worktree, "link")); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(worktree, "link", "planted.txt")
	err := s.writeFile("write_file", target, []byte("nope"), 0o644)
	if err == nil {
		t.Fatal("writeFile through a symlinked component must be refused")
	}
	var denied *sandbox.DeniedError
	if !asDenied(err, &denied) {
		t.Fatalf("symlinked-parent write refusal must be *sandbox.DeniedError, got %T: %v", err, err)
	}
	if _, statErr := os.Stat(filepath.Join(home, "planted.txt")); statErr == nil {
		t.Error("a refused write must not create a file outside the root via the symlink")
	}
}

func TestRemoveRenameMkdirConfined(t *testing.T) {
	t.Parallel()
	s, home, worktree := newSB(t, sandbox.ModeWorkspaceWrite)

	// mkdirAll in-root, then a write, then rename within root, then remove.
	if err := s.mkdirAll("apply_patch", filepath.Join(worktree, "d1")); err != nil {
		t.Fatalf("mkdirAll: %v", err)
	}
	src := filepath.Join(worktree, "d1", "f.txt")
	if err := s.writeFile("apply_patch", src, []byte("v"), 0o644); err != nil {
		t.Fatalf("writeFile: %v", err)
	}
	dst := filepath.Join(worktree, "d2", "g.txt")
	if err := s.rename("apply_patch", src, dst); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if _, err := os.Stat(src); err == nil {
		t.Error("rename left the source in place")
	}
	if got, _ := os.ReadFile(dst); string(got) != "v" {
		t.Errorf("rename destination content = %q", got)
	}
	if err := s.remove("apply_patch", dst); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(dst); err == nil {
		t.Error("remove did not delete the file")
	}

	// Out-of-root endpoints are denied.
	outside := filepath.Join(home, "x.txt")
	if err := os.WriteFile(outside, []byte("o"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.remove("apply_patch", outside); err == nil {
		t.Error("out-of-root remove must be denied")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Error("a denied remove must not delete the out-of-root file")
	}
	if err := s.rename("apply_patch", filepath.Join(worktree, "d1"), filepath.Join(home, "moved")); err == nil {
		t.Error("rename to an out-of-root destination must be denied")
	}
	if err := s.mkdirAll("apply_patch", filepath.Join(home, "newdir")); err == nil {
		t.Error("mkdirAll outside the writable root must be denied")
	}
}

func TestAuditSinkRedaction(t *testing.T) {
	// Mutates the package auditSink; not parallel.
	var records []AuditRecord
	orig := auditSink
	auditSink = func(r AuditRecord) { records = append(records, r) }
	t.Cleanup(func() { auditSink = orig })

	s, home, worktree := newSB(t, sandbox.ModeReadOnly)

	// A masked (secret) denial redacts the path to "<denied>" — never the basename.
	if _, err := s.readFile("read_file", filepath.Join(home, ".ssh", "id_rsa")); err == nil {
		t.Fatal("expected masked denial")
	}
	// An out-of-root write denial (read-only denies all writes) redacts to a
	// basename — informative but not a full path.
	if err := s.writeFile("write_file", filepath.Join(worktree, "note.txt"), []byte("x"), 0o644); err == nil {
		t.Fatal("expected write denial in read-only mode")
	}

	if len(records) < 2 {
		t.Fatalf("expected at least two audit records, got %d: %+v", len(records), records)
	}
	var sawMasked, sawWrite bool
	for _, r := range records {
		if r.Reason == denyReasonMasked {
			sawMasked = true
			if r.Path != "<denied>" {
				t.Errorf("masked denial must redact path to <denied>, got %q", r.Path)
			}
			if strings.Contains(r.Path, "id_rsa") {
				t.Errorf("masked denial leaked the secret basename: %q", r.Path)
			}
		}
		if r.Tool == "write_file" {
			sawWrite = true
			if r.Path != "note.txt" {
				t.Errorf("write denial should redact to basename note.txt, got %q", r.Path)
			}
		}
		if r.Mode != "read-only" {
			t.Errorf("audit mode = %q, want read-only", r.Mode)
		}
	}
	if !sawMasked || !sawWrite {
		t.Errorf("missing expected records: masked=%v write=%v (%+v)", sawMasked, sawWrite, records)
	}
}

// TestDenyReasonAnnouncesImmutablePolicy: a NON-sensitive denial appends a clause
// telling the model the box is fixed for the session (so it stops retrying); a
// SENSITIVE (credential) denial stays terse — no clause, no path hint.
func TestDenyReasonAnnouncesImmutablePolicy(t *testing.T) {
	t.Parallel()
	s, home, worktree := newSB(t, sandbox.ModeReadOnly)

	err := s.writeFile("write_file", filepath.Join(worktree, "note.txt"), []byte("x"), 0o644)
	var de *sandbox.DeniedError
	if !asDenied(err, &de) {
		t.Fatalf("write in read-only must be a *sandbox.DeniedError, got %T: %v", err, err)
	}
	if de.Sensitive {
		t.Fatal("a write-denied error must not be Sensitive")
	}
	if !strings.Contains(de.Reason, "fixed for the session") {
		t.Errorf("non-sensitive denial must announce the fixed policy, got reason %q", de.Reason)
	}

	_, err = s.readFile("read_file", filepath.Join(home, ".ssh", "id_rsa"))
	var mde *sandbox.DeniedError
	if !asDenied(err, &mde) {
		t.Fatalf("masked read must be a *sandbox.DeniedError, got %T: %v", err, err)
	}
	if !mde.Sensitive {
		t.Fatal("a masked credential denial must be Sensitive")
	}
	if strings.Contains(mde.Reason, "fixed for the session") {
		t.Errorf("sensitive denial must stay terse, got reason %q", mde.Reason)
	}
}

func TestOffModeUnused(t *testing.T) {
	t.Parallel()
	// A nil policy and an explicit off policy both leave the environment on the
	// afero path — e.sandbox() returns nil so no sandboxFS is ever built.
	env := NewLocalExecutionEnvironment(t.TempDir())
	if env.sandbox() != nil {
		t.Fatal("a nil-policy env must not build a sandboxFS")
	}
	env.Sandbox = &sandbox.ResolvedPolicy{Mode: sandbox.ModeOff}
	if env.sandbox() != nil {
		t.Fatal("an off-mode policy must not build a sandboxFS (off = today's behavior)")
	}
}
