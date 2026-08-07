package agent

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/sandbox"
	"primeradiant.com/serf/envvars"
)

// The capability preamble is the environment section's factual answer to "what
// can this session actually do?", so a model reads its box instead of
// discovering it by failing at it. Its binding rule is docs/sandboxing.md's:
// the banner NEVER OVERSTATES. Every line below is either read from the
// RESOLVED policy (mode, roots, masked count, cache strategy) or measured by
// the one session-start probe; a probe that could not run renders "unprobed",
// never a guess and never an intention.

// probedPathTools are the developer tools whose PATH presence the probe
// reports. Kept short on purpose: the probe is one subprocess with a tight
// timeout at session start, not a survey.
var probedPathTools = []string{"go", "node", "rg"}

// capabilityProbeTimeout bounds the single session-start probe, both as the
// context deadline and as the timeout ExecCommand arms its own timer from
// (ExecCommand's timer is independent of the context, so both derive from this
// one value). On expiry the probe result is the zero value — every measured
// line renders "unprobed" — and session start continues. A package-level var
// only so tests can adjust it.
var capabilityProbeTimeout = 2 * time.Second

// capabilityProbe is the result of that one probe. The zero value means the
// probe never produced a usable answer, which every renderer spells "unprobed".
type capabilityProbe struct {
	// ran reports that the probe command completed AND reported every expected
	// key. A partial reply is not partially believed: it is unprobed.
	ran bool
	// gitConfigReads reports that `git config --list` exited 0 inside this
	// session's execution environment (the sandbox included, since the probe
	// runs through the same spawn path every command does).
	//
	// The probed command is deliberately NOT `git --version`, which never opens
	// a config file and so reports success on a box where every real git
	// invocation fatals — a restricted session whose ~/.gitconfig is unreadable
	// is exactly that box. `git config --list` reads the system/global/local
	// config chain, which is the first thing every git command does.
	//
	// It is reported as what it is — a config read — and never as "git works":
	// a successful config read does not measure the index, object store, or
	// worktree writes.
	gitConfigReads bool
	// onPath holds one entry per probedPathTools name: whether `command -v`
	// found it.
	onPath map[string]bool
	// goCache and goModCache are the RESOLVED `go env GOCACHE` / `GOMODCACHE`
	// values — the paths this session's builds will actually use, whether that
	// is a sandbox redirect or a host configuration. Empty when go is absent or
	// `go env` failed. Surfacing them is diagnostic: a host-configured GOCACHE
	// on slow or shared storage has wedged sessions for hours, and the resolved
	// path makes that self-evident instead of invisible.
	goCache    string
	goModCache string
}

// capabilityFacts are the inputs the preamble renders: the resolved policy (nil
// when unsandboxed), the scratch directory already exported to spawned
// commands, whether PATH came from the login shell, and the probe.
type capabilityFacts struct {
	policy     *sandbox.ResolvedPolicy
	scratchDir string
	loginPATH  bool
	probe      capabilityProbe
}

func (f capabilityFacts) sandboxed() bool {
	return f.policy != nil && f.policy.Enforced()
}

// capabilityFactsFromEnv reads the facts out of an execution environment. A
// non-local env (or nil) contributes nothing beyond the probe.
func capabilityFactsFromEnv(env execenv.ExecutionEnvironment, probe capabilityProbe) capabilityFacts {
	facts := capabilityFacts{probe: probe}
	le, ok := env.(*execenv.LocalExecutionEnvironment)
	if !ok || le == nil {
		return facts
	}
	if le.Sandbox != nil && le.Sandbox.Enforced() {
		facts.policy = le.Sandbox
	}
	facts.scratchDir = le.SessionScratchDir()
	facts.loginPATH = le.LoginPATH != ""
	return facts
}

// capabilityPreambleLines renders the preamble as short "label: values" lines.
// A sandboxed session gets the sandbox-derived lines (writable roots, masked
// count, cache strategy, the sandboxed-go telemetry fact); an unsandboxed one
// gets the same section without them.
func capabilityPreambleLines(f capabilityFacts) []string {
	lines := make([]string, 0, 8)
	if f.sandboxed() {
		lines = append(lines,
			"Writable roots: "+writableRootsSummary(*f.policy),
			fmt.Sprintf("Masked paths: %d", len(f.policy.MaskedPaths)),
		)
	}
	lines = append(lines, "PATH: "+pathSource(f.loginPATH))
	if f.scratchDir != "" {
		lines = append(lines, "Scratch ($"+envvars.SERFScratchDir.Name+", $"+envvars.TmpDir.Name+"): "+f.scratchDir)
	}
	if f.sandboxed() {
		lines = append(lines, "Cache: "+f.policy.CacheStrategy.String())
	}
	if line := goCacheLine(f.probe); line != "" {
		lines = append(lines, line)
	}
	// Ruled 2026-08-06 (docs/sandboxing.md, "Known residual"): a sandboxed `go`
	// cannot write Go's telemetry counter/token file and logs one denied-write
	// line to stderr while exiting 0. Stated only when go was actually measured
	// present, so the line reports a fact about a toolchain this session has.
	if f.sandboxed() && f.probe.ran && f.probe.onPath["go"] {
		lines = append(lines, "go: telemetry writes denied (harmless stderr noise)")
	}
	lines = append(lines, "git: "+gitSummary(f.probe), "On PATH: "+onPathSummary(f.probe))
	return lines
}

