package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"primeradiant.com/evener/identifier"
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
		// The session scratch is allocated after Resolve, so a cache root
		// sharing storage with it (scratch kept under ~/.cache) is dropped
		// here, by the same rule Resolve applies to persistent write roots.
		policy.CacheRoots = dropCacheRootsSharingWriteRoots(slices.Clone(policy.CacheRoots), appendNonEmpty(nil, sessionTmp))
		policy = prepareCacheRoots(policy)
	}
	return &Wrapper{policy: policy, binaryPath: binaryPath, sessionTmp: sessionTmp}, nil
}

// prepareCacheRoots creates, empty, each overlaid cache root that does not exist
// yet. Such a root has no lower to overlay and a mount target bwrap cannot create
// in the read-only tree, so it would stay read-only while GOCACHE or GOMODCACHE
// still point at it; the go command would create the same empty directory on its
// first unsandboxed run. An empty directory cannot poison a cache. A root that
// cannot be served yet stays in the policy: each spawn judges it again
// (liveCacheRoots), so one that becomes available is served.
func prepareCacheRoots(rp ResolvedPolicy) ResolvedPolicy {
	if rp.CacheStrategy == CacheOverlay {
		recreateMissingCacheRoots(rp)
		warmOverlayProbes(rp)
	}
	return rp
}

// warmOverlayProbes asks the per-root overlay probe for each cache root that is
// a directory now, so the session's first spawn does not pay the probe's
// subprocess. A root that is not a directory yet is left unwarmed: its verdict
// is judged when it appears (servableCacheRoot, #4220), and warming it now would
// pin an unserved verdict it might outgrow.
func warmOverlayProbes(rp ResolvedPolicy) {
	if rp.resolveHost.OverlayLowerOK == nil {
		return
	}
	for _, c := range rp.CacheRoots {
		if fi, err := os.Stat(c); err == nil && fi.IsDir() {
			overlayLowerOK(rp, c)
		}
	}
}

// recreateMissingCacheRoots creates, empty, each cache root that has gone
// missing, at wrapper creation and again at every spawn (go clean -modcache on
// the host removes one), unless creating it would land inside a persistent
// write root or the worktree.
func recreateMissingCacheRoots(rp ResolvedPolicy) {
	for _, c := range rp.CacheRoots {
		if _, err := os.Lstat(c); errors.Is(err, fs.ErrNotExist) && !landsInPersistentRoot(rp, c) {
			_ = os.MkdirAll(c, 0o755)
		}
	}
}

// servableCacheRoot reports whether the overlay can serve cache root c now: it
// is a directory, it shares no storage with a persistent write root or the
// worktree (dropCacheRootsSharingWriteRoots, landsInPersistentRoot), and
// resolving it follows no link in or into bwrap's private /tmp or /dev. It is
// judged at each spawn, since a symlinked parent retargeted after resolution
// would otherwise move the overlay, and its discarded upper layer, into the
// workspace.
//
// A root whose filesystem this host refuses as an unprivileged overlay lower is
// never servable (overlayLowerOK): emitting it would abort every spawn with
// EINVAL (#4220). The verdict is judged here, at each spawn, so a root that only
// becomes a directory later is judged when it appears.
//
// The session scratch is checked once, when the wrapper is built (NewWrapper):
// a cache link retargeted onto the scratch mid-session would at worst send the
// session's own scratch writes into the discarded layer, which the threat
// model leaves to the session (docs/sandboxing.md#threat-model).
func servableCacheRoot(rp ResolvedPolicy, c string) bool {
	fi, err := os.Stat(c)
	if err != nil || !fi.IsDir() || landsInPersistentRoot(rp, c) || linksThroughPrivateMount(c) ||
		len(dropCacheRootsSharingWriteRoots([]string{c}, bwrapWriteRoots(rp))) == 0 {
		return false
	}
	// The shared-tree guard Resolve applied, applied again to where the root
	// resolves now: a retargeted parent must not make it a temp root or /.
	return len(guardedHostRoots([]string{resolveExistingPrefix(c)}, rp.resolveHost.Home, rp.Git.WorktreeRoot)) == 1 &&
		overlayLowerOK(rp, c)
}

// overlayLowerOK reports whether bwrap can mount an unprivileged overlay with c
// as its lower on this host (HostFacts.OverlayLowerOK, #4220). A nil probe means
// yes: no overlay support, or a hand-built policy in a test. The probe memoizes
// per root, so asking at every spawn costs one probe per root per session.
func overlayLowerOK(rp ResolvedPolicy, c string) bool {
	if rp.resolveHost.OverlayLowerOK == nil {
		return true
	}
	return rp.resolveHost.OverlayLowerOK(c)
}

