package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/cmdutil"
)

var probeSandboxHost = func() sandbox.HostFacts {
	return sandbox.RealProber{}.Probe()
}

// provisionSandbox is cmdutil.ProvisionSandbox against the real host probe;
// tests swap probeSandboxHost to control the host facts.
func provisionSandbox(env *execenv.LocalExecutionEnvironment, cfg *agent.SessionConfig, cwd string) error {
	return cmdutil.ProvisionSandbox(env, cfg, cwd, probeSandboxHost)
}

// startScratchReclaim runs the reclaim for a command working in workingDir on
// its own goroutine, falling back to the process working directory when the
// command did not name one. A working directory that cannot be read skips the
// reclaim rather than sweeping with no workspace to exclude: the command is
// about to fail on the same lookup.
func startScratchReclaim(workingDir string) {
	if strings.TrimSpace(workingDir) == "" {
		wd, err := os.Getwd()
		if err != nil {
			return
		}
		workingDir = wd
	}
	go reclaimCrashedSessionScratch(workingDir, os.Stderr)
}

// reclaimCrashedSessionScratch removes the session scratch directories that
// sessions retained (lease released, directory kept) and never came back for.
// workingDir is the directory the command runs in, which the reclaim anchors to
// the same workspace root a session would (execenv.SessionScratchWorkspaceRoot),
// so a scratch base inside the workspace is off limits to the reclaim for
// exactly the reason it is off limits to allocation: nothing Evener owns is ever
// written there. The reclaim reads every scratch base and probes a lease per
// candidate, so startup runs it on its own goroutine and never waits on it; that
// outliving goroutine is why the callers hand it os.Stderr rather than a writer
// the caller owns. What it could not remove it names and leaves for the next
// start: two processes starting together can race on one directory, and the
// loser's failure is reclaimed the next time either of them starts.
func reclaimCrashedSessionScratch(workingDir string, warnings io.Writer) {
	if err := sandbox.SweepCrashedSessionScratch(execenv.SessionScratchWorkspaceRoot(workingDir)); err != nil {
		fmt.Fprintf(warnings, "warning: reclaiming crashed session scratch: %v; retrying at the next start\n", err) //nolint:errcheck
	}
}

// reconcileClearSandbox makes a cleared serve session's config agree with the
// environment it reuses. serve's thread/clear starts a fresh session on the SAME env, so
// the cleared session inherits the env's ACTUAL sandbox — which on resume is the
// persisted mode, not the launch flag. Setting the config's mode + network from the
// env's resolved policy inputs keeps what the session persists identical to what its
// env enforces; an off (unsandboxed) env clears the carrier so the cleared session
// is off too. Without this, a session resumed sandboxed but cleared under an off
// flag would persist off while running enforced (or the reverse), and the sandbox
// could silently evaporate on the next resume.
func reconcileClearSandbox(cfg *agent.SessionConfig, env *execenv.LocalExecutionEnvironment) {
	if env == nil || env.Sandbox == nil || !env.Sandbox.Enforced() {
		cfg.Sandbox = ""
		cfg.SandboxNet = nil
		return
	}
	in := env.Sandbox.Inputs()
	cfg.Sandbox = in.Mode.String()
	cfg.SandboxNet = in.Network
}

// sandboxEnforcementLine returns the single startup line describing what a
// sandboxed session enforces on this host (backend + mode + network + cache
// strategy), read from the environment's ACTUAL resolved policy so it can never
// overstate — a mode that degraded (e.g. overlay-unavailable → cold cache) or a
// backend the host could not serve is reflected exactly. Returns "" for an
// unsandboxed environment, so there is nothing to print for the default path.
func sandboxEnforcementLine(env execenv.ExecutionEnvironment) string {
	le, ok := env.(*execenv.LocalExecutionEnvironment)
	if !ok || le.Sandbox == nil {
		return ""
	}
	return sandbox.EnforcementLine(*le.Sandbox)
}
