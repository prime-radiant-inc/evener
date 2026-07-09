//go:build linux

package execenv

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/agent/sandbox"
)

// The M7 per-invocation grant widens ONLY root-containment for EXACTLY one path,
// for one short-lived sandboxFS clone. It never overrides masking, git-protection,
// or symlink refusal, and it never widens a sibling. These are the securepath-level
// guarantees the escalation approve path rests on.

// TestDenialReasonKindMapping pins the reason-string → typed-kind mapping so a
// renamed reason (or a broken switch arm) fails HERE rather than silently mapping to
// DenialUnspecified — which would fail-closed but stop escalation firing for that
// class with no failing test. It also cross-checks that exactly the containment
// reasons are Curable(), tying the mapping to M7's escalation eligibility.
func TestDenialReasonKindMapping(t *testing.T) {
	t.Parallel()
	cases := []struct {
		reason  string
		want    sandbox.DenialReason
		curable bool
	}{
		{denyReasonOutsideRead, sandbox.DenialOutsideReadRoots, true},
		{denyReasonOutsideWrite, sandbox.DenialOutsideWriteRoots, true},
		{denyReasonWriteDenied, sandbox.DenialWritesDisabled, true},
		{denyReasonMasked, sandbox.DenialMasked, false},
		{denyReasonProtected, sandbox.DenialGitProtected, false},
		{denyReasonSymlink, sandbox.DenialSymlink, false},
		{denyReasonEscape, sandbox.DenialEscape, false},
		{denyReasonRootTarget, sandbox.DenialRootTarget, false},
	}
	for _, tc := range cases {
		got := denialReasonKind(tc.reason)
		if got != tc.want {
			t.Errorf("denialReasonKind(%q) = %v, want %v", tc.reason, got, tc.want)
		}
		if got == sandbox.DenialUnspecified {
			t.Errorf("reason %q must map to a specific kind, not Unspecified", tc.reason)
		}
		if got.Curable() != tc.curable {
			t.Errorf("reason %q Curable() = %v, want %v", tc.reason, got.Curable(), tc.curable)
		}
	}
	// An unknown/renamed reason fails closed to Unspecified (not curable → final).
	if got := denialReasonKind("a reason nobody mapped"); got != sandbox.DenialUnspecified {
		t.Errorf("unknown reason must map to DenialUnspecified, got %v", got)
	}
}

