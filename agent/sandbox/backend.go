package sandbox

import (
	"fmt"
	"os"
	"os/exec"
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
	// sessionTmp is the per-session scratch CONTAINER — the one directory the
	// backend is given. It is reachable but never writable: the child's TMPDIR is
	// its `tmp` subtree and its EVENER_SCRATCH_DIR is its `private` subtree (see
	// SessionScratchTmpDir / SessionScratchPrivateDir), which are the writable
	// roots the backends derive from it.
	sessionTmp string
}

var wrapSeatbelt = seatbeltWrap

// NewWrapper builds a kernel wrapper for policy using the backend binary at
// binaryPath and the per-session tmp dir sessionTmp. It refuses a backend that
// imposes no containment (BackendNone) and a non-absolute binary path: a
// cwd-relative sandbox binary is a PATH-injection vector — a spawned command
// could drop a fake "bwrap"/"sandbox-exec" beside the worktree and neuter the
// sandbox. The caller resolves the binary via HostFacts.BwrapPath /
// HostFacts.SandboxExecPath (both absolute). The Seatbelt Wrap path additionally
// hard-codes /usr/bin/sandbox-exec regardless of binaryPath, so a bogus stored
// path can never redirect the exec.
//
// It also MUTATES sessionTmp when that directory exists: the container is put into
// the session-scratch layout (traversable container, `0700` private subtree,
// `1777`+sticky temp subtree, legacy direct entries migrated into the private
// subtree), because the wrapper's writable roots and the environment floor's
// $TMPDIR/$EVENER_SCRATCH_DIR are derived from it. That is idempotent for a
// container NewSessionScratch or a restore already laid out — every in-repo caller
// passes such a scratch — and it fails closed rather than unlink anything, so the
// constructor can fail where it previously could not. A caller must therefore hold
// whatever exclusivity the directory needs: the restore path (rebuildSandboxWrapper)
// only ever unwraps an allocation OpenRetainedSessionScratch has already prepared
// under its lease.
func NewWrapper(policy ResolvedPolicy, binaryPath, sessionTmp string) (*Wrapper, error) {
	switch policy.Backend {
	case BackendBwrap, BackendSeatbelt:
	default:
		return nil, fmt.Errorf("sandbox: kernel wrapper requires an enforcing backend (bwrap or seatbelt), got %s", policy.Backend)
	}
	if !filepath.IsAbs(binaryPath) {
		return nil, fmt.Errorf("sandbox: backend binary path %q must be an absolute path (cwd-relative sandbox binaries are a PATH-injection vector)", binaryPath)
	}
	// bubblewrap pins a MISSING protected git surface by mounting over it, which
	// materializes that name on the real filesystem — so the surfaces whose empty
	// residue would break the repo are written with inert content first, once per
	// wrapper, rather than as a side effect of every Wrap. Seatbelt matches path
	// strings and creates nothing, so it needs no preparation.
	if policy.Backend == BackendBwrap {
		if err := prepareGitSurfaces(policy); err != nil {
			return nil, err
		}
	}
	// The wrapper derives its writable roots from the container's two exported
	// subtrees, and the environment floor names them as $TMPDIR and
	// $EVENER_SCRATCH_DIR, so the container has to be in the exported layout before
	// it is wrapped: a bare or legacy `0700` directory would export a `$TMPDIR` whose
	// parent a dropped-privilege child cannot traverse. prepareSessionScratch is
	// idempotent for a container Evener already laid out, and fails closed rather
	// than unlink a caller's entry. A container path that does not exist is left
	// alone: the wrapper is handed a directory Evener provisioned, and inventing a
	// tree for a caller that passed a path it never created would hide the caller's
	// own error rather than fix the sandbox.
	if sessionTmp != "" {
		if info, err := os.Stat(sessionTmp); err == nil && info.IsDir() {
			if err := prepareSessionScratchForWrapper(sessionTmp); err != nil {
				return nil, err
			}
		}
	}
	return &Wrapper{policy: policy, binaryPath: binaryPath, sessionTmp: sessionTmp}, nil
}