// liveCacheRoots returns the cache roots bwrap overlays at this spawn: the
// servable ones (servableCacheRoot), minus any whose real location lies inside
// another's, which that outer overlay serves (a nested overlay would be a
// mountpoint go clean -modcache cannot remove). A root merely spelled under
// another but linked elsewhere keeps its own overlay. Of two spellings of one
// directory, the first is kept.
func liveCacheRoots(rp ResolvedPolicy) []string {
	var servable, resolved []string
	for _, c := range rp.CacheRoots {
		if servableCacheRoot(rp, c) {
			servable = append(servable, c)
			resolved = append(resolved, resolveExistingPrefix(c))
		}
	}
	var live []string
	for i, c := range servable {
		nested := false
		for j := range servable {
			if j == i {
				continue
			}
			// A root whose real location lies inside another's is covered by
			// that overlay, provided the sandbox reaches it there: spelled
			// inside the outer root, or the outer root not spelled under a
			// private mount (bwrap's own /tmp and /dev hide host symlinks, so
			// such an overlay sits elsewhere in the sandbox).
			inside := resolved[i] != resolved[j] && identifier.PathUnder(resolved[i], resolved[j]) || resolved[i] == resolved[j] && j < i
			reached := identifier.PathUnder(servable[i], servable[j]) || !inPrivateMount(servable[j])
			if inside && reached {
				nested = true
			}
		}
		if !nested {
			live = append(live, c)
		}
	}
	return live
}

// landsInPersistentRoot reports whether c's real location (or, not created
// yet, where creating it would land) is at or inside where a persistent write
// root or the worktree lands, dangling links included (physicalLanding).
func landsInPersistentRoot(rp ResolvedPolicy, c string) bool {
	rc := physicalLanding(c)
	return slices.ContainsFunc(appendNonEmpty(slices.Clone(bwrapWriteRoots(rp)), rp.Git.WorktreeRoot), func(w string) bool {
		return identifier.PathUnder(rc, physicalLanding(w))
	})
}

// Policy returns the resolved policy this wrapper enforces.
func (w *Wrapper) Policy() ResolvedPolicy { return w.policy }

// SessionTmp returns the per-session writable tmp directory (the child's TMPDIR).
func (w *Wrapper) SessionTmp() string { return w.sessionTmp }

// unconfirmedReason is the refusal reason of a command whose sandbox check has
// not answered yet.
const unconfirmedReason = "sandbox not yet confirmed: the host check timed out; retry the command"

// Confirm settles an unconfirmed policy (ResolvedPolicy.Unconfirmed) before a
// spawn: it reruns the host's user-namespace check, sharing a run already in
// progress, until one answers. Every spawn site calls it before Confine and
// spawns nothing when it errs. A check still without an answer when the run
// ends or ctx ends refuses this command with RefusalUnconfirmed, so a later
// command rechecks; a definitive "no" refuses with RefusalDefinitive, and every
// later command refuses the same way. A nil wrapper, or a policy that was never
// unconfirmed, confirms at once.
func (w *Wrapper) Confirm(ctx context.Context) error {
	if w == nil || w.policy.Backend != BackendBwrap || w.policy.resolveHost.BwrapRecheck == nil {
		return nil
	}
	switch w.policy.resolveHost.BwrapRecheck.Outcome(ctx) {
	case CheckPassed:
		return nil
	case CheckFailed:
		return noBwrapRefusal(w.policy.Mode, w.policy.Network)
	default:
		return &RefusalError{Mode: w.policy.Mode, Net: w.policy.Network, Kind: RefusalUnconfirmed, Reason: unconfirmedReason}
	}
}

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

// childEnvShell is the program the Seatbelt backend runs immediately after its
// "--" separator to apply the configured environment. sandbox-exec has no
// args-from-fd flag, so a shell reads the assignments from the private
// child-env fd and applies them itself. The path is absolute because the
// confined process's PATH is the session's, not the launcher's; /bin/bash is
// the macOS system shell and every construct the script uses is bash 3.2.
const childEnvShell = "/bin/bash"

// childEnvScriptTemplate is the fixed script that shell runs. The launcher
// reads NUL-separated records from the private fd: "-NAME" unsets a name and
// "+NAME=VALUE" assigns one. The text is built from constants only — never from
// configuration — and values are applied through quoting-only expansion, so a
// repository-controlled value is never evaluated as shell code. The final
// "exec N<&-" keeps the private fd out of the server, matching the fd hygiene a
// bare bwrap spawn gets.
const childEnvScriptTemplate = `while IFS= read -r -d '' rec; do
	case $rec in
	-*) unset "${rec#-}" ;;
	+*) export "${rec#+}" ;;
	esac
done <&%[1]d
exec %[1]d<&-
exec "$@"`

