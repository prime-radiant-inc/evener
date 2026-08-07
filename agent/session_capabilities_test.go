package agent

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/sandbox"
	"primeradiant.com/serf/internal/bundled"
)

// probedFacts is the toolchain probe result the snapshots below render: both
// probes ran, git read its config, go and node are on PATH, rg is not, and `go env`
// reported the resolved cache paths.
func probedFacts() capabilityProbe {
	return capabilityProbe{
		gitProbed:      true,
		gitConfigReads: true,
		toolsProbed:    true,
		onPath:         map[string]bool{"go": true, "node": true, "rg": false},
		goCache:        "/scratch/s1/gocache",
		goModCache:     "/scratch/s1/gomodcache",
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
		"git config read: `git config --list` exit 0",
		"On PATH: go=yes node=yes rg=no",
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
		"git config read: `git config --list` exit 0",
		"git under restricted: the global git config (~/.gitconfig, ~/.config/git/config) is readable but not writable; the grant covers those files only",
		"On PATH: go=yes node=yes rg=no",
	}, "\n")
	if diff := normalize(got, root, home); diff != want {
		t.Errorf("restricted preamble:\ngot:\n%s\nwant:\n%s", diff, want)
	}
}

// TestCapabilityPreambleRestrictedSeatbelt: the Seatbelt backend states the SAME
// git residuals as any other — no more. It used to carry a macOS-only extra, the
// xcrun shim's denied cache write and its multi-second cost; the env floor's
// toolchain PATH removed that residual on 2026-08-07, so stating it would now
// overstate the cost. This snapshot pins its absence, because a banner that
// never overstates is wrong in the pessimistic direction too.
func TestCapabilityPreambleRestrictedSeatbelt(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	initGitRepo(t, root)
	host := sandbox.HostFacts{OS: "darwin", Home: home, SandboxExecPath: "/usr/bin/sandbox-exec"}
	policy, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: sandbox.ModeRestricted}, host, root)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if policy.Backend != sandbox.BackendSeatbelt {
		t.Fatalf("backend = %s, want seatbelt", policy.Backend)
	}
	got := strings.Join(capabilityPreambleLines(capabilityFacts{
		policy: &policy, scratchDir: "/scratch/s1", loginPATH: true, probe: probedFacts(),
	}), "\n")

	want := strings.Join([]string{
		"Writable roots: <root>; git metadata: 7 paths",
		"Masked paths: " + maskedCountString(&policy),
		"PATH: login shell ($SHELL -lc)",
		"Scratch ($SERF_SCRATCH_DIR, $TMPDIR): /scratch/s1",
		"Cache: session-private",
		"Go cache: GOCACHE=/scratch/s1/gocache GOMODCACHE=/scratch/s1/gomodcache",
		"go: telemetry writes denied (harmless stderr noise)",
		"git config read: `git config --list` exit 0",
		"git under restricted: the global git config (~/.gitconfig, ~/.config/git/config) is readable but not writable; the grant covers those files only",
		"On PATH: go=yes node=yes rg=no",
	}, "\n")
	if diff := normalize(got, policy.Git.WorktreeRoot, home); diff != want {
		t.Errorf("restricted/seatbelt preamble:\ngot:\n%s\nwant:\n%s", diff, want)
	}
}

// TestCapabilityPreambleResidualsAreRestrictedOnly: the recorded git residuals
// belong to restricted mode, so no other mode may state them.
func TestCapabilityPreambleResidualsAreRestrictedOnly(t *testing.T) {
	for _, mode := range []sandbox.Mode{sandbox.ModeWorkspaceWrite, sandbox.ModeReadOnly} {
		policy, _, _ := resolvePolicyForTest(t, mode)
		got := strings.Join(capabilityPreambleLines(capabilityFacts{policy: policy, probe: probedFacts()}), "\n")
		if strings.Contains(got, "under restricted") {
			t.Errorf("%s must not state restricted-mode residuals:\n%s", mode, got)
		}
	}
	got := strings.Join(capabilityPreambleLines(capabilityFacts{probe: probedFacts()}), "\n")
	if strings.Contains(got, "under restricted") {
		t.Errorf("an unsandboxed session must not state restricted-mode residuals:\n%s", got)
	}
}

