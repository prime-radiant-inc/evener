package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBwrapCleanManagedRootSkipsWalkButUserSkillsStillWalk(t *testing.T) {
	home := t.TempDir()
	cache := filepath.Join(home, ".config", "evener", "plugins", "cache")
	skills := filepath.Join(home, ".config", "evener", "skills")
	cacheGit := filepath.Join(cache, "mkt", "plugin", "abc", ".git")
	skillGit := filepath.Join(skills, "mine", ".git")
	for _, dir := range []string{cacheGit, skillGit} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	facts := bwrapFacts(home)
	facts.EvenerContentRoots = []string{cache, skills}
	facts.EvenerManagedContentRoots = []string{cache}
	facts.EvenerManagedContentClean = true
	cwd := MaterializeWorkspace(t, MainCheckout)
	rp, err := Resolve(SandboxPolicy{Mode: ModeRestricted, Network: new(true)}, facts, cwd)
	if err != nil {
		t.Fatal(err)
	}
	args := buildBwrapArgv(rp, t.TempDir(), cwd)
	if !hasSeq(args, "--ro-bind", cache, cache) {
		t.Errorf("clean managed root must stay granted: %v", args)
	}
	if hasSeq(args, "--tmpfs", cacheGit) {
		t.Errorf("clean managed root was still walked and masked: %v", args)
	}
	if !hasSeq(args, "--tmpfs", skillGit) {
		t.Errorf("user skills .git must still be masked: %v", args)
	}
}

func TestBwrapUnverifiedManagedRootKeepsWalk(t *testing.T) {
	home := t.TempDir()
	cache := filepath.Join(home, ".config", "evener", "plugins", "cache")
	skills := filepath.Join(home, ".config", "evener", "skills")
	cacheGit := filepath.Join(cache, "mkt", "plugin", "abc", ".git")
	skillGit := filepath.Join(skills, "mine", ".git")
	for _, dir := range []string{cacheGit, skillGit} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	facts := bwrapFacts(home)
	facts.EvenerContentRoots = []string{cache, skills}
	facts.EvenerManagedContentRoots = []string{cache}
	facts.EvenerManagedContentClean = false
	cwd := MaterializeWorkspace(t, MainCheckout)
	rp, err := Resolve(SandboxPolicy{Mode: ModeRestricted, Network: new(true)}, facts, cwd)
	if err != nil {
		t.Fatal(err)
	}
	args := buildBwrapArgv(rp, t.TempDir(), cwd)
	if !hasSeq(args, "--ro-bind", cache, cache) || !hasSeq(args, "--tmpfs", cacheGit) {
		t.Errorf("unverified managed root must keep its grant and walk: %v", args)
	}
	if !hasSeq(args, "--tmpfs", skillGit) {
		t.Errorf("user skills .git must still be masked: %v", args)
	}
}

func TestSeatbeltCleanManagedRootSkipsLinkedTargetWalk(t *testing.T) {
	host := seatbeltHost()
	host.Home = t.TempDir()
	config := filepath.Join(host.Home, ".config", "evener")
	cache := filepath.Join(config, "plugins", "cache")
	skills := filepath.Join(config, "skills")
	cacheTarget := filepath.Join(cache, "mkt", "plugin", "abc", "metadata")
	skillTarget := filepath.Join(skills, "mine", "metadata")
	for _, dir := range []string{cacheTarget, skillTarget} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("metadata", filepath.Join(cache, "mkt", "plugin", "abc", ".git")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("metadata", filepath.Join(skills, "mine", ".git")); err != nil {
		t.Fatal(err)
	}
	host.EvenerContentRoots = []string{cache, skills}
	host.EvenerManagedContentRoots = []string{cache}
	host.EvenerManagedContentClean = true
	cwd := MaterializeWorkspace(t, MainCheckout)
	rp, err := Resolve(SandboxPolicy{Mode: ModeRestricted, Network: new(true)}, host, cwd)
	if err != nil {
		t.Fatal(err)
	}
	text, params := SeatbeltPolicy(rp, "/evener-session-tmp", identityCanon)
	if !strings.Contains(text, `regex #"`+gitBelowRootRegex(cache)+`"`) {
		t.Errorf("Seatbelt must keep its path-free .git name rule for a clean managed root:\n%s", text)
	}
	for _, p := range params {
		if p.Path == cacheTarget {
			t.Errorf("clean managed root was still walked to %q", cacheTarget)
		}
	}
	foundSkill := false
	for _, p := range params {
		if p.Path == skillTarget {
			foundSkill = true
		}
	}
	if !foundSkill {
		t.Error("user skills linked .git target was not walked")
	}
}

type managedProbeSystem struct {
	home string
	env  map[string]string
}

func (s managedProbeSystem) goos() string                    { return "darwin" }
func (s managedProbeSystem) getenv(name string) string       { return s.env[name] }
func (s managedProbeSystem) environ() []string               { return nil }
func (s managedProbeSystem) userHomeDir() (string, error)    { return s.home, nil }
func (s managedProbeSystem) loginHomeDir() (string, error)   { return s.home, nil }
func (s managedProbeSystem) lookPath(string) (string, error) { return "", errors.New("not found") }
func (s managedProbeSystem) nonDirectoryFile(path string) bool {
	return path == "/usr/bin/sandbox-exec"
}
func (s managedProbeSystem) userConfigDir() (string, error) { return "", errors.New("no") }
func (s managedProbeSystem) readFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}
func (s managedProbeSystem) run(context.Context, string, ...string) error { return errors.New("no") }
func (s managedProbeSystem) combinedOutput(context.Context, string, ...string) ([]byte, error) {
	return nil, errors.New("no")
}
func (s managedProbeSystem) output(context.Context, string, ...string) ([]byte, error) {
	return nil, errors.New("no")
}

