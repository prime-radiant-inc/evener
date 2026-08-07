package agent

import (
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/sandbox"
	"primeradiant.com/serf/internal/bundled"
)

// probedFacts is the toolchain probe result the snapshots below render: the
// probe ran, git executed, go and node are on PATH, rg is not, and `go env`
// reported the resolved cache paths.
func probedFacts() capabilityProbe {
	return capabilityProbe{
		ran:        true,
		gitRuns:    true,
		onPath:     map[string]bool{"go": true, "node": true, "rg": false},
		goCache:    "/scratch/s1/gocache",
		goModCache: "/scratch/s1/gomodcache",
	}
}

// resolvePolicyForTest resolves a real policy for mode against a real git
// checkout, so the snapshots pin values the resolver actually produces rather
// than a hand-built literal that could drift from it. Host facts are fixed
// (linux/bwrap/overlay) so the result does not depend on the test host.
func resolvePolicyForTest(t *testing.T, mode sandbox.Mode) (*sandbox.ResolvedPolicy, string, string) {
	t.Helper()
	root := t.TempDir()
	home := t.TempDir()
	initGitRepo(t, root)
	host := sandbox.HostFacts{OS: "linux", Home: home, BwrapPath: "/usr/bin/bwrap", BwrapCapable: true, OverlaySupported: true}
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: mode}, host, root)
	if err != nil {
		t.Fatalf("Resolve(%s): %v", mode, err)
	}
	// The resolver canonicalizes the worktree (on macOS t.TempDir returns a
	// /var symlink into /private/var), so normalization must key off the
	// resolved root, not the raw temp path.
	return &rp, rp.Git.WorktreeRoot, home
}

// normalize makes a rendered preamble comparable across hosts by replacing the
// per-run temp paths with stable placeholders.
func normalize(s, root, home string) string {
	s = strings.ReplaceAll(s, root, "<root>")
	return strings.ReplaceAll(s, home, "<home>")
}

// TestCapabilityPreambleWorkspaceWrite pins the rendered preamble for a
// workspace-write session on an overlay-capable host.
func TestCapabilityPreambleWorkspaceWrite(t *testing.T) {
	policy, root, home := resolvePolicyForTest(t, sandbox.ModeWorkspaceWrite)
	got := strings.Join(capabilityPreambleLines(capabilityFacts{
		policy:     policy,
		scratchDir: "/scratch/s1",
		loginPATH:  true,
		probe:      probedFacts(),
	}), "\n")

	want := strings.Join([]string{
		"Writable roots: <root>; git metadata: 7 paths",
		"Masked paths: " + maskedCountString(policy),
		"PATH: login shell ($SHELL -lc)",
		"Scratch ($SERF_SCRATCH_DIR, $TMPDIR): /scratch/s1",
		"Cache: overlay",
		"Go cache: GOCACHE=/scratch/s1/gocache GOMODCACHE=/scratch/s1/gomodcache",
		"go: telemetry writes denied (harmless stderr noise)",
		"Toolchain: git=ok go=yes node=yes rg=no",
	}, "\n")
	if diff := normalize(got, root, home); diff != want {
		t.Errorf("workspace-write preamble:\ngot:\n%s\nwant:\n%s", diff, want)
	}
}

// TestCapabilityPreambleRestricted pins the rendered preamble for a restricted
// session (session-private cache, no overlay).
func TestCapabilityPreambleRestricted(t *testing.T) {
	policy, root, home := resolvePolicyForTest(t, sandbox.ModeRestricted)
	got := strings.Join(capabilityPreambleLines(capabilityFacts{
		policy:     policy,
		scratchDir: "/scratch/s1",
		loginPATH:  true,
		probe:      probedFacts(),
	}), "\n")

	want := strings.Join([]string{
		"Writable roots: <root>; git metadata: 7 paths",
		"Masked paths: " + maskedCountString(policy),
		"PATH: login shell ($SHELL -lc)",
		"Scratch ($SERF_SCRATCH_DIR, $TMPDIR): /scratch/s1",
		"Cache: session-private",
		"Go cache: GOCACHE=/scratch/s1/gocache GOMODCACHE=/scratch/s1/gomodcache",
		"go: telemetry writes denied (harmless stderr noise)",
		"Toolchain: git=ok go=yes node=yes rg=no",
	}, "\n")
	if diff := normalize(got, root, home); diff != want {
		t.Errorf("restricted preamble:\ngot:\n%s\nwant:\n%s", diff, want)
	}
}

