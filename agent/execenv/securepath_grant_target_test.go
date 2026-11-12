//go:build linux || darwin

package execenv

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/internal/testwait"
)

// An approval for an out-of-roots path targets the resolved location: the
// containment denial names where a grant would land (every symlink in the
// parent resolved), and the grant writes or reads exactly there. These tests
// spell the outside path through an un-canonicalized temp dir on purpose, so on
// macOS they also cross the /var → /private/var symlink.

// symlinkedOutside builds, outside every root, a real directory and a symlink
// naming it, and returns the real directory's canonical path and the link.
func symlinkedOutside(t *testing.T) (realDir, link string) {
	t.Helper()
	base := t.TempDir()
	realDir = filepath.Join(base, "real")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link = filepath.Join(base, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatal(err)
	}
	return evalSym(t, realDir), link
}

func TestDenial_OutsideRootsNamesResolvedGrantTarget(t *testing.T) {
	t.Parallel()
	realDir, link := symlinkedOutside(t)
	if err := os.WriteFile(filepath.Join(realDir, "existing.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(realTempDir(t), "plain.txt")
	cases := []struct {
		name       string
		op         func(s *sandboxFS, abs string) error
		abs        string
		wantTarget string
	}{
		{"write through symlink", sbWrite, filepath.Join(link, "target.txt"), filepath.Join(realDir, "target.txt")},
		{"read through symlink", sbRead, filepath.Join(link, "existing.txt"), filepath.Join(realDir, "existing.txt")},
		{"write without symlink", sbWrite, plain, plain},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := newSB(t, sandbox.ModeRestricted)
			var d *sandbox.DeniedError
			if err := tc.op(s, tc.abs); !asDenied(err, &d) {
				t.Fatalf("expected an out-of-roots denial, got %v", err)
			}
			if !d.ReasonKind.Curable() || d.Path != tc.abs || d.GrantTarget != tc.wantTarget {
				t.Fatalf("denial = {kind %v path %q target %q}, want a curable denial of %q targeting %q", d.ReasonKind, d.Path, d.GrantTarget, tc.abs, tc.wantTarget)
			}
		})
	}
}

func TestGrant_OperatesAtApprovedResolvedTarget(t *testing.T) {
	t.Parallel()
	realDir, link := symlinkedOutside(t)

	s, _, _ := newSB(t, sandbox.ModeRestricted)
	requested := filepath.Join(link, "target.txt")
	s.grant = invocationGrant{path: requested, target: filepath.Join(realDir, "target.txt")}
	if err := s.writeFile("write_file", requested, []byte("approved"), 0o644); err != nil {
		t.Fatalf("granted write: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(realDir, "target.txt")); err != nil || string(got) != "approved" {
		t.Fatalf("resolved target = %q, %v; want the approved write", got, err)
	}
	got, err := s.readFile("read_file", requested)
	if err != nil || string(got) != "approved" {
		t.Fatalf("granted read = %q, %v; want the approved target's content", got, err)
	}
}

// A symlink retargeted after approval never redirects the grant: the requested
// spelling no longer resolves to the approved target, so the operation is
// refused, and neither the approved directory nor the new one is touched.
func TestGrant_RefusesWhenSymlinkRetargetedAfterApproval(t *testing.T) {
	t.Parallel()
	realDir, link := symlinkedOutside(t)
	other := filepath.Join(filepath.Dir(link), "other")
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "target.txt"), []byte("other"), 0o644); err != nil {
		t.Fatal(err)
	}
	requested := filepath.Join(link, "target.txt")
	approved := filepath.Join(realDir, "target.txt")
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, link); err != nil {
		t.Fatal(err)
	}

	s, _, _ := newSB(t, sandbox.ModeRestricted)
	s.grant = invocationGrant{path: requested, target: approved}
	var d *sandbox.DeniedError
	if err := s.writeFile("write_file", requested, []byte("redirected"), 0o644); !asDenied(err, &d) || d.ReasonKind != sandbox.DenialSymlink {
		t.Fatalf("write after retarget = %v, want a symlink denial", err)
	}
	if _, err := s.readFile("read_file", requested); !asDenied(err, &d) || d.ReasonKind != sandbox.DenialSymlink {
		t.Fatalf("read after retarget = %v, want a symlink denial", err)
	}
	if _, err := os.Lstat(approved); !os.IsNotExist(err) {
		t.Fatalf("the approved directory gained the file (err=%v)", err)
	}
	if got, _ := os.ReadFile(filepath.Join(other, "target.txt")); string(got) != "other" {
		t.Fatalf("the retargeted directory's file changed to %q", got)
	}
}

