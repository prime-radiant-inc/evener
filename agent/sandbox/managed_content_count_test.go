package sandbox

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestSpawnPolicyWalkCount counts the roots actually passed to the .git walk by
// both backends, twice each, for a clean and an unverified managed store. The
// clean store must skip its managed root while still walking user skills; the
// unverified store must walk both.
func TestSpawnPolicyWalkCount(t *testing.T) {
	home := t.TempDir()
	cache := filepath.Join(home, ".config", "evener", "plugins", "cache")
	skills := filepath.Join(home, ".config", "evener", "skills")
	for _, dir := range []string{
		filepath.Join(cache, "mkt", "plugin", "abc", ".git"),
		filepath.Join(skills, "mine", ".git"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	original := gitReadDirChunk
	var mu sync.Mutex
	var walked []string
	gitReadDirChunk = func(f *os.File, n int) ([]os.DirEntry, error) {
		mu.Lock()
		walked = append(walked, f.Name())
		mu.Unlock()
		return original(f, n)
	}
	t.Cleanup(func() { gitReadDirChunk = original })

	// Compare identity, not raw spelling: a resolved root has its own name.
	normalize := func(p string) string {
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			return resolved
		}
		return filepath.Clean(p)
	}
	count := func(target string) int {
		mu.Lock()
		defer mu.Unlock()
		want := normalize(target)
		n := 0
		for _, root := range walked {
			if normalize(root) == want {
				n++
			}
		}
		return n
	}

	cwd := MaterializeWorkspace(t, MainCheckout)
	for _, clean := range []bool{true, false} {
		mu.Lock()
		walked = nil
		mu.Unlock()

		facts := bwrapFacts(home)
		facts.EvenerContentRoots = []string{cache, skills}
		facts.EvenerManagedContentRoots = []string{cache}
		facts.EvenerManagedContentClean = clean
		rp, err := Resolve(SandboxPolicy{Mode: ModeRestricted, Network: new(true)}, facts, cwd)
		if err != nil {
			t.Fatal(err)
		}
		sessionTmp := t.TempDir()
		for range 2 {
			buildBwrapArgv(rp, sessionTmp, cwd)
		}

		host := seatbeltHost()
		host.Home = home
		host.EvenerContentRoots = []string{cache, skills}
		host.EvenerManagedContentRoots = []string{cache}
		host.EvenerManagedContentClean = clean
		rp2, err := Resolve(SandboxPolicy{Mode: ModeRestricted, Network: new(true)}, host, cwd)
		if err != nil {
			t.Fatal(err)
		}
		for range 2 {
			SeatbeltPolicy(rp2, "/evener-session-tmp", identityCanon)
		}

		cacheCount, skillsCount := count(cache), count(skills)
		if clean {
			if cacheCount != 0 {
				t.Errorf("clean managed root was walked %d times, want 0", cacheCount)
			}
			if skillsCount < 2 {
				t.Errorf("user skills root was walked %d times, want at least 2", skillsCount)
			}
		} else {
			if cacheCount < 2 {
				t.Errorf("unverified managed root was walked %d times, want at least 2", cacheCount)
			}
			if skillsCount < 2 {
				t.Errorf("user skills root was walked %d times, want at least 2", skillsCount)
			}
		}
		t.Logf("clean=%v: managed root walked %d times, skills root walked %d times", clean, cacheCount, skillsCount)
	}
}
