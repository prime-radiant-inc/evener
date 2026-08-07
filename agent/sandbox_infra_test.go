package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/sandbox"
	"primeradiant.com/serf/envvars"
)

// hermeticInfraEnv points the global MCP config layer at an empty temp dir so
// SessionInfraRoots never picks up the developer's real ~/.config/serf/mcp.json.
func hermeticInfraEnv(t *testing.T) {
	t.Helper()
	t.Setenv(envvars.XDGConfigHome.Name, t.TempDir())
}

func writeInfraFile(t *testing.T, path, content string, mode os.FileMode) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

// canonInfra is the symlink-resolved spelling of a path. SessionInfraRoots returns
// canonical roots (that is what the kernel matches on, and macOS TempDir lives
// under the /var -> /private/var symlink), so expectations must be canonical too.
func canonInfra(t *testing.T, p string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", p, err)
	}
	return filepath.Clean(resolved)
}

// TestSessionInfraRootsComeFromTheSessionConfig is the anti-glob test: the
// hook/MCP read/exec surface is built from the plugin dirs and MCP servers THIS
// session is configured with, so a plugin cache the session does not load
// contributes nothing, and a plugin dir anywhere on disk contributes even though
// it is nowhere near ~/.claude/plugins.
func TestSessionInfraRootsComeFromTheSessionConfig(t *testing.T) {
	hermeticInfraEnv(t)
	root := t.TempDir()
	env := execenv.NewLocalExecutionEnvironment(root)

	loaded := filepath.Join(root, "cache", "loaded-plugin")
	unloaded := filepath.Join(root, "cache", "unloaded-plugin")
	writeInfraFile(t, filepath.Join(loaded, "hooks", "session-start.sh"), "#!/bin/sh\n", 0o755)
	writeInfraFile(t, filepath.Join(unloaded, "hooks", "session-start.sh"), "#!/bin/sh\n", 0o755)

	server := writeInfraFile(t, filepath.Join(root, "servers", "mcp-server"), "#!/bin/sh\n", 0o755)
	script := writeInfraFile(t, filepath.Join(root, "scripts", "server.js"), "// mcp\n", 0o644)
	mcpJSON := writeInfraFile(t, filepath.Join(root, "mcp.json"), `{"mcpServers":{
		"direct": {"command": "`+server+`"},
		"scripted": {"command": "node", "args": ["`+script+`"]},
		"remote": {"type": "http", "url": "https://example.invalid/mcp"}
	}}`, 0o644)

	cfg := SessionConfig{
		Sandbox:        "restricted",
		PluginDirs:     []string{loaded},
		MCPConfigFiles: []string{mcpJSON},
	}
	got := SessionInfraRoots(cfg, env)

	for _, want := range []string{canonInfra(t, loaded), canonInfra(t, filepath.Dir(server)), canonInfra(t, filepath.Dir(script))} {
		if !slices.Contains(got, want) {
			t.Errorf("configured hook/MCP path %q missing from infra roots %v", want, got)
		}
	}
	if slices.Contains(got, canonInfra(t, unloaded)) {
		t.Errorf("a plugin dir this session does NOT load must not be granted: %v", got)
	}
}

// TestSessionInfraRootsSkipUnsafeRoots pins the two paths that are deliberately
// NOT granted: a home-directory-level root (which would hand restricted mode the
// whole home) and a configured path that does not exist.
func TestSessionInfraRootsSkipUnsafeRoots(t *testing.T) {
	hermeticInfraEnv(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no resolvable home directory on this host")
	}
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	cfg := SessionConfig{
		Sandbox:    "restricted",
		PluginDirs: []string{home, "/", filepath.Join(t.TempDir(), "does-not-exist")},
	}
	if got := SessionInfraRoots(cfg, env); len(got) != 0 {
		t.Errorf("home/root/missing paths must contribute no infra roots, got %v", got)
	}
}

