package sandbox

import (
	"fmt"
	"path/filepath"
)

// Wrapper turns a spawned command's argv into a bubblewrap-wrapped argv that
// confines the command — and every descendant it forks — to a ResolvedPolicy's
// boundary. It is built once per sandboxed session from the resolved policy, the
// probed bwrap binary, and the per-session tmp dir, then carried on the execution
// environment and threaded to the MCP-stdio and hook spawn sites (which do not
// flow through execenv). A nil *Wrapper means no kernel confinement — exactly
// today's behavior, so a non-sandboxed spawn is byte-identical to before.
//
// bwrap is the only Linux backend: M1's floor makes Landlock always refuse (it
// cannot subtract the in-worktree .git pointer from an allowlisted root), so the
// resolver never selects it. NewWrapper therefore accepts only BackendBwrap.
type Wrapper struct {
	policy     ResolvedPolicy
	bwrapPath  string // absolute bubblewrap binary, resolved outside cwd (PATH-injection defense)
	sessionTmp string // per-session writable tmp; also the child's TMPDIR
}

// NewWrapper builds a kernel wrapper for policy using the bwrap binary at
// bwrapPath and the per-session tmp dir sessionTmp. It refuses a non-bwrap
// backend (M3 enforces only through bubblewrap) and a non-absolute bwrap path: a
// cwd-relative sandbox binary is a PATH-injection vector — a spawned command
// could drop a fake "bwrap" beside the worktree and neuter the sandbox. The
// caller resolves the binary via HostFacts.BwrapPath (RealProber uses
// exec.LookPath, whose result is absolute) or os.Executable-adjacent discovery.
func NewWrapper(policy ResolvedPolicy, bwrapPath, sessionTmp string) (*Wrapper, error) {
	if policy.Backend != BackendBwrap {
		return nil, fmt.Errorf("sandbox: kernel wrapper requires the bwrap backend, got %s", policy.Backend)
	}
	if !filepath.IsAbs(bwrapPath) {
		return nil, fmt.Errorf("sandbox: bwrap path %q must be an absolute path (cwd-relative sandbox binaries are a PATH-injection vector)", bwrapPath)
	}
	return &Wrapper{policy: policy, bwrapPath: bwrapPath, sessionTmp: sessionTmp}, nil
}

// Policy returns the resolved policy this wrapper enforces.
func (w *Wrapper) Policy() ResolvedPolicy { return w.policy }

// SessionTmp returns the per-session writable tmp directory (the child's TMPDIR).
func (w *Wrapper) SessionTmp() string { return w.sessionTmp }

// Wrap prepends the bubblewrap invocation to argv so the command runs confined to
// the wrapper's policy with cwd as its working directory inside the sandbox. The
// returned slice is [bwrap, <flags...>, --argv0, argv[0], --, argv...] ready to
// hand to exec.Command / syscall.Exec. A nil wrapper returns argv unchanged so
// callers can wrap unconditionally.
func (w *Wrapper) Wrap(argv []string, cwd string) []string {
	if w == nil {
		return argv
	}
	flags := buildBwrapArgv(w.policy, w.sessionTmp, cwd)
	out := make([]string, 0, 1+len(flags)+3+len(argv))
	out = append(out, w.bwrapPath)
	out = append(out, flags...)
	// --argv0 preserves the command's own argv[0] inside the sandbox (bwrap 0.9.0+).
	if len(argv) > 0 {
		out = append(out, "--argv0", argv[0])
	}
	out = append(out, "--")
	out = append(out, argv...)
	return out
}
