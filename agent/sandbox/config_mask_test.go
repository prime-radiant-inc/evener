package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

// When XDG_CONFIG_HOME moves Evener's config root, the credential mask follows
// it, since hub configuration and credentials live wherever that root is
// (#4232).
func TestEvenerConfigRootIsMaskedWhereverItLives(t *testing.T) {
	root := mainRepo(t)
	host := bwrapHost()
	host.EvenerConfigRoot = "/xdg/evener"
	for _, mode := range []Mode{ModeReadOnly, ModeWorkspaceWrite, ModeRestricted} {
		rp := mustResolve(t, SandboxPolicy{Mode: mode, Network: new(true)}, host, root)
		if rp.FileToolCanRead("/xdg/evener/hub.toml") {
			t.Errorf("%v: the relocated config root must be masked: %v", mode, rp.MaskedPaths)
		}
	}
	// Removing ~/.config/evener from the denylist removes both.
	rp := mustResolve(t, SandboxPolicy{Mode: ModeReadOnly, Network: new(true), DenylistRemove: []string{".config/evener"}}, host, root)
	if !rp.FileToolCanRead("/xdg/evener/hub.toml") {
		t.Errorf("removing ~/.config/evener from the denylist must unmask the relocated root too: %v", rp.MaskedPaths)
	}
}

// The probe finds Evener's config root the way Evener does: under
// $XDG_CONFIG_HOME, else ~/.config, a relative XDG_CONFIG_HOME resolved against
// the working directory.
func TestProbeFindsEvenerConfigRoot(t *testing.T) {
	t.Parallel()
	rel, err := filepath.Abs(filepath.Join("rel", "evener"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"default", nil, "/Users/tester/.config/evener"},
		{"XDG_CONFIG_HOME", map[string]string{"XDG_CONFIG_HOME": "/xdg"}, "/xdg/evener"},
		{"relative XDG_CONFIG_HOME", map[string]string{"XDG_CONFIG_HOME": "rel"}, rel},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := probeHost(stubProbeSystem{env: tc.env}).EvenerConfigRoot; got != tc.want {
				t.Errorf("EvenerConfigRoot = %q, want %q", got, tc.want)
			}
		})
	}
}

// A masked directory reached through a symlink is masked at its real location
// too, so the file tools cannot read the same secrets by the target's spelling.
func TestMasksCoverASymlinkedMasksRealLocation(t *testing.T) {
	root := mainRepo(t)
	home := clean(t.TempDir())
	realConfig := filepath.Join(clean(t.TempDir()), "real-config")
	if err := os.MkdirAll(filepath.Join(realConfig, "evener"), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(clean(t.TempDir()), "config-link")
	if err := os.Symlink(realConfig, link); err != nil {
		t.Fatal(err)
	}
	host := bwrapHost()
	host.Home = home
	host.EvenerConfigRoot = filepath.Join(link, "evener")
	rp := mustResolve(t, SandboxPolicy{Mode: ModeReadOnly, Network: new(true)}, host, root)
	if rp.FileToolCanRead(filepath.Join(realConfig, "evener", "credentials.toml")) {
		t.Errorf("the relocated config root must be masked at its real location: %v", rp.MaskedPaths)
	}
}

// Removing the relocated config root itself from the denylist unmasks it, like
// any other denylist removal.
func TestDenylistRemoveUnmasksTheRelocatedConfigRoot(t *testing.T) {
	root := mainRepo(t)
	host := bwrapHost()
	host.EvenerConfigRoot = "/xdg/evener"
	rp := mustResolve(t, SandboxPolicy{Mode: ModeReadOnly, Network: new(true), DenylistRemove: []string{"/xdg/evener"}}, host, root)
	if !rp.FileToolCanRead("/xdg/evener/hub.toml") {
		t.Errorf("removing /xdg/evener from the denylist must unmask it: %v", rp.MaskedPaths)
	}
}