// A containment denial the grant could not complete at the resolved location
// carries no grant target, so no approval card is offered for it. The model
// sees the same plain containment denial either way: nothing it learns from the
// denial describes the filesystem outside the roots.
func TestDenial_UngrantableOutsideRootsHasNoGrantTarget(t *testing.T) {
	t.Parallel()
	outDir := realTempDir(t)
	worktree := filepath.Join(outDir, "wt")
	masked := filepath.Join(outDir, "creds")
	plain := filepath.Join(outDir, "plain")
	for _, dir := range []string{worktree, masked, plain} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	protected := filepath.Join(plain, "config")
	realFile := filepath.Join(plain, "real.txt")
	for _, file := range []string{protected, realFile, filepath.Join(masked, "key")} {
		if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	symlinks := map[string]string{
		filepath.Join(outDir, "toMasked"):  masked,
		filepath.Join(plain, "leaf.txt"):   realFile,
		filepath.Join(masked, "toPlain"):   plain,
		filepath.Join(outDir, "toPlain"):   plain,
		filepath.Join(masked, "maskedLnk"): realFile,
	}
	for link, target := range symlinks {
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	rp := &sandbox.ResolvedPolicy{
		Mode:        sandbox.ModeRestricted,
		FileTool:    sandbox.AccessScope{Read: sandbox.ReadWorktreeOnly, ReadRoots: []string{worktree}, WriteRoots: []string{worktree}},
		MaskedPaths: []string{masked},
		Git:         sandbox.GitLayout{ProtectedPaths: []string{protected}},
	}
	cases := []struct {
		name   string
		op     func(*sandboxFS, string) error
		abs    string
		secret string // what the model must not learn from the denial
	}{
		{"write through a symlinked leaf", sbWrite, filepath.Join(plain, "leaf.txt"), realFile},
		{"read through a symlinked leaf", sbRead, filepath.Join(plain, "leaf.txt"), realFile},
		{"write through a symlink into a masked location", sbWrite, filepath.Join(outDir, "toMasked", "key"), masked},
		{"read through a symlink into a masked location", sbRead, filepath.Join(outDir, "toMasked", "key"), masked},
		{"read of a masked spelling that resolves outside the mask", sbRead, filepath.Join(masked, "toPlain", "real.txt"), plain},
		{"read of a masked symlinked leaf", sbRead, filepath.Join(masked, "maskedLnk"), realFile},
		{"write through a symlink onto a git-protected file", sbWrite, filepath.Join(outDir, "toPlain", "config"), protected},
		{"write under a missing parent", sbWrite, filepath.Join(outDir, "toPlain", "missing", "f.txt"), plain},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newSandboxFS(rp, "")
			t.Cleanup(s.close)
			var d *sandbox.DeniedError
			if err := tc.op(s, tc.abs); !asDenied(err, &d) {
				t.Fatalf("expected a denial, got %v", err)
			}
			if d.GrantTarget != "" {
				t.Fatalf("an ungrantable denial named grant target %q", d.GrantTarget)
			}
			if !d.ReasonKind.Curable() || d.Sensitive {
				t.Fatalf("the model must see the plain containment denial, got kind %v sensitive %v", d.ReasonKind, d.Sensitive)
			}
			if strings.Contains(d.Error(), tc.secret) {
				t.Fatalf("the denial disclosed %q to the model", tc.secret)
			}
		})
	}
}

func sbWrite(s *sandboxFS, abs string) error {
	return s.writeFile("write_file", abs, []byte("x"), 0o644)
}

func sbRead(s *sandboxFS, abs string) error {
	_, err := s.readFile("read_file", abs)
	return err
}