func TestGrant_ReadsExactLeafOutsideReadRoots(t *testing.T) {
	t.Parallel()
	s, _, _ := newSB(t, sandbox.ModeRestricted)
	outside := filepath.Join(t.TempDir(), "granted.txt")
	want := []byte("granted read\n")
	if err := os.WriteFile(outside, want, 0o644); err != nil {
		t.Fatal(err)
	}

	// Without a grant, the out-of-root read is denied.
	var d *sandbox.DeniedError
	if _, err := s.readFile("read_file", outside); !asDenied(err, &d) {
		t.Fatalf("expected out-of-root denial without a grant, got %v", err)
	}

	// With the exact path granted, the read succeeds through the same fd-anchored path.
	s.grant = outside
	got, err := s.readFile("read_file", outside)
	if err != nil {
		t.Fatalf("granted read: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("read %q, want %q", got, want)
	}

	// A sibling out-of-root path (NOT granted) still denies — proves single-leaf,
	// not parent-directory, widening.
	sibling := filepath.Join(filepath.Dir(outside), "sibling.txt")
	if err := os.WriteFile(sibling, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.readFile("read_file", sibling); !asDenied(err, &d) {
		t.Fatalf("a non-granted sibling in the granted dir must still deny, got %v", err)
	}
}

func TestGrant_WritesExactLeafOutsideWriteRoots(t *testing.T) {
	t.Parallel()
	s, _, _ := newSB(t, sandbox.ModeReadOnly) // read-only ⇒ WriteRoots empty
	outDir := t.TempDir()
	target := filepath.Join(outDir, "out.txt")

	var d *sandbox.DeniedError
	if err := s.writeFile("write_file", target, []byte("x"), 0o644); !asDenied(err, &d) {
		t.Fatalf("expected write denial without a grant, got %v", err)
	}

	s.grant = target
	if err := s.writeFile("write_file", target, []byte("granted write"), 0o644); err != nil {
		t.Fatalf("granted write: %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "granted write" {
		t.Fatalf("on-disk content %q, want %q", got, "granted write")
	}

	// A different path in the same dir still denies.
	other := filepath.Join(outDir, "other.txt")
	if err := s.writeFile("write_file", other, []byte("x"), 0o644); !asDenied(err, &d) {
		t.Fatalf("a non-granted path must deny even in the granted dir, got %v", err)
	}
}

func TestGrant_RefusesSymlinkedLeaf(t *testing.T) {
	t.Parallel()
	s, _, _ := newSB(t, sandbox.ModeRestricted)
	outDir := t.TempDir()
	realFile := filepath.Join(outDir, "real.txt")
	if err := os.WriteFile(realFile, []byte("secret via symlink"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(outDir, "link.txt")
	if err := os.Symlink(realFile, link); err != nil {
		t.Fatal(err)
	}

	// Granting a symlinked leaf must NOT let the grant follow it: the race-safe
	// no-symlinks open still refuses. (Prevents a symlink-swap escape via a grant.)
	s.grant = link
	var d *sandbox.DeniedError
	if _, err := s.readFile("read_file", link); !asDenied(err, &d) {
		t.Fatalf("a granted symlink leaf must still be refused, got %v", err)
	}
}

func TestGrant_RefusesSymlinkedParent_Read(t *testing.T) {
	t.Parallel()
	s, _, _ := newSB(t, sandbox.ModeRestricted)
	outDir := t.TempDir()
	realDir := filepath.Join(outDir, "real")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(realDir, "secret")
	if err := os.WriteFile(secret, []byte("SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(outDir, "link") // link -> real
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatal(err)
	}

	// A grant whose PARENT is a symlink must NOT be resolved through it: the grant
	// widens containment only, never symlink resolution. Reading link/secret must be
	// refused, never redirected to real/secret. (This is the escape the adversarial
	// review caught: openRootDir would have followed the parent symlink.)
	grantPath := filepath.Join(link, "secret")
	s.grant = grantPath
	var d *sandbox.DeniedError
	if _, err := s.readFile("read_file", grantPath); !asDenied(err, &d) {
		t.Fatalf("a grant with a symlinked parent must be refused, got %v", err)
	}
}

func TestGrant_RefusesSymlinkedParent_Write(t *testing.T) {
	t.Parallel()
	s, _, _ := newSB(t, sandbox.ModeReadOnly)
	outDir := t.TempDir()
	realDir := filepath.Join(outDir, "real")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(outDir, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatal(err)
	}

	grantPath := filepath.Join(link, "planted") // link -> real
	s.grant = grantPath
	var d *sandbox.DeniedError
	if err := s.writeFile("write_file", grantPath, []byte("x"), 0o644); !asDenied(err, &d) {
		t.Fatalf("a granted write with a symlinked parent must be refused, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(realDir, "planted")); !os.IsNotExist(err) {
		t.Fatal("the write must not land in the symlink target directory")
	}
}

func TestGrant_DoesNotOverrideGitProtection(t *testing.T) {
	t.Parallel()
	outDir := t.TempDir()
	worktree := filepath.Join(outDir, "wt")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	protected := filepath.Join(worktree, "config")
	if err := os.WriteFile(protected, []byte("[core]"), 0o644); err != nil {
		t.Fatal(err)
	}
	rp := &sandbox.ResolvedPolicy{
		Mode:     sandbox.ModeWorkspaceWrite,
		FileTool: sandbox.AccessScope{Read: sandbox.ReadAnywhere, WriteRoots: []string{worktree}},
		Git:      sandbox.GitLayout{ProtectedPaths: []string{protected}},
	}
	s := newSandboxFS(rp)
	t.Cleanup(s.close)

	// Even granted, a git-protected surface stays write-denied — the grant widens
	// containment only, never the git-protection floor.
	s.grant = protected
	var d *sandbox.DeniedError
	if err := s.writeFile("write_file", protected, []byte("tampered"), 0o644); !asDenied(err, &d) {
		t.Fatalf("a granted git-protected write must still deny, got %v", err)
	}
}

func TestGrant_DoesNotOverrideMasking(t *testing.T) {
	t.Parallel()
	outDir := t.TempDir()
	worktree := filepath.Join(outDir, "wt")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	masked := filepath.Join(outDir, "creds")
	if err := os.WriteFile(masked, []byte("SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	rp := &sandbox.ResolvedPolicy{
		Mode:        sandbox.ModeRestricted,
		FileTool:    sandbox.AccessScope{Read: sandbox.ReadWorktreeOnly, ReadRoots: []string{worktree}},
		MaskedPaths: []string{masked},
	}
	s := newSandboxFS(rp)
	t.Cleanup(s.close)

	// Even granted, a masked (credential) path stays denied — the grant widens
	// containment only, never the secrets floor.
	s.grant = masked
	var d *sandbox.DeniedError
	if _, err := s.readFile("read_file", masked); !asDenied(err, &d) {
		t.Fatalf("a granted masked path must still deny, got %v", err)
	}
}
