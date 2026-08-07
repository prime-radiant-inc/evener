package agent

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/sandbox"
	"primeradiant.com/serf/envvars"
)

// The capability preamble is the environment section's factual answer to "what
// can this session actually do?", so a model reads its box instead of
// discovering it by failing at it. Its binding rule is docs/sandboxing.md's:
// the banner NEVER OVERSTATES. Every line below is either read from the
// RESOLVED policy (mode, roots, masked count, cache strategy), quoted from a
// ruling that doc records, or measured by the session-start probes; a probe
// that could not run renders "unprobed", never a guess and never an intention.

// probedPathTools are the developer tools whose PATH presence the probe
// reports. Kept short on purpose: the probe is one subprocess with a tight
// timeout at session start, not a survey.
var probedPathTools = []string{"go", "node", "rg"}

// toolProbeTimeout bounds the cheap probe (three `command -v` calls and two
// `go env` reads). It is tight because that work is tight.
var toolProbeTimeout = 2 * time.Second

// gitProbeTimeout bounds the git probe SEPARATELY from the tool probe. It used
// to be sized against a genuinely expensive call — a `git` under `restricted`
// cost 3.62-4.42s (mean 3.98s) while the /usr/bin xcrun shim re-ran its tool
// lookup on every invocation, so the budget stood at 10s. That cost is gone:
// the env floor now names the developer toolchain's bin directory on PATH
// (sandbox.ResolvedPolicy.ToolchainBinDir), so a sandboxed `git` IS the real
// git. Re-measured 2026-08-07 as an interleaved A/B in a live restricted
// sandbox: shim path mean 8.61s (5.27-12.75s on a loaded host), toolchain path
// mean 164ms (127-225ms).
//
// 5s is therefore a fallback bound rather than an expected budget. It leaves
// ~30x headroom over the measured call, and still covers a host where the
// toolchain directory could not be named (no active developer directory, or one
// the RootGuard refused) and git falls back to the shim. Re-measure before
// changing this.
//
// It stays separate from the tool probe's tighter bound rather than merging into
// it: the two probes run CONCURRENTLY with independent deadlines, so a slow or
// wedged git can never consume the PATH and cache measurements, and the cheap
// facts still land at their own pace.
var gitProbeTimeout = 5 * time.Second