// TestDenial_ApprovalCardMatchesFdCanonicalRefusal covers #6115: a spelling
// that passes the textual masked/protected checks but whose kernel canonical
// path names a protected or masked surface must not offer an approval card,
// because the grant refuses it after Allow. The alternate-spelling volumes
// (case-insensitive APFS, macOS's /System/Volumes/Data firmlink) are not
// reproducible on every test host, so the canonical seam itself is varied.
func TestDenial_ApprovalCardMatchesFdCanonicalRefusal(t *testing.T) {
	outDir := realTempDir(t)
	worktree := filepath.Join(outDir, "wt")
	plain := filepath.Join(outDir, "plain")
	repoGit := filepath.Join(outDir, "repo", ".git")
	masked := filepath.Join(outDir, "creds")
	for _, dir := range []string{worktree, plain, repoGit, masked} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	protected := filepath.Join(repoGit, "config")
	ordinary := filepath.Join(plain, "ordinary.txt")
	plainKey := filepath.Join(plain, "key")
	for _, file := range []string{protected, ordinary, plainKey} {
		if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	rp := &sandbox.ResolvedPolicy{
		Mode:        sandbox.ModeRestricted,
		FileTool:    sandbox.AccessScope{Read: sandbox.ReadWorktreeOnly, ReadRoots: []string{worktree}, WriteRoots: []string{worktree}},
		MaskedPaths: []string{masked},
		Git:         sandbox.GitLayout{ProtectedPaths: []string{protected}},
	}
	orig := canonicalPathForFd
	t.Cleanup(func() { canonicalPathForFd = orig })

	cases := []struct {
		name     string
		write    bool
		abs      string
		canonDir string
		wantCard bool
	}{
		{"write to a git-protected alias", true, filepath.Join(plain, "config"), repoGit, false},
		{"write to a plain file", true, filepath.Join(plain, "new.txt"), plain, true},
		{"read of a masked alias", false, plainKey, masked, false},
		{"read of a plain file", false, ordinary, plain, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			canonicalPathForFd = func(int) (string, error) { return tc.canonDir, nil }
			s := newSandboxFS(rp, "")
			defer s.close()

			var err error
			if tc.write {
				err = sbWrite(s, tc.abs)
			} else {
				err = sbRead(s, tc.abs)
			}
			var d *sandbox.DeniedError
			if !asDenied(err, &d) {
				t.Fatalf("expected an out-of-roots denial, got %v", err)
			}
			gotCard := d.GrantTarget != ""
			if gotCard != tc.wantCard {
				t.Fatalf("approval card offered = %v (target %q), want %v", gotCard, d.GrantTarget, tc.wantCard)
			}
		})
	}
}

// TestDenial_OutsideRootFIFONotOpenedForApproval covers #6226: building a
// restricted-mode read denial must not open an outside-root FIFO. A nonblocking
// reader open connects the FIFO and releases a producer blocked in open(2),
// losing its output; the Lstat preflight refuses the card before that open.
func TestDenial_OutsideRootFIFONotOpenedForApproval(t *testing.T) {
	outDir := realTempDir(t)
	worktree := filepath.Join(outDir, "wt")
	if err := os.Mkdir(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(outDir, "outside.fifo")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	rp := &sandbox.ResolvedPolicy{
		Mode:     sandbox.ModeRestricted,
		FileTool: sandbox.AccessScope{Read: sandbox.ReadWorktreeOnly, ReadRoots: []string{worktree}, WriteRoots: []string{worktree}},
	}
	s := newSandboxFS(rp, "")
	t.Cleanup(s.close)

	// A producer blocked in open(2) until a reader connects, then writing.
	done := make(chan struct{})
	go blockOnFIFOOpen(fifo, done)
	testwait.Until(t, "the producer to block in open(2)", producerBlockedInOpen)
	t.Cleanup(func() {
		// Release and reap the producer even when an assertion fails early.
		if r, err := os.OpenFile(fifo, os.O_RDONLY|unix.O_NONBLOCK, 0); err == nil {
			_ = r.Close()
		}
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	})

	var d *sandbox.DeniedError
	if err := sbRead(s, fifo); !asDenied(err, &d) {
		t.Fatalf("expected an out-of-roots denial, got %v", err)
	}
	if d.GrantTarget != "" {
		t.Fatalf("the denial offered a grant for a FIFO: %q", d.GrantTarget)
	}
	select {
	case <-done:
		t.Fatalf("constructing the denial opened the FIFO and released the waiting producer")
	case <-time.After(300 * time.Millisecond):
	}

	reader, err := os.OpenFile(fifo, os.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		t.Fatalf("open the FIFO to release the producer: %v", err)
	}
	defer reader.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("the producer did not finish after a reader opened the FIFO")
	}
}

// blockOnFIFOOpen blocks in open(2) until a reader connects, then writes one
// line. Its name is what producerBlockedInOpen looks for.
func blockOnFIFOOpen(fifo string, done chan<- struct{}) {
	defer close(done)
	f, err := os.OpenFile(fifo, os.O_WRONLY, 0)
	if err != nil {
		return
	}
	_, _ = f.Write([]byte("one line\n"))
	_ = f.Close()
}

// producerBlockedInOpen reports whether blockOnFIFOOpen is currently in a
// syscall. For a reader-less FIFO, open(2) blocks there until a reader
// connects, which is the state the denial must not disturb.
func producerBlockedInOpen() bool {
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	for block := range strings.SplitSeq(string(buf[:n]), "\n\n") {
		if strings.Contains(block, "blockOnFIFOOpen") && strings.Contains(block, "[syscall") {
			return true
		}
	}
	return false
}
