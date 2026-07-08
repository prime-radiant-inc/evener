package sandbox

import (
	"fmt"
	"path/filepath"
)

// Wrapper turns a spawned command's argv into a kernel-wrapped argv that
// confines the command — and every descendant it forks — to a ResolvedPolicy's
// boundary. It is built once per sandboxed session from the resolved policy, the
// probed backend binary, and the per-session tmp dir, then carried on the
// execution environment and threaded to the MCP-stdio and hook spawn sites
// (which do not flow through execenv). A nil *Wrapper means no kernel confinement
// — exactly today's behavior, so a non-sandboxed spawn is byte-identical to
// before.
//
// It drives the bwrap backend on Linux (Wrap prepends the bubblewrap flags) and
// the Seatbelt backend on macOS (Wrap prepends sandbox-exec + the generated
// SBPL); the backend is chosen by the resolver and recorded on the policy.
type Wrapper struct {
	policy     ResolvedPolicy
	binaryPath string // absolute backend binary, resolved outside cwd (PATH-injection defense)
	sessionTmp string // per-session writable tmp; also the child's TMPDIR
}

// NewWrapper builds a kernel wrapper for policy using the backend binary at
// binaryPath and the per-session tmp dir sessionTmp. It refuses a backend that
// imposes no containment (BackendNone) and a non-absolute binary path: a
// cwd-relative sandbox binary is a PATH-injection vector — a spawned command
// could drop a fake "bwrap"/"sandbox-exec" beside the worktree and neuter the
// sandbox. The caller resolves the binary via HostFacts.BwrapPath /
// HostFacts.SandboxExecPath (both absolute). The Seatbelt Wrap path additionally
// hard-codes /usr/bin/sandbox-exec regardless of binaryPath, so a bogus stored
// path can never redirect the exec.
func NewWrapper(policy ResolvedPolicy, binaryPath, sessionTmp string) (*Wrapper, error) {
	switch policy.Backend {
	case BackendBwrap, BackendSeatbelt:
	default:
		return nil, fmt.Errorf("sandbox: kernel wrapper requires an enforcing backend (bwrap or seatbelt), got %s", policy.Backend)
	}
	if !filepath.IsAbs(binaryPath) {
		return nil, fmt.Errorf("sandbox: backend binary path %q must be an absolute path (cwd-relative sandbox binaries are a PATH-injection vector)", binaryPath)
	}
	return &Wrapper{policy: policy, binaryPath: binaryPath, sessionTmp: sessionTmp}, nil
}

// Policy returns the resolved policy this wrapper enforces.
func (w *Wrapper) Policy() ResolvedPolicy { return w.policy }

// SessionTmp returns the per-session writable tmp directory (the child's TMPDIR).
func (w *Wrapper) SessionTmp() string { return w.sessionTmp }

// Wrap prepends the backend invocation to argv so the command runs confined to
// the wrapper's policy with cwd as its working directory inside the sandbox. For
// bwrap the returned slice is [bwrap, <flags...>, --argv0, argv[0], --, argv...];
// for Seatbelt it is [/usr/bin/sandbox-exec, -p, <policy>, -DKEY=path..., --,
// argv...]. Either is ready to hand to exec.Command / syscall.Exec. A nil wrapper
// returns argv unchanged so callers can wrap unconditionally.
func (w *Wrapper) Wrap(argv []string, cwd string) []string {
	if w == nil {
		return argv
	}
	if w.policy.Backend == BackendSeatbelt {
		wrapped, err := seatbeltWrap(argv, w.policy, w.sessionTmp, cwd)
		if err != nil {
			// Unreachable in a real deployment: the resolver selects the seatbelt
			// backend only on darwin, where seatbeltWrap always succeeds. A seatbelt
			// policy reaching the non-darwin stub means the fail-closed floor was
			// bypassed; panic (fail closed, loudly) rather than return argv unwrapped
			// and run the command UNCONFINED.
			panic(fmt.Sprintf("sandbox: seatbelt backend selected on a host that cannot enforce it: %v", err))
		}
		return wrapped
	}
	flags := buildBwrapArgv(w.policy, w.sessionTmp, cwd)
	out := make([]string, 0, 1+len(flags)+3+len(argv))
	out = append(out, w.binaryPath)
	out = append(out, flags...)
	// --argv0 preserves the command's own argv[0] inside the sandbox (bwrap 0.9.0+).
	if len(argv) > 0 {
		out = append(out, "--argv0", argv[0])
	}
	out = append(out, "--")
	out = append(out, argv...)
	return out
}