// capabilityProbe is what the session-start probes measured. Its two halves are
// tracked independently (gitProbed / toolsProbed) precisely so one failing does
// not erase the other. Each unset half renders "unprobed".
type capabilityProbe struct {
	// gitProbed reports that the git probe completed and answered.
	gitProbed bool
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

	// toolsProbed reports that the tool probe completed AND answered for every
	// probedPathTools name. A partial reply is not partially believed: it is
	// unprobed.
	toolsProbed bool
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
// commands, whether PATH came from the login shell, and the probes.
type capabilityFacts struct {
	policy     *sandbox.ResolvedPolicy
	scratchDir string
	loginPATH  bool
	probe      capabilityProbe

	// unknownEnv marks facts read from an execution environment this renderer
	// cannot inspect (nil, or an implementation other than the local one). The
	// PATH line is then omitted entirely: `loginPATH` false would otherwise
	// render "inherited process environment", a positive claim about an
	// environment nobody measured — the same guess the whole preamble forbids.
	//
	// The polarity is negative on purpose. The zero value must mean "readable",
	// because the fallthrough that CANNOT read the env is the one place that
	// sets it, and a default that silently claimed readability is exactly the
	// failure mode being closed.
	unknownEnv bool
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
		facts.unknownEnv = true
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
// count, cache strategy, the mode's recorded toolchain residuals); an
// unsandboxed one gets the same section without them.
func capabilityPreambleLines(f capabilityFacts) []string {
	lines := make([]string, 0, 10)
	if f.sandboxed() {
		lines = append(lines,
			"Writable roots: "+writableRootsSummary(*f.policy),
			fmt.Sprintf("Masked paths: %d", len(f.policy.MaskedPaths)),
		)
	}
	if !f.unknownEnv {
		lines = append(lines, "PATH: "+pathSource(f.loginPATH))
	}
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
	if f.sandboxed() && f.probe.toolsProbed && f.probe.onPath["go"] {
		lines = append(lines, "go: telemetry writes denied (harmless stderr noise)")
	}
	lines = append(lines, "git config read: "+gitSummary(f.probe))
	lines = append(lines, gitResidualLines(f)...)
	lines = append(lines, "On PATH: "+onPathSummary(f.probe))
	return lines
}

// gitResidualLines states the git residuals docs/sandboxing.md records for
// restricted mode, immediately after the git probe result so a failed probe has
// its recorded explanation adjacent rather than costing a session the turns to
// rediscover it. Same precedent as the go telemetry line: a resolved ruling is
// a fact the preamble may state, and it is stated only for the exact mode the
// ruling covers.
//
// The global-config line states the 2026-08-07 grant and its exact extent. It
// replaced the earlier line, which told sessions a present ~/.gitconfig fatals
// git: once the grant landed that became FALSE, and a stale false line is the
// precise failure the never-overstates principle exists to prevent. The scope
// clause is stated with it so the line cannot be read as a home-read grant it is
// not. It is deliberately scoped to THIS grant rather than claiming the home
// directory is otherwise unreadable: the hook/MCP infrastructure grant can also
// name a path under the home directory (a plugin dir under ~/.claude is the
// canonical case), so an absolute claim would overstate containment in the
// reassuring direction — the worse direction for a banner to be wrong in.
//
// The macOS xcrun line that used to sit beside it — "xcrun_db writes denied
// (2 stderr lines/call), ~4s/call" — is GONE with the residual it described:
// the env floor names the developer toolchain's bin directory on PATH, so a
// sandboxed git is the real git, silent and fast (2026-08-07). Keeping the line
// would have overstated the cost in the pessimistic direction, which the
// never-overstates principle forbids just as firmly as the reassuring one.
func gitResidualLines(f capabilityFacts) []string {
	if !f.sandboxed() || f.policy.Mode != sandbox.ModeRestricted {
		return nil
	}
	return []string{
		"git under restricted: the global git config (~/.gitconfig, " +
			"~/.config/git/config) is readable but not writable; the grant covers those files only",
	}
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

// goCacheLine states the resolved Go cache paths, or "unprobed" when the tool
// probe could not run. It renders nothing when go was measured absent: there is
// no resolved cache to state, and the On PATH line already reports go=no.
func goCacheLine(p capabilityProbe) string {
	if !p.toolsProbed {
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
	case !p.gitProbed:
		return "unprobed"
	case p.gitConfigReads:
		return "`git config --list` exit 0"
	default:
		return "`git config --list` failed"
	}
}

// onPathSummary renders which probed tools `command -v` found, or "unprobed".
func onPathSummary(p capabilityProbe) string {
	if !p.toolsProbed {
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

// probeCapabilities runs the session's toolchain probes THROUGH the session's
// own execution environment, so what they measure is what a real command would
// meet — inside the sandbox, with the session's PATH and env floor — rather than
// what the daemon process happens to see.
//
// It is two subprocesses run CONCURRENTLY under INDEPENDENT deadlines: the
// cheap tool probe (toolProbeTimeout) and the git probe (gitProbeTimeout, a
// fallback bound rather than an expected budget — see its comment).
// Independence is the point — a wedged git leaves the PATH and cache facts
// intact, and the cheap probe's tight bound is not relaxed to accommodate it.
// Session start therefore waits at most the LONGER of the two, and each half
// that errors, exits non-zero, times out, or answers partially renders
// "unprobed" on its own.
func probeCapabilities(env execenv.ExecutionEnvironment, cwd string) capabilityProbe {
	if env == nil {
		return capabilityProbe{}
	}
	if strings.TrimSpace(cwd) == "" {
		cwd = env.WorkingDirectory()
	}

	var wg sync.WaitGroup
	var git, tools capabilityProbe
	wg.Add(2)
	go func() {
		defer wg.Done()
		if out, ok := runProbeScript(env, cwd, gitProbeScript(), gitProbeTimeout); ok {
			git = parseGitProbe(out)
		}
	}()
	go func() {
		defer wg.Done()
		if out, ok := runProbeScript(env, cwd, toolProbeScript(), toolProbeTimeout); ok {
			tools = parseToolProbe(out)
		}
	}()
	wg.Wait()

	tools.gitProbed = git.gitProbed
	tools.gitConfigReads = git.gitConfigReads
	return tools
}

// runProbeScript runs one probe script and reports whether it produced a usable
// reply. The timeout is passed BOTH as the context deadline and as the
// millisecond timeout ExecCommand arms its own independent timer from, so the
// bound holds however the command ends.
func runProbeScript(env execenv.ExecutionEnvironment, cwd, script string, timeout time.Duration) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	res, err := env.ExecCommand(ctx, script, int(timeout/time.Millisecond), cwd, nil)
	if err != nil || res.ExitCode != 0 {
		return "", false
	}
	return res.Stdout, true
}

// gitProbeScript measures whether git can read its config chain (see
// capabilityProbe.gitConfigReads for why that command and not `git --version`).
func gitProbeScript() string {
	return "git config --list >/dev/null 2>&1 && echo git=ok || echo git=no\n"
}

// toolProbeScript measures which probed tools are on PATH and go's resolved
// cache paths. `go env` stderr is discarded because a sandboxed go writes the
// telemetry denial there (the preamble states that fact separately).
func toolProbeScript() string {
	var b strings.Builder
	for _, tool := range probedPathTools {
		fmt.Fprintf(&b, "command -v %s >/dev/null 2>&1 && echo %s=yes || echo %s=no\n", tool, tool, tool)
	}
	fmt.Fprintf(&b, "if command -v go >/dev/null 2>&1; then "+
		"echo gocache=$(go env GOCACHE 2>/dev/null); "+
		"echo gomodcache=$(go env %s 2>/dev/null); fi\n", envvars.GoModCache.Name)
	return b.String()
}

// parseGitProbe reads the git probe's reply.
func parseGitProbe(out string) capabilityProbe {
	values := probeValues(out)
	got, ok := values["git"]
	if !ok {
		return capabilityProbe{}
	}
	return capabilityProbe{gitProbed: true, gitConfigReads: got == "ok"}
}

// parseToolProbe reads the tool probe's reply. A reply missing any expected key
// is treated as unprobed rather than partially believed: half a measurement
// invites the preamble to state something that was not measured.
func parseToolProbe(out string) capabilityProbe {
	values := probeValues(out)
	p := capabilityProbe{toolsProbed: true, onPath: map[string]bool{}}
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

func probeValues(out string) map[string]string {
	values := map[string]string{}
	for line := range strings.SplitSeq(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		if key, val, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			values[key] = val
		}
	}
	return values
}