// TestCapabilityPreambleUnknownEnvMakesNoPathClaim: an execution environment
// this renderer cannot inspect yields NO PATH line. Rendering the "inherited
// process environment" default there would be a positive claim about an
// environment nobody measured — the same guess the preamble forbids everywhere
// else. Only the probe's measured lines survive.
func TestCapabilityPreambleUnknownEnvMakesNoPathClaim(t *testing.T) {
	facts := capabilityFactsFromEnv(nil, probedFacts())
	if !facts.unknownEnv {
		t.Fatalf("an uninspectable env must be marked unknown: %+v", facts)
	}
	lines := capabilityPreambleLines(facts)
	for _, line := range lines {
		if strings.HasPrefix(line, "PATH:") {
			t.Errorf("an uninspectable env must make no PATH claim, got %q", line)
		}
	}
	got := strings.Join(lines, "\n")
	if !strings.Contains(got, "On PATH: go=yes node=yes rg=no") {
		t.Errorf("the probe's own measurements must still render, got:\n%s", got)
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
		"git config read: `git config --list` exit 0",
		"On PATH: go=yes node=yes rg=no",
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
		"git config read: unprobed",
		"git under restricted: the global git config (~/.gitconfig, ~/.config/git/config) is readable but not writable; the grant covers those files only",
		"On PATH: unprobed",
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
		"git config read: `git config --list` exit 0",
		"On PATH: go=no node=yes rg=no",
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

// TestParseCapabilityProbe: each probe's output parses into measured facts, and
// a reply missing any expected key is treated as unprobed rather than partially
// believed.
func TestParseCapabilityProbe(t *testing.T) {
	if p := parseGitProbe("git=ok\n"); !p.gitProbed || !p.gitConfigReads {
		t.Errorf("parsed git probe = %+v, want a successful config read", p)
	}
	if p := parseGitProbe("git=no\n"); !p.gitProbed || p.gitConfigReads {
		t.Errorf("parsed git probe = %+v, want a measured failure (probed, not ok)", p)
	}
	if p := parseGitProbe(""); p.gitProbed {
		t.Errorf("empty git probe output must be unprobed, got %+v", p)
	}

	p := parseToolProbe("go=yes\nnode=no\nrg=yes\ngocache=/c/go\ngomodcache=/c/mod\n")
	if !p.toolsProbed || !p.onPath["go"] || p.onPath["node"] || !p.onPath["rg"] {
		t.Errorf("parsed tool probe = %+v, want go yes, node no, rg yes", p)
	}
	if p.goCache != "/c/go" || p.goModCache != "/c/mod" {
		t.Errorf("parsed cache = %q/%q, want /c/go and /c/mod", p.goCache, p.goModCache)
	}

	if p := parseToolProbe("go=yes\n"); p.toolsProbed {
		t.Errorf("a reply missing probed keys must be unprobed, got %+v", p)
	}
	if p := parseToolProbe(""); p.toolsProbed {
		t.Errorf("empty tool probe output must be unprobed, got %+v", p)
	}
}

// TestCapabilityPreambleGitProbeFailsAloneKeepsToolFacts: the two probes are
// bounded independently, so a git probe that times out (restricted mode's git
// costs ~4s per call — docs/sandboxing.md) must leave the PATH and cache
// measurements standing rather than collapsing the whole preamble to
// "unprobed". Only the git line degrades.
func TestCapabilityPreambleGitProbeFailsAloneKeepsToolFacts(t *testing.T) {
	probe := probedFacts()
	probe.gitProbed, probe.gitConfigReads = false, false
	got := strings.Join(capabilityPreambleLines(capabilityFacts{scratchDir: "/scratch/s1", probe: probe}), "\n")

	want := strings.Join([]string{
		"PATH: inherited process environment",
		"Scratch ($SERF_SCRATCH_DIR, $TMPDIR): /scratch/s1",
		"Go cache: GOCACHE=/scratch/s1/gocache GOMODCACHE=/scratch/s1/gomodcache",
		"git config read: unprobed",
		"On PATH: go=yes node=yes rg=no",
	}, "\n")
	if got != want {
		t.Errorf("git-only failure preamble:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestProbeCapabilitiesGitTimeoutIsIndependent proves that independence against
// a REAL execution environment and a REAL subprocess: with the git budget cut to
// an interval no process can finish in, the git half comes back unprobed while
// the tool half still reports live, measured facts.
func TestProbeCapabilitiesGitTimeoutIsIndependent(t *testing.T) {
	prev := gitProbeTimeout
	gitProbeTimeout = time.Nanosecond
	t.Cleanup(func() { gitProbeTimeout = prev })

	dir := t.TempDir()
	env := execenv.NewLocalExecutionEnvironment(dir)
	t.Cleanup(env.Cleanup)

	p := probeCapabilities(env, dir)
	if p.gitProbed {
		t.Errorf("git probe must not report a result it could not measure: %+v", p)
	}
	if !p.toolsProbed || !p.onPath["go"] || p.goCache == "" {
		t.Errorf("a timed-out git probe must not consume the tool measurements: %+v", p)
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
		"\ngit config read: `git config --list` exit 0\n",
		"\nOn PATH: go=yes node=yes rg=no\n",
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
	if !p.toolsProbed || !p.gitProbed {
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
	if p := probeCapabilities(nil, ""); p.toolsProbed || p.gitProbed {
		t.Errorf("a nil environment must leave the probe unprobed, got %+v", p)
	}
}

// maskedCountString renders the masked-path count the preamble states, so the
// snapshots assert the count comes from the resolved policy rather than pinning
// a number that changes whenever the denylist does.
func maskedCountString(p *sandbox.ResolvedPolicy) string {
	return strconv.Itoa(len(p.MaskedPaths))
}