// writableRootsSummary summarizes the spawned layer's write grants without
// dumping every path: the non-git roots verbatim (at most three, then a count),
// plus a count of the git-metadata paths, which are numerous, fixed, and
// uninteresting individually.
func writableRootsSummary(p sandbox.ResolvedPolicy) string {
	var roots []string
	gitPaths := 0
	for _, r := range p.Spawned.WriteRoots {
		if slices.Contains(p.Git.WritablePaths, r) {
			gitPaths++
			continue
		}
		roots = append(roots, r)
	}
	var out string
	switch {
	case len(roots) == 0:
		out = "none"
	case len(roots) <= 3:
		out = strings.Join(roots, ", ")
	default:
		out = fmt.Sprintf("%s (+%d more)", strings.Join(roots[:3], ", "), len(roots)-3)
	}
	if gitPaths > 0 {
		out += fmt.Sprintf("; git metadata: %d paths", gitPaths)
	}
	return out
}

// pathSource names where spawned commands' PATH came from. Both values are
// resolved facts: the login-shell probe either produced a PATH or it did not,
// in which case the inherited process PATH stands unchanged.
func pathSource(loginPATH bool) string {
	if loginPATH {
		return "login shell ($" + envvars.Shell.Name + " -lc)"
	}
	return "inherited process environment"
}

// goCacheLine states the resolved Go cache paths, or "unprobed" when the probe
// could not run. It renders nothing when go was measured absent: there is no
// resolved cache to state, and the toolchain line already reports go=no.
func goCacheLine(p capabilityProbe) string {
	if !p.ran {
		return "Go cache: unprobed"
	}
	if !p.onPath["go"] {
		return ""
	}
	if p.goCache == "" && p.goModCache == "" {
		return "Go cache: unprobed"
	}
	return fmt.Sprintf("Go cache: GOCACHE=%s %s=%s", p.goCache, envvars.GoModCache.Name, p.goModCache)
}

// gitSummary renders the git measurement as the exact command that was run and
// its exit status — never as the broader claim "git works", which no single
// probe measures.
func gitSummary(p capabilityProbe) string {
	switch {
	case !p.ran:
		return "unprobed"
	case p.gitConfigReads:
		return "`git config --list` exit 0"
	default:
		return "`git config --list` failed"
	}
}

// onPathSummary renders which probed tools `command -v` found, or "unprobed".
func onPathSummary(p capabilityProbe) string {
	if !p.ran {
		return "unprobed"
	}
	parts := make([]string, 0, len(probedPathTools))
	for _, tool := range probedPathTools {
		yes := "no"
		if p.onPath[tool] {
			yes = "yes"
		}
		parts = append(parts, tool+"="+yes)
	}
	return strings.Join(parts, " ")
}

// probeCapabilities runs the session's single toolchain probe THROUGH the
// session's own execution environment, so what it measures is what a real
// command would meet — inside the sandbox, with the session's PATH and env
// floor — rather than what the daemon process happens to see. It is one
// subprocess bounded by capabilityProbeTimeout; on any error, non-zero exit, or
// timeout it returns the zero value and every measured line renders "unprobed".
func probeCapabilities(env execenv.ExecutionEnvironment, cwd string) capabilityProbe {
	if env == nil {
		return capabilityProbe{}
	}
	if strings.TrimSpace(cwd) == "" {
		cwd = env.WorkingDirectory()
	}
	ctx, cancel := context.WithTimeout(context.Background(), capabilityProbeTimeout)
	defer cancel()
	res, err := env.ExecCommand(ctx, capabilityProbeScript(), int(capabilityProbeTimeout/time.Millisecond), cwd, nil)
	if err != nil || res.ExitCode != 0 {
		return capabilityProbe{}
	}
	return parseCapabilityProbe(res.Stdout)
}

// capabilityProbeScript is the probe command: whether git can read its config
// chain (see capabilityProbe.gitConfigReads for why that command and not
// `git --version`), which
// probed tools are on PATH, and go's resolved cache paths. `go env` stderr is
// discarded because a sandboxed go writes the telemetry denial there (the
// preamble states that fact separately).
func capabilityProbeScript() string {
	var b strings.Builder
	b.WriteString("git config --list >/dev/null 2>&1 && echo git=ok || echo git=no\n")
	for _, tool := range probedPathTools {
		fmt.Fprintf(&b, "command -v %s >/dev/null 2>&1 && echo %s=yes || echo %s=no\n", tool, tool, tool)
	}
	fmt.Fprintf(&b, "if command -v go >/dev/null 2>&1; then "+
		"echo gocache=$(go env GOCACHE 2>/dev/null); "+
		"echo gomodcache=$(go env %s 2>/dev/null); fi\n", envvars.GoModCache.Name)
	return b.String()
}

// parseCapabilityProbe turns the probe's key=value output into measured facts.
// A reply missing any expected key is treated as unprobed rather than partially
// believed: half a measurement invites the preamble to state something that was
// not measured.
func parseCapabilityProbe(out string) capabilityProbe {
	values := map[string]string{}
	for line := range strings.SplitSeq(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		if key, val, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			values[key] = val
		}
	}
	if _, ok := values["git"]; !ok {
		return capabilityProbe{}
	}
	p := capabilityProbe{ran: true, gitConfigReads: values["git"] == "ok", onPath: map[string]bool{}}
	for _, tool := range probedPathTools {
		val, ok := values[tool]
		if !ok {
			return capabilityProbe{}
		}
		p.onPath[tool] = val == "yes"
	}
	// The cache keys are present only when go is: their absence is not a partial
	// reply, and goCacheLine renders nothing for a measured-absent go.
	p.goCache = values["gocache"]
	p.goModCache = values["gomodcache"]
	return p
}