// A symlinked config root reaches cache and bundled by a different spelling
// than the probe records. The exemption must still match, and a user-skills
// root that resolves elsewhere must keep its walk.
func TestProbeResolveSeatbeltWithASymlinkedManagedRoot(t *testing.T) {
	home := t.TempDir()
	configBase := t.TempDir()
	realConfig := t.TempDir()
	if err := os.MkdirAll(configBase, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realConfig, filepath.Join(configBase, "evener")); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(realConfig, "plugins", "cache")
	skills := filepath.Join(realConfig, "skills")
	cacheTarget := filepath.Join(cache, "mkt", "plugin", "abc", "metadata")
	skillTarget := filepath.Join(skills, "mine", "metadata")
	for _, dir := range []string{cacheTarget, skillTarget} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("metadata", filepath.Join(cache, "mkt", "plugin", "abc", ".git")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("metadata", filepath.Join(skills, "mine", ".git")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realConfig, "plugins", "store-format.json"), []byte(`{"version":1,"managed_content":"clean"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	facts := probeHost(managedProbeSystem{home: home, env: map[string]string{"XDG_CONFIG_HOME": configBase}})
	if !facts.EvenerManagedContentClean {
		t.Fatal("probe did not read the clean store marker through the symlinked config root")
	}
	rp, err := Resolve(SandboxPolicy{Mode: ModeRestricted, Network: new(true)}, facts, MaterializeWorkspace(t, MainCheckout))
	if err != nil {
		t.Fatal(err)
	}
	resolve := func(p string) string {
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			return resolved
		}
		return p
	}
	lexicalCache := filepath.Join(configBase, "evener", "plugins", "cache")
	if !rp.ManagedWalkExempt(lexicalCache) || !rp.ManagedWalkExempt(resolve(lexicalCache)) {
		t.Fatalf("managed root exemption missed lexical or resolved spelling: %+v", rp.ManagedContentRoots)
	}
	if rp.ManagedWalkExempt(resolve(filepath.Join(configBase, "evener", "skills"))) {
		t.Fatal("user-skills root was exempted")
	}
	text, params := SeatbeltPolicy(rp, "/evener-session-tmp", resolve)
	for _, p := range params {
		if p.Path == resolve(cacheTarget) {
			t.Errorf("symlinked managed root was still walked: %s", p.Path)
		}
	}
	foundSkill := false
	for _, p := range params {
		if p.Path == resolve(skillTarget) {
			foundSkill = true
		}
	}
	if !foundSkill {
		t.Errorf("user-skills linked .git target was not walked:\n%s", text)
	}
}

func TestMissingManagedRootUsesProspectiveResolvedSpelling(t *testing.T) {
	home := t.TempDir()
	realConfig := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realConfig, filepath.Join(home, ".config", "evener")); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(home, ".config", "evener", "plugins", "cache")
	facts := bwrapFacts(home)
	facts.EvenerContentRoots = []string{cache}
	facts.EvenerManagedContentRoots = []string{cache}
	facts.EvenerManagedContentClean = true
	rp, err := Resolve(SandboxPolicy{Mode: ModeRestricted, Network: new(true)}, facts, MaterializeWorkspace(t, MainCheckout))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	resolved := filepath.Join(realConfig, "plugins", "cache")
	if !rp.ManagedWalkExempt(resolved) {
		t.Fatalf("a managed root created after resolution was not exempt at %q", resolved)
	}
}
