//go:build linux || darwin

package execenv

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/sandbox"
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

// A grant never follows a symlinked final component, so such a denial offers
// nothing an approval could complete: it stays final.
func TestDenial_SymlinkedLeafOutsideRootsStaysFinal(t *testing.T) {
	t.Parallel()
	outDir := realTempDir(t)
	realFile := filepath.Join(outDir, "real.txt")
	if err := os.WriteFile(realFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	leaf := filepath.Join(outDir, "leaf.txt")
	if err := os.Symlink(realFile, leaf); err != nil {
		t.Fatal(err)
	}
	for name, op := range map[string]func(*sandboxFS, string) error{"write": sbWrite, "read": sbRead} {
		t.Run(name, func(t *testing.T) {
			s, _, _ := newSB(t, sandbox.ModeRestricted)
			var d *sandbox.DeniedError
			if err := op(s, leaf); !asDenied(err, &d) || d.ReasonKind.Curable() {
				t.Fatalf("symlinked leaf outside roots = %v, want a final denial", err)
			}
		})
	}
}

// A symlinked parent that leads into a masked location is a masked denial,
// final and redacted, never an approval card naming the secret location.
func TestDenial_SymlinkIntoMaskedLocationStaysFinal(t *testing.T) {
	t.Parallel()
	outDir := realTempDir(t)
	worktree := filepath.Join(outDir, "wt")
	masked := filepath.Join(outDir, "creds")
	for _, dir := range []string{worktree, masked} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(outDir, "link")
	if err := os.Symlink(masked, link); err != nil {
		t.Fatal(err)
	}
	rp := &sandbox.ResolvedPolicy{
		Mode:        sandbox.ModeRestricted,
		FileTool:    sandbox.AccessScope{Read: sandbox.ReadWorktreeOnly, ReadRoots: []string{worktree}, WriteRoots: []string{worktree}},
		MaskedPaths: []string{masked},
	}
	s := newSandboxFS(rp, "")
	t.Cleanup(s.close)
	var d *sandbox.DeniedError
	if err := s.writeFile("write_file", filepath.Join(link, "key"), []byte("x"), 0o600); !asDenied(err, &d) || d.ReasonKind != sandbox.DenialMasked || !d.Sensitive {
		t.Fatalf("write through a symlink into a masked location = %v, want a sensitive masked denial", err)
	}
}

func sbWrite(s *sandboxFS, abs string) error {
	return s.writeFile("write_file", abs, []byte("x"), 0o644)
}

func sbRead(s *sandboxFS, abs string) error {
	_, err := s.readFile("read_file", abs)
	return err
}