// ChildEnvDelta is the difference between the sandbox LAUNCHER's environment
// (cmd.Env) and the environment the CONFINED child must observe: Set carries
// every assignment the child gains, and Unset names every variable the child
// must not inherit from the launcher. Unset is sorted by name so the generated
// payload is deterministic.
//
// The delta is a difference rather than an overlay because the two environments
// are not related by one. The floor DERIVES assignments from the environment it
// judges — an npm_config_cache redirect is computed from npm's effective config,
// which a configured npm_config_userconfig can move — so merging a server's
// cfg.Env can change names cfg.Env never mentions. And it REMOVES names a config
// supplied, which an overlay of assignments alone cannot express: bwrap starts
// the child from the launcher's environment, so a variable the floor dropped
// from the child's environment would reach it anyway.
type ChildEnvDelta struct {
	Set   map[string]string
	Unset []string
}

// empty reports whether the delta changes nothing, so a backend pays for no
// private file and no flag when there is nothing to apply.
func (d ChildEnvDelta) empty() bool { return len(d.Set) == 0 && len(d.Unset) == 0 }

// ConfineWithChildEnv confines cmd exactly like Confine and delivers childEnv to
// the CONFINED process instead of to the backend launcher.
//
// The distinction is load-bearing. cmd.Env is the environment of bwrap or
// sandbox-exec themselves, and their dynamic loader reads LD_PRELOAD,
// LD_LIBRARY_PATH, DYLD_INSERT_LIBRARIES and friends BEFORE any namespace,
// mount, or Seatbelt policy exists. A configured environment whose contents come
// from a config layer a repository can write therefore must not ride there.
// The assignments instead ride a private fd (cmd.ExtraFiles) that the launcher
// reads at startup — bwrap's --args, or a shell after sandbox-exec's "--" — so
// their VALUES never appear in the launcher's argv, where host process-list
// diagnostics would see them. They still apply on the far side of the
// confinement: the namespaces and mounts are in place before the assignment is
// applied, and the loader that reads it is the child's own.
//
// Assignments are ordered by name so the payload is deterministic. The channel
// is appended to cmd.ExtraFiles: the spawn site owns it and must close it once
// the launcher has started (the launcher reads it during startup). A nil
// wrapper has no backend to apply them behind, so they join cmd.Env: an
// unconfined spawn's own environment IS the child's.
func (w *Wrapper) ConfineWithChildEnv(cmd *exec.Cmd, dir string, childEnv ChildEnvDelta) error {
	return w.confineWithChildEnv(cmd, dir, childEnv, false)
}

// ConfineTrustedInfraWithChildEnv is ConfineWithChildEnv with
// ConfineTrustedInfra's network carve-out: identical confinement except that
// egress is never severed, for trusted-infrastructure spawns.
func (w *Wrapper) ConfineTrustedInfraWithChildEnv(cmd *exec.Cmd, dir string, childEnv ChildEnvDelta) error {
	return w.confineWithChildEnv(cmd, dir, childEnv, true)
}

// confineWithChildEnv is the shared body: it wraps cmd.Args and splices
// childEnv's private fd into the wrapped invocation on the child's side of the
// separator.
func (w *Wrapper) confineWithChildEnv(cmd *exec.Cmd, dir string, childEnv ChildEnvDelta, trustedInfra bool) error {
	if w == nil {
		cmd.Env = mergeChildEnvAssignments(cmd.Env, childEnv)
		return nil
	}
	target := w
	if trustedInfra {
		target = w.withNetworkAllowed()
	}
	if target.policy.Backend == BackendSeatbelt && dir != "" {
		cmd.Dir = dir
	}
	original := cmd.Args
	wrapped := target.Wrap(original, dir)
	argv := wrapped
	if !childEnv.empty() {
		fd := childEnvFdBase + len(cmd.ExtraFiles)
		channel, err := childEnvFile(target.policy.Backend, childEnv)
		if err != nil {
			return err
		}
		argv = childEnvArgv(target.policy.Backend, wrapped, len(original), fd)
		cmd.ExtraFiles = append(cmd.ExtraFiles, channel)
	}
	cmd.Path = argv[0]
	cmd.Args = argv
	return nil
}