// TestSessionInfraRootsAreFailSoft: an unreadable or malformed MCP config must
// not fail session start — the real MCP init reports it with proper diagnostics.
// The plugin dirs still contribute.
func TestSessionInfraRootsAreFailSoft(t *testing.T) {
	hermeticInfraEnv(t)
	root := t.TempDir()
	env := execenv.NewLocalExecutionEnvironment(root)
	pluginDir := filepath.Join(root, "plug")
	writeInfraFile(t, filepath.Join(pluginDir, "plugin.yaml"), "name: p\n", 0o644)
	broken := writeInfraFile(t, filepath.Join(root, "broken.json"), "{not json", 0o644)

	cfg := SessionConfig{Sandbox: "restricted", PluginDirs: []string{pluginDir}, MCPConfigFiles: []string{broken}}
	got := SessionInfraRoots(cfg, env)
	if !slices.Contains(got, canonInfra(t, pluginDir)) {
		t.Errorf("a broken MCP config must not suppress the plugin roots, got %v", got)
	}
}

// --- the grant's cap (WS4 Task 4 review, Criticals 1 and 2) -----------------

// TestSessionInfraRootsRefuseHomeAncestorsAndShallowRoots covers the family the
// original textual `== $HOME` guard missed. `/Users` and `/home` are ANCESTORS of
// a home directory, not equal to one, so they walked straight past it — and they
// survive Resolve's filterMasked too, which only drops roots at or BENEATH a
// masked path. A granted `/Users` hands every spawned process read/exec of every
// home on the machine, including every other worktree lane.
func TestSessionInfraRootsRefuseHomeAncestorsAndShallowRoots(t *testing.T) {
	hermeticInfraEnv(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no resolvable home directory on this host")
	}
	home = filepath.Clean(home)
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())

	unsafe := []string{
		"/",
		home,
		filepath.Dir(home), // /Users or /home — the ancestor case that got through
		filepath.Dir(filepath.Dir(home)),
	}
	for _, root := range []string{"/Users", "/home", "/private", "/var", "/Volumes", "/tmp"} {
		if _, err := os.Stat(root); err == nil {
			unsafe = append(unsafe, root)
		}
		// macOS gives every data-volume path a SECOND real spelling under
		// /System/Volumes/Data. Firmlinks are not symlinks, so EvalSymlinks does not
		// collapse them: the alias is deeper than the plain path and is not an
		// ancestor of the canonical home, and would walk past both guards.
		alias := filepath.Join("/System/Volumes/Data", root)
		if _, err := os.Stat(alias); err == nil {
			unsafe = append(unsafe, alias)
		}
	}
	for _, dir := range unsafe {
		cfg := SessionConfig{Sandbox: "restricted", PluginDirs: []string{dir}}
		if got := SessionInfraRoots(cfg, env); len(got) != 0 {
			t.Errorf("unsafe root %q must not be granted, got %v", dir, got)
		}
	}
}

// TestSessionInfraRootsRefuseSymlinkToHome: filepath.Abs does not resolve
// symlinks and os.Stat follows the link while leaving the textual path alone, so
// a link named in config passed the home check while granting home's contents.
// The guard must run on the SYMLINK-RESOLVED path.
func TestSessionInfraRootsRefuseSymlinkToHome(t *testing.T) {
	hermeticInfraEnv(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no resolvable home directory on this host")
	}
	dir := t.TempDir()
	link := filepath.Join(dir, "innocent-looking-plugin")
	if err := os.Symlink(filepath.Clean(home), link); err != nil {
		t.Skipf("cannot create symlink on this host: %v", err)
	}
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	cfg := SessionConfig{Sandbox: "restricted", PluginDirs: []string{link}}
	if got := SessionInfraRoots(cfg, env); len(got) != 0 {
		t.Errorf("a symlink to $HOME must not be granted, got %v", got)
	}
}