// TestCapabilityPreambleUnsandboxed: an unsandboxed session gets the same
// section minus every sandbox-derived line (writable roots, masked count, cache
// strategy, and the sandbox-only go telemetry note).
func TestCapabilityPreambleUnsandboxed(t *testing.T) {
	got := strings.Join(capabilityPreambleLines(capabilityFacts{
		scratchDir: "/scratch/s1",
		loginPATH:  false,
		probe:      probedFacts(),
	}), "\n")

	want := strings.Join([]string{
		"PATH: inherited process environment",
		"Scratch ($SERF_SCRATCH_DIR, $TMPDIR): /scratch/s1",
		"Go cache: GOCACHE=/scratch/s1/gocache GOMODCACHE=/scratch/s1/gomodcache",
		"Toolchain: git=ok go=yes node=yes rg=no",
	}, "\n")
	if got != want {
		t.Errorf("unsandboxed preamble:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestCapabilityPreambleUnprobed: a probe that could not run renders "unprobed"
// for every measured line and never a guess. The sandbox-only telemetry note is
// withheld too — it is only stated when go was actually measured on PATH.
func TestCapabilityPreambleUnprobed(t *testing.T) {
	policy, root, home := resolvePolicyForTest(t, sandbox.ModeRestricted)
	got := strings.Join(capabilityPreambleLines(capabilityFacts{
		policy:     policy,
		scratchDir: "/scratch/s1",
		loginPATH:  true,
	}), "\n")

	want := strings.Join([]string{
		"Writable roots: <root>; git metadata: 7 paths",
		"Masked paths: " + maskedCountString(policy),
		"PATH: login shell ($SHELL -lc)",
		"Scratch ($SERF_SCRATCH_DIR, $TMPDIR): /scratch/s1",
		"Cache: session-private",
		"Go cache: unprobed",
		"Toolchain: unprobed",
	}, "\n")
	if diff := normalize(got, root, home); diff != want {
		t.Errorf("unprobed preamble:\ngot:\n%s\nwant:\n%s", diff, want)
	}
}

// TestCapabilityPreambleGoAbsent: with go measured absent, no Go cache line is
// rendered at all (there is no resolved cache to state) and no telemetry note.
func TestCapabilityPreambleGoAbsent(t *testing.T) {
	probe := probedFacts()
	probe.onPath["go"] = false
	probe.goCache, probe.goModCache = "", ""
	got := strings.Join(capabilityPreambleLines(capabilityFacts{
		scratchDir: "/scratch/s1",
		probe:      probe,
	}), "\n")

	want := strings.Join([]string{
		"PATH: inherited process environment",
		"Scratch ($SERF_SCRATCH_DIR, $TMPDIR): /scratch/s1",
		"Toolchain: git=ok go=no node=yes rg=no",
	}, "\n")
	if got != want {
		t.Errorf("go-absent preamble:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestCapabilityPreambleNoScratch: with no scratch dir provisioned, the scratch
// line is omitted rather than rendered with an empty or guessed path.
func TestCapabilityPreambleNoScratch(t *testing.T) {
	got := strings.Join(capabilityPreambleLines(capabilityFacts{probe: probedFacts()}), "\n")
	if strings.Contains(got, "Scratch") {
		t.Errorf("no scratch dir must render no scratch line, got:\n%s", got)
	}
}

// TestParseCapabilityProbe: the probe output parses into measured facts, and a
// reply missing any expected key is treated as unprobed rather than partially
// believed.
func TestParseCapabilityProbe(t *testing.T) {
	full := "git=ok\ngo=yes\nnode=no\nrg=yes\ngocache=/c/go\ngomodcache=/c/mod\n"
	p := parseCapabilityProbe(full)
	if !p.ran || !p.gitRuns || !p.onPath["go"] || p.onPath["node"] || !p.onPath["rg"] {
		t.Errorf("parsed probe = %+v, want git ok, go yes, node no, rg yes", p)
	}
	if p.goCache != "/c/go" || p.goModCache != "/c/mod" {
		t.Errorf("parsed cache = %q/%q, want /c/go and /c/mod", p.goCache, p.goModCache)
	}

	if p := parseCapabilityProbe("git=ok\ngo=yes\n"); p.ran {
		t.Errorf("a reply missing probed keys must be unprobed, got %+v", p)
	}
	if p := parseCapabilityProbe(""); p.ran {
		t.Errorf("empty probe output must be unprobed, got %+v", p)
	}
}

// TestCapabilityPreambleRendersInEnvironmentSection: the preamble lines reach
// the rendered prompt's <environment> block, each on its own line.
func TestCapabilityPreambleRendersInEnvironmentSection(t *testing.T) {
	resolver := &sectionResolver{
		provider: "anthropic",
		agent:    defaultAgentName,
		agentFS:  bundled.Agents(),
		sources:  []sectionSource{embedSource{fs: embeddedPrompts, prefix: "prompts/sections/"}},
	}
	data := promptData{
		WorkingDir:   "/w",
		Sandbox:      "restricted (network off) — fixed for this session",
		Capabilities: capabilityPreambleLines(capabilityFacts{scratchDir: "/scratch/s1", probe: probedFacts()}),
	}
	out, _, err := resolver.RenderEmbedded(embeddedPrompts, "prompts/templates/", "system", data)
	if err != nil {
		t.Fatalf("render error: %v", err)
	}
	for _, want := range []string{
		"\nSandbox: restricted (network off) — fixed for this session\n",
		"\nPATH: inherited process environment\n",
		"\nScratch ($SERF_SCRATCH_DIR, $TMPDIR): /scratch/s1\n",
		"\nGo cache: GOCACHE=/scratch/s1/gocache GOMODCACHE=/scratch/s1/gomodcache\n",
		"\nToolchain: git=ok go=yes node=yes rg=no\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered environment section missing %q", want)
		}
	}
}

// TestProbeCapabilitiesLive runs the real probe through a real execution
// environment — no fake, no injected output — so a broken probe script fails
// here rather than silently rendering "unprobed" forever. go is necessarily on
// PATH inside `go test`, which makes its measurement and its resolved GOCACHE
// assertable facts rather than host assumptions.
func TestProbeCapabilitiesLive(t *testing.T) {
	dir := t.TempDir()
	env := execenv.NewLocalExecutionEnvironment(dir)
	t.Cleanup(env.Cleanup)

	p := probeCapabilities(env, dir)
	if !p.ran {
		t.Fatalf("live probe did not produce a usable result: %+v", p)
	}
	if !p.onPath["go"] {
		t.Errorf("go must be measured on PATH inside a go test run: %+v", p)
	}
	if p.goCache == "" || p.goModCache == "" {
		t.Errorf("go env must resolve GOCACHE/GOMODCACHE: %+v", p)
	}
}

// TestProbeCapabilitiesUnrunnable: a probe that cannot run yields the zero
// value, which renders "unprobed" — never a guess.
func TestProbeCapabilitiesUnrunnable(t *testing.T) {
	if p := probeCapabilities(nil, ""); p.ran {
		t.Errorf("a nil environment must leave the probe unprobed, got %+v", p)
	}
}

// maskedCountString renders the masked-path count the preamble states, so the
// snapshots assert the count comes from the resolved policy rather than pinning
// a number that changes whenever the denylist does.
func maskedCountString(p *sandbox.ResolvedPolicy) string {
	return strconv.Itoa(len(p.MaskedPaths))
}
