package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

// A backend can receive either APFS spelling of an admitted managed root.
// Comparing spelling instead of identity must not restart its metadata walk.
func TestManagedWalkExemptDataVolumeAliases(t *testing.T) {
	t.Parallel()
	const plain = "/Users/fixture/.config/evener/plugins/cache"
	const alias = "/System/Volumes/Data/Users/fixture/.config/evener/plugins/cache"
	for _, root := range []string{plain, alias} {
		for _, clean := range []bool{false, true} {
			policy := ResolvedPolicy{ManagedContentClean: clean, ManagedContentRoots: []string{root}}
			for _, path := range []string{plain, alias} {
				if got := policy.ManagedWalkExempt(path); got != clean {
					t.Errorf("root %q, path %q, clean %v: exemption = %v", root, path, clean, got)
				}
			}
			for _, path := range []string{
				"/System/Volumes/Data/Users/fixture/.config/evener/skills",
				alias + "/plugin",
				alias + "-other",
			} {
				if policy.ManagedWalkExempt(path) {
					t.Errorf("non-managed root %q was exempted", path)
				}
			}
		}
	}
}

func TestSeatbeltManagedAliasSkipsLinkedTargetWalk(t *testing.T) {
	t.Parallel()
	host := seatbeltHost()
	host.Home = t.TempDir()
	cache := filepath.Join(host.Home, ".config", "evener", "plugins", "cache")
	target := filepath.Join(cache, "market", "plugin", "revision", "metadata")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("metadata", filepath.Join(filepath.Dir(target), ".git")); err != nil {
		t.Fatal(err)
	}
	host.EvenerContentRoots = []string{cache}
	// The backend opens the plain spelling while the probe records its alias.
	host.EvenerManagedContentRoots = []string{"/System/Volumes/Data" + cache}
	cwd := MaterializeWorkspace(t, MainCheckout)
	for _, clean := range []bool{false, true} {
		host.EvenerManagedContentClean = clean
		policy, err := Resolve(SandboxPolicy{Mode: ModeRestricted, Network: new(true)}, host, cwd)
		if err != nil {
			t.Fatal(err)
		}
		// Exercise the backend boundary with only the alternate managed-root
		// spelling. Resolve also supplies a plain spelling on this test host.
		policy.managedContentRootsResolved = nil
		policy.managedContentRootsOnDisk = nil
		for range 2 {
			_, params := SeatbeltPolicy(policy, "/evener-session-tmp", identityCanon)
			walked := false
			for _, param := range params {
				if param.Path == target {
					walked = true
				}
			}
			if walked == clean {
				t.Errorf("clean %v: backend traversed managed alias = %v", clean, walked)
			}
		}
	}
}