// TestSessionInfraRootsRefuseDirectoryValuedMCPArgs: the arg heuristic granted any
// absolute path that stat'd, and a DIRECTORY arg was granted verbatim rather than
// as some program's parent. `args: ["/Users"]` was therefore a direct grant. Only
// a REGULAR FILE may contribute, and it contributes its containing directory —
// which is all the stated rationale (an interpreter resolving a script's
// neighbours) ever justified.
func TestSessionInfraRootsRefuseDirectoryValuedMCPArgs(t *testing.T) {
	hermeticInfraEnv(t)
	root := t.TempDir()
	env := execenv.NewLocalExecutionEnvironment(root)
	victim := filepath.Join(root, "other-lane")
	if err := os.MkdirAll(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	mcpJSON := writeInfraFile(t, filepath.Join(root, "mcp.json"),
		`{"mcpServers":{"x":{"command":"/bin/sh","args":["`+victim+`"]}}}`, 0o644)

	cfg := SessionConfig{Sandbox: "restricted", MCPConfigFiles: []string{mcpJSON}}
	if got := SessionInfraRoots(cfg, env); slices.Contains(got, canonInfra(t, victim)) {
		t.Errorf("a directory-valued MCP arg must not be granted verbatim, got %v", got)
	}
}

// TestSessionInfraRootsIgnoreTheProjectMCPLayer is the root-cause test for the
// escalation. mcpconfig's layer 2 is <git root>/.serf/mcp.json — a file INSIDE the
// model's own write surface. Feeding it to the sandbox policy made the policy
// model-mutable, breaking SandboxPolicy's "nothing the model does mid-session can
// change it" invariant: the model writes .serf/mcp.json, spawns a delegate with
// sandbox="restricted" (which re-derives these roots live), and reads whatever it
// named. Only config layers the model CANNOT write may feed a sandbox grant.
func TestSessionInfraRootsIgnoreTheProjectMCPLayer(t *testing.T) {
	hermeticInfraEnv(t)
	root := t.TempDir()
	runInfraGit(t, root, "init", "-q")
	env := execenv.NewLocalExecutionEnvironment(root)

	// A plausible-looking server the model could plant, naming a real script that
	// sits outside the worktree.
	outside := t.TempDir()
	script := writeInfraFile(t, filepath.Join(outside, "server.js"), "// mcp\n", 0o644)
	writeInfraFile(t, filepath.Join(root, ".serf", "mcp.json"),
		`{"mcpServers":{"planted":{"command":"node","args":["`+script+`"]}}}`, 0o644)

	cfg := SessionConfig{Sandbox: "restricted"}
	got := SessionInfraRoots(cfg, env)
	if slices.Contains(got, canonInfra(t, outside)) {
		t.Errorf("a model-writable .serf/mcp.json must not feed the sandbox policy, got %v", got)
	}
	if len(got) != 0 {
		t.Errorf("the project MCP layer must contribute no infra roots at all, got %v", got)
	}
}

// TestSessionInfraRootsResolveIntoNothingOutsideTheWorktree is the end-to-end
// guard the suite lacked: the derivation tests never resolved a policy, and the
// live tests set InfraReadRoots by hand, so nothing pinned that a root the
// DERIVATION produces is a root Resolve actually grants. This runs the real
// derivation over a hostile in-worktree config and asserts the resolved
// restricted-mode spawned read surface gains nothing outside the worktree.
func TestSessionInfraRootsResolveIntoNothingOutsideTheWorktree(t *testing.T) {
	hermeticInfraEnv(t)
	root := t.TempDir()
	runInfraGit(t, root, "init", "-q")
	env := execenv.NewLocalExecutionEnvironment(root)

	// Everything a hostile .serf/mcp.json could name: the home ancestor, home
	// itself, a directory-valued arg, and a script outside the lane.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no resolvable home directory on this host")
	}
	otherLane := t.TempDir()
	writeInfraFile(t, filepath.Join(root, ".serf", "mcp.json"),
		`{"mcpServers":{"evil":{"command":"/bin/sh","args":["`+filepath.Dir(filepath.Clean(home))+
			`","`+filepath.Clean(home)+`","`+otherLane+`"]}}}`, 0o644)

	infra := SessionInfraRoots(SessionConfig{Sandbox: "restricted"}, env)
	net := true
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{
		Mode: sandbox.ModeRestricted, Network: &net, InfraReadRoots: infra,
	}, sandbox.RealProber{}.Probe(), root)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	assertNoReadRootOutside(t, root, infra, rp)
}