// childEnvNames returns childEnv's variable names in sorted order, so every
// generated environment or payload is deterministic.
func childEnvNames(childEnv map[string]string) []string {
	return slices.Sorted(maps.Keys(childEnv))
}

// childEnvFdBase is the child fd number of the first extra file: Go's exec
// numbers cmd.ExtraFiles from 3.
const childEnvFdBase = 3

// childEnvArgv splices the private child-env channel into a wrapped argv so the
// assignments apply to the confined process, never to the backend that starts
// it. n is the length of the ORIGINAL argv, which both backends place unchanged
// at the end of the wrapped vector, so the separator's position is arithmetic
// rather than a search: bwrap's "--args FD" goes just before the "--", and
// Seatbelt's shell prefix just after it. fd is where the payload fd lands in
// the child.
//
// A wrapped argv that does not end in the original argv behind a separator would
// put the assignments on the wrong side of the confinement, so it panics rather
// than deliver them to the launcher's loader — the same fail-closed posture Wrap
// takes for an unconfirmed policy.
func childEnvArgv(backend Backend, argv []string, n, fd int) []string {
	split := len(argv) - n
	if split < 1 || argv[split-1] != "--" {
		panic("sandbox: wrapped argv does not end in the original argv behind the backend separator")
	}
	var insert []string
	at := split
	switch backend {
	case BackendBwrap:
		at = split - 1
		insert = []string{"--args", strconv.Itoa(fd)}
	case BackendSeatbelt:
		insert = []string{childEnvShell, "-c", childEnvScript(fd), childEnvShell}
	}
	out := make([]string, 0, len(argv)+len(insert))
	out = append(out, argv[:at]...)
	out = append(out, insert...)
	out = append(out, argv[at:]...)
	return out
}

// childEnvScript is childEnvScriptTemplate with the private fd filled in.
func childEnvScript(fd int) string {
	return fmt.Sprintf(childEnvScriptTemplate, fd)
}

// childEnvPayload encodes childEnv as the NUL-separated records the private
// channel carries. bwrap's --args consumes bwrap flags, so bwrap gets
// --unsetenv NAME and --setenv NAME VALUE records; the Seatbelt shell gets the
// "-NAME"/"+NAME=VALUE" form its script reads.
//
// The unset names go first, so a name that somehow appeared in both halves
// would end up set rather than unset — the direction the merged environment,
// which cannot contain an unset name, actually assigns.
func childEnvPayload(backend Backend, childEnv ChildEnvDelta) []byte {
	unset := slices.Sorted(slices.Values(childEnv.Unset))
	names := childEnvNames(childEnv.Set)
	var payload []byte
	record := func(fields ...string) {
		for _, field := range fields {
			payload = append(payload, field...)
			payload = append(payload, 0)
		}
	}
	switch backend {
	case BackendBwrap:
		for _, name := range unset {
			record("--unsetenv", name)
		}
		for _, name := range names {
			record("--setenv", name, childEnv.Set[name])
		}
	case BackendSeatbelt:
		for _, name := range unset {
			record("-" + name)
		}
		for _, name := range names {
			record("+" + name + "=" + childEnv.Set[name])
		}
	}
	return payload
}

// mergeChildEnvAssignments applies childEnv to env by NAME: an unset name loses
// its entry, then a set name replaces any entry it had. It never mutates env.
func mergeChildEnvAssignments(env []string, childEnv ChildEnvDelta) []string {
	if childEnv.empty() {
		return env
	}
	unset := make(map[string]bool, len(childEnv.Unset))
	for _, name := range childEnv.Unset {
		unset[name] = true
	}
	out := make([]string, 0, len(env)+len(childEnv.Set))
	for _, kv := range env {
		if name, _, ok := strings.Cut(kv, "="); ok {
			if _, overridden := childEnv.Set[name]; overridden || unset[name] {
				continue
			}
		}
		out = append(out, kv)
	}
	for _, name := range childEnvNames(childEnv.Set) {
		out = append(out, name+"="+childEnv.Set[name])
	}
	return out
}

// withNetworkAllowed returns a Wrapper identical to w except its policy's
// Network is forced true, so Wrap/Confine on the copy run under the mode's
// net=on policy even when the session's real setting is off. A nil receiver
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
	if w.policy.Unconfirmed() {
		// Unreachable when every spawn site calls Confirm first. A command
		// built here would run sandboxed work the host never confirmed; panic
		// (fail closed, loudly) rather than hand one back.
		panic("sandbox: confining a command before Confirm settled an unconfirmed bwrap policy")
	}
	if w.policy.CacheStrategy == CacheOverlay {
		recreateMissingCacheRoots(w.policy)
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