// Policy returns the resolved policy this wrapper enforces.
func (w *Wrapper) Policy() ResolvedPolicy { return w.policy }

// SessionTmp returns the per-session scratch CONTAINER the wrapper was built with.
// It is traversal-only inside the sandbox: the child's $TMPDIR is
// SessionScratchTmpDir(SessionTmp()) and its $EVENER_SCRATCH_DIR is
// SessionScratchPrivateDir(SessionTmp()). Callers that need one of those paths must
// derive it, never use this value as the child's temp directory.
func (w *Wrapper) SessionTmp() string { return w.sessionTmp }

// Confine rewrites cmd to run under the wrapper's backend confinement: it prepends
// the backend invocation to cmd.Args (updating cmd.Path via Wrap) and, for the
// Seatbelt backend, sets cmd.Dir to dir. sandbox-exec has no chdir flag (unlike
// bwrap's --chdir, which Wrap encodes in the argv), so without this the confined
// child would inherit evener's process cwd instead of the worktree. It is the single
// spawn-site helper every kernel-wrapped command routes through (execenv, hooks,
// mcp) so the Seatbelt cwd handling lives in one place rather than being duplicated
// at each site. A nil wrapper leaves cmd unchanged (byte-identical to an
// unsandboxed spawn); an empty dir leaves cmd.Dir as the caller set it.
func (w *Wrapper) Confine(cmd *exec.Cmd, dir string) {
	if w == nil {
		return
	}
	if w.policy.Backend == BackendSeatbelt && dir != "" {
		cmd.Dir = dir
	}
	argv := w.Wrap(cmd.Args, dir)
	cmd.Path = argv[0]
	cmd.Args = argv
}

// ConfineTrustedInfra confines cmd exactly like Confine — same filesystem
// binds, exec floor, fd hygiene — EXCEPT the network is never severed, even
// under a net=off session policy. It exists for trusted-infrastructure spawns:
// MCP server subprocesses, launched only from config layers the model cannot
// write (docs/sandboxing.md), get a network carve-out because the config that
// started them is trusted, not model-directed. Model-authored spawned
// processes (the shell, hooks, rg) must go through Confine, which stays
// network-severed under net=off — this method must never be used for those.
func (w *Wrapper) ConfineTrustedInfra(cmd *exec.Cmd, dir string) {
	w.withNetworkAllowed().Confine(cmd, dir)
}

// withNetworkAllowed returns a Wrapper identical to w except its policy's
// Network is forced true, so Wrap/Confine on the copy never emit a network
// denial regardless of the session's real net=off/on setting. A nil receiver
// stays nil, so ConfineTrustedInfra on an unsandboxed (nil) wrapper is still a
// no-op, matching Confine's nil behavior.
func (w *Wrapper) withNetworkAllowed() *Wrapper {
	if w == nil {
		return nil
	}
	dup := *w
	dup.policy.Network = true
	return &dup
}

// Wrap prepends the backend invocation to argv so the command runs confined to
// the wrapper's policy. For bwrap the returned slice is
// [bwrap, <flags...>, --argv0, argv[0], --, argv...] and cwd becomes the sandbox
// working directory via a --chdir flag. For Seatbelt it is
// [/usr/bin/sandbox-exec, -p, <policy>, -DKEY=path..., --, argv...] and cwd is
// IGNORED — sandbox-exec has no chdir flag, so the caller must set the spawned
// command's cmd.Dir itself (use Confine, which does this). Either is ready to hand
// to exec.Command / syscall.Exec. A nil wrapper returns argv unchanged so callers
// can wrap unconditionally.
func (w *Wrapper) Wrap(argv []string, cwd string) []string {
	if w == nil {
		return argv
	}
	if w.policy.Backend == BackendSeatbelt {
		wrapped, err := wrapSeatbelt(argv, w.policy, w.sessionTmp, cwd)
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