// assertNoReadRootOutside asserts that the roots the DERIVATION contributed add
// nothing to the restricted spawned read surface outside the worktree.
//
// It measures a DELTA against the same policy resolved with no InfraReadRoots
// rather than checking against a hardcoded list of expected system roots. The
// resolver grants other things for its own reasons (the system exec roots, and the
// developer-toolchain roots), and those are not this feature's to police; a
// hardcoded allowlist would also rot silently every time the resolver gains a
// grant. The delta isolates exactly what SessionInfraRoots caused.
func assertNoReadRootOutside(t *testing.T, worktree string, infra []string, rp sandbox.ResolvedPolicy) {
	t.Helper()
	net := true
	baseline, err := sandbox.Resolve(sandbox.SandboxPolicy{
		Mode: sandbox.ModeRestricted, Network: &net,
	}, sandbox.RealProber{}.Probe(), worktree)
	if err != nil {
		t.Fatalf("Resolve baseline: %v", err)
	}
	canonRoot := canonInfra(t, worktree)
	for _, r := range rp.Spawned.ReadRoots {
		if slices.Contains(baseline.Spawned.ReadRoots, r) {
			continue // granted regardless of this feature
		}
		if r == canonRoot || strings.HasPrefix(r, canonRoot+string(filepath.Separator)) {
			continue // inside the session's own lane
		}
		t.Errorf("the hook/MCP derivation added read root %q outside the worktree\n  derived: %v\n  resolved: %v", r, infra, rp.Spawned.ReadRoots)
	}
}

func runInfraGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// TestSessionInfraRootsRefuseCaseVariantOfHome closes the last spelling hole. On a
// case-insensitive filesystem (APFS by default, NTFS) `/Users/JESSE` and
// `/Users/jesse` are the SAME directory, but they are not the same string, and
// filepath.EvalSymlinks returns whichever spelling it was given — canonicalization
// does not collapse them. A byte-exact anchor check therefore sees depth 2 and no
// ancestor relationship and lets it through, while Seatbelt's own `subpath`
// matching is case-INSENSITIVE, so the kernel really does grant the whole home
// tree. The same shape covers Unicode NFC-vs-NFD spellings, which APFS also treats
// as one file.
//
// Skipped on a genuinely case-sensitive filesystem, where the two names are
// different directories and refusing the second would be wrong.
func TestSessionInfraRootsRefuseCaseVariantOfHome(t *testing.T) {
	hermeticInfraEnv(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no resolvable home directory on this host")
	}
	home = filepath.Clean(home)
	variant := filepath.Join(filepath.Dir(home), strings.ToUpper(filepath.Base(home)))
	if variant == home {
		t.Skip("home directory name has no distinct upper-case spelling")
	}
	hi, err := os.Stat(home)
	if err != nil {
		t.Skipf("cannot stat home: %v", err)
	}
	vi, err := os.Stat(variant)
	if err != nil || !os.SameFile(hi, vi) {
		t.Skip("case-sensitive filesystem: the upper-case spelling is a different directory")
	}

	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	cfg := SessionConfig{Sandbox: "restricted", PluginDirs: []string{variant}}
	if got := SessionInfraRoots(cfg, env); len(got) != 0 {
		t.Errorf("a case-variant spelling of $HOME must not be granted, got %v", got)
	}
}

// TestTrustedConfigCannotReachOutsideTheWorktree drives the hostile paths through a
// TRUSTED layer (--mcp-config), so the project-layer exclusion cannot be what makes
// it pass. The sibling test above it is dropped wholesale before the guard is ever
// consulted, which means it would still pass with infraGuard.permit's body deleted;
// this one exercises permit for real.
func TestTrustedConfigCannotReachOutsideTheWorktree(t *testing.T) {
	hermeticInfraEnv(t)
	root := t.TempDir()
	runInfraGit(t, root, "init", "-q")
	env := execenv.NewLocalExecutionEnvironment(root)

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no resolvable home directory on this host")
	}
	home = filepath.Clean(home)

	hostile := []string{filepath.Dir(home), home, os.TempDir(), "/tmp", t.TempDir()}
	if v := filepath.Join(filepath.Dir(home), strings.ToUpper(filepath.Base(home))); v != home {
		hostile = append(hostile, v)
	}
	if _, err := os.Stat(filepath.Join(dataVolumePrefixForTest, "Users")); err == nil {
		hostile = append(hostile, filepath.Join(dataVolumePrefixForTest, "Users"))
	}
	quoted := make([]string, 0, len(hostile))
	for _, h := range hostile {
		quoted = append(quoted, `"`+h+`"`)
	}
	mcpJSON := writeInfraFile(t, filepath.Join(root, "trusted-mcp.json"),
		`{"mcpServers":{"evil":{"command":"/bin/sh","args":[`+strings.Join(quoted, ",")+`]}}}`, 0o644)

	infra := SessionInfraRoots(SessionConfig{Sandbox: "restricted", MCPConfigFiles: []string{mcpJSON}}, env)
	net := true
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{
		Mode: sandbox.ModeRestricted, Network: &net, InfraReadRoots: infra,
	}, sandbox.RealProber{}.Probe(), root)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	assertNoReadRootOutside(t, root, infra, rp)
}

// dataVolumePrefixForTest mirrors the production constant so the test can build an
// alias path without exporting it.
const dataVolumePrefixForTest = "/System/Volumes/Data"

// TestSessionInfraRootsGrantLegitimateRootsUnderHome is the positive regression
// test for the guard. Everything else in this file proves what is REFUSED, and the
// refusals are easy to over-tighten: the guard's anchors include $HOME itself, and
// the whole point of the feature is granting a plugin cache that normally lives
// UNDER $HOME (~/.claude/plugins/..., ~/.config/serf/plugins/...). Nothing else here
// would notice if a tightening of RootGuard.Permit — the os.SameFile spelling check
// especially — started refusing those too, because the other tests all use
// t.TempDir(), which sits under the TEMP anchor rather than under home.
//
// A path under an anchor must stay grantable; only a path at or ABOVE one is
// refused.
func TestSessionInfraRootsGrantLegitimateRootsUnderHome(t *testing.T) {
	hermeticInfraEnv(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no resolvable home directory on this host")
	}
	// The agent test harness points HOME at a temp dir, so this writes nothing into
	// a real developer's home.
	if !strings.Contains(home, os.TempDir()) && !strings.Contains(home, "serf-agent-home") {
		t.Skip("test harness home is not a scratch directory; refusing to write into a real home")
	}

	pluginDir := filepath.Join(home, ".claude", "plugins", "superpowers")
	writeInfraFile(t, filepath.Join(pluginDir, "hooks", "session-start.sh"), "#!/bin/sh\n", 0o755)
	script := writeInfraFile(t, filepath.Join(home, ".config", "serf", "servers", "server.js"), "// mcp\n", 0o644)
	mcpJSON := writeInfraFile(t, filepath.Join(t.TempDir(), "mcp.json"),
		`{"mcpServers":{"under-home":{"command":"node","args":["`+script+`"]}}}`, 0o644)

	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	cfg := SessionConfig{Sandbox: "restricted", PluginDirs: []string{pluginDir}, MCPConfigFiles: []string{mcpJSON}}
	got := SessionInfraRoots(cfg, env)

	for _, want := range []string{canonInfra(t, pluginDir), canonInfra(t, filepath.Dir(script))} {
		if !slices.Contains(got, want) {
			t.Errorf("a legitimate root UNDER $HOME must still be granted: %q missing from %v", want, got)
		}
	}
}
