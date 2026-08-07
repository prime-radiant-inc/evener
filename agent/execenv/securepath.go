package execenv

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"primeradiant.com/serf/agent/sandbox"
)

// This file holds sandboxFS's platform-independent policy logic — denial
// classification, root containment, and path arithmetic — and builds on every
// platform. The fd-anchored enforcement operations live in
// securepath_fdops_unix.go (linux/darwin); securepath_other.go supplies
// fail-closed stand-ins elsewhere.

var (
	canonicalPathForFd = canonicalPathOfFd
	secureRandRead     = rand.Read
	secureEntryInfo    = func(entry os.DirEntry) (os.FileInfo, error) { return entry.Info() }
	securePathRel      = filepath.Rel
)

// sandboxFS enforces a resolved sandbox policy on file operations using
// fd-anchored, symlink-refusing primitives. It is the in-process (privileged)
// enforcement layer described in the sandboxing design's "Race-safe path
// enforcement" section: it never re-opens a path after checking it, so a
// concurrent symlink swap between the check and the I/O makes the operation fail
// rather than redirect.
//
// A sandboxFS is built once per LocalExecutionEnvironment from its resolved
// policy (see e.sandbox()) and is only ever constructed for an ENFORCED policy —
// off mode / a nil policy never builds one, so those paths keep today's
// afero/os behavior byte-for-byte. It caches an O_DIRECTORY fd for each allowed
// root the first time that root is used, so a later swap of the root directory
// itself cannot redirect resolution.
//
// The file-operation methods are safe for concurrent use with each other. close()
// is NOT — it releases the cached root fds and must run only at environment
// teardown (Cleanup), after all file tools for the environment have returned; the
// session lifecycle guarantees this ordering.
type sandboxFS struct {
	policy *sandbox.ResolvedPolicy

	// grant, when non-empty, is a single per-invocation granted absolute path (M7
	// escalation approve). It widens ONLY root-containment for EXACTLY this one
	// path: a read/write of grant resolves as if grant's parent were an allowed
	// root, through a "/"-anchored open that refuses EVERY symlink in the path
	// (parent and leaf), so the grant widens containment only — never symlink
	// resolution, and never anchors on an unvetted parent directory. It never
	// overrides masking or git-protection, and never widens a sibling or a subtree.
	// It is set only on a short-lived clone (WithSandboxInvocationGrant) used for one
	// tool re-dispatch, never on a durable env, so it cannot outlive the invocation.
	grant string

	mu      sync.Mutex
	rootFds map[string]int // canonical root path → cached O_DIRECTORY fd
}

// isGranted reports whether abs is exactly this fs's single per-invocation granted
// path (never a sibling, never a subtree — precisely one leaf).
func (s *sandboxFS) isGranted(abs string) bool {
	return s.grant != "" && abs == s.grant
}

// newSandboxFS builds a sandboxFS for an enforced resolved policy. The caller
// (e.sandbox()) guarantees policy != nil and policy.Enforced().
//
// scratchDir, when non-empty, is the concrete per-session scratch directory
// (agent/sandbox.SessionScratch.Dir, via the env's Wrapper.SessionTmp()) folded
// into the policy's file-tool grants via WithSessionScratch: Resolve ran before
// the directory existed, so p alone never carries it. A blank scratchDir (an
// unsandboxed caller, or a policy resolved without a wrapper) leaves p untouched.
func newSandboxFS(p *sandbox.ResolvedPolicy, scratchDir string) *sandboxFS {
	policy := p
	if scratchDir != "" {
		granted := p.WithSessionScratch(scratchDir)
		policy = &granted
	}
	return &sandboxFS{policy: policy, rootFds: map[string]int{}}
}

// Denial reasons. The masked/protected reasons drive audit-log redaction to a
// <denied> token (the basename of a secret path is itself sensitive); the others
// redact to a basename.
const (
	denyReasonMasked       = "credential or pseudo-filesystem path is masked"
	denyReasonProtected    = "git config/hook surface is read-only under sandbox"
	denyReasonOutsideRead  = "outside the sandbox's readable roots"
	denyReasonOutsideWrite = "outside the sandbox's writable roots"
	denyReasonWriteDenied  = "writes are denied in this sandbox mode"
	denyReasonSymlink      = "refuses to traverse a symlinked or non-directory path component"
	denyReasonEscape       = "path resolves outside the sandbox root"
	denyReasonRootTarget   = "cannot operate on a sandbox root itself"
)

// errEscapesRoot is the sentinel a component walk returns when a path would
// escape its anchoring root (the macOS/other analogue of openat2's EXDEV under
// RESOLVE_BENEATH). Kept package-level so both the darwin walk and the shared
// error mapper agree on it.
var errEscapesRoot = errors.New("path escapes sandbox root")

// errSymlinkComponent is the sentinel a component walk returns when it refused to
// traverse a component: a symlinked directory opened with O_DIRECTORY|O_NOFOLLOW
// surfaces as either ELOOP or ENOTDIR depending on kernel check order (and a real
// swap during a race can flip which), so the walks map BOTH to this one sentinel
// without a second, race-prone lstat — the open already refused to follow, and
// this only classifies the resulting error into a legible typed denial.
var errSymlinkComponent = errors.New("sandbox: refused non-traversable path component")

// deny builds a typed *sandbox.DeniedError for a file-tool denial and emits one
// redacted audit line. Command/OutputSoFar are left empty — those belong to the
// shell/kernel denials M3 populates.
func (s *sandboxFS) deny(tool, denyPath, reason string) *sandbox.DeniedError {
	auditDenial(s.policy.Mode, tool, denyPath, reason)
	// A masked (credential/pseudo-fs) denial is Sensitive: neither the model
	// message nor the audit log may echo even the basename (id_rsa, credentials,
	// environ would reveal which secret was probed). Other reasons — outside a
	// root, a read-only git surface, a symlink component — carry an informative
	// non-secret path, so they keep the basename.
	sensitive := reason == denyReasonMasked
	modelReason := reason
	if !sensitive {
		// Tell the model the box is immutable — a per-session policy no tool call can
		// relax — so it stops retrying the same denied path. Mode-agnostic and
		// accurate for every mode; the audit record above keeps the terse reason.
		modelReason = reason + "; this sandbox policy is fixed for the session"
	}
	return &sandbox.DeniedError{
		Mode:       s.policy.Mode,
		Tool:       tool,
		Path:       denyPath,
		Reason:     modelReason,
		Sensitive:  sensitive,
		ReasonKind: denialReasonKind(reason),
	}
}

// denialReasonKind maps a display-text reason to its typed classification, so the
// two never diverge from one place (deny() is the single construction site). M7's
// escalation eligibility keys on the typed kind, never on this text.
func denialReasonKind(reason string) sandbox.DenialReason {
	switch reason {
	case denyReasonOutsideRead:
		return sandbox.DenialOutsideReadRoots
	case denyReasonOutsideWrite:
		return sandbox.DenialOutsideWriteRoots
	case denyReasonWriteDenied:
		return sandbox.DenialWritesDisabled
	case denyReasonMasked:
		return sandbox.DenialMasked
	case denyReasonProtected:
		return sandbox.DenialGitProtected
	case denyReasonSymlink:
		return sandbox.DenialSymlink
	case denyReasonEscape:
		return sandbox.DenialEscape
	case denyReasonRootTarget:
		return sandbox.DenialRootTarget
	default:
		return sandbox.DenialUnspecified
	}
}

// underMasked reports whether abs is at or beneath any masked (secrets/pseudo-fs)
// path from the resolved policy.
func (s *sandboxFS) underMasked(abs string) bool {
	for _, m := range s.policy.MaskedPaths {
		if abs == m || pathUnder(abs, m) {
			return true
		}
	}
	return false
}

// isGrantedRoot reports whether abs is exactly one of the policy's granted roots
// (file-tool read or write roots). Such a root exists by construction even when
// its parent lies outside the policy.
func (s *sandboxFS) isGrantedRoot(abs string) bool {
	return slices.Contains(s.policy.FileTool.ReadRoots, abs) || slices.Contains(s.policy.FileTool.WriteRoots, abs)
}

// underProtected reports whether abs is at or beneath any git config/hook surface
// the resolved policy protects (write-denied even inside a writable root).
func (s *sandboxFS) underProtected(abs string) bool {
	for _, p := range s.policy.Git.ProtectedPaths {
		if abs == p || pathUnder(abs, p) {
			return true
		}
	}
	return false
}

// ---- shared, platform-independent primitives ----

// tempName returns an unpredictable temp basename for the atomic write. The
// randomness avoids a griefing pre-create DoS (a predictable name could be
// pre-planted to make O_EXCL creation fail); combined with O_EXCL, a collision
// fails the create rather than clobbering anything.
func tempName() string {
	var b [12]byte
	if _, err := secureRandRead(b[:]); err != nil {
		// crypto/rand should never fail; fall back to a pid-tagged name rather than
		// panicking. Still O_EXCL-guarded at the create site.
		return fmt.Sprintf(".serf-sbtmp-%d", os.Getpid())
	}
	return ".serf-sbtmp-" + hex.EncodeToString(b[:])
}

// containingRoot returns the first root that contains abs, the slash-relative
// path from that root to abs (".": abs is the root itself), and ok.
//
// It first tries a direct lexical match — the common case where a root and abs
// share a spelling — which needs no syscalls. When every root misses lexically it
// retries tolerating an ANCESTOR-spelling difference: the resolver canonicalizes a
// granted root to its real path (e.g. macOS /var → /private/var, or a symlinked
// project/home parent) while the env still spells the target through the symlinked
// ancestor, so `/private/var/…/wt` and `/var/…/wt/file` name the same worktree yet
// mismatch textually. relUnderRealAncestor resolves that mismatch by the roots'
// real paths while keeping the in-root tail LITERAL, so a symlinked ancestor of the
// root is tolerated but a symlink COMPONENT inside the root stays in the tail for
// the fd walk to refuse. A genuinely out-of-root path matches no root's real
// ancestor and stays denied (containment is not weakened).
func containingRoot(roots []string, abs string) (root, rel string, ok bool) {
	abs = filepath.Clean(abs)
	for _, r := range roots {
		if abs == r {
			return r, ".", true
		}
		if pathUnder(abs, r) {
			rl, err := securePathRel(r, abs)
			if err != nil {
				continue
			}
			return r, filepath.ToSlash(rl), true
		}
	}
	for _, r := range roots {
		if rel, ok := relUnderRealAncestor(r, abs); ok {
			return r, rel, true
		}
	}
	return "", "", false
}

// relUnderRealAncestor reports whether abs lies under root when only their ANCESTOR
// spelling differs (a symlinked ancestor of the root), returning the slash-relative
// tail from root to abs. It resolves root to its real path, then walks up abs's
// ancestors for the SHALLOWEST one whose real path equals it — shallowest so the
// tail keeps the most components literal, so an in-root symlink that resolves back
// under the root is left in the tail (the fd walk refuses it) rather than silently
// collapsed. The tail is built by lexical string splitting of abs, never from a
// symlink-resolved path, so no in-root component is ever followed here. A root that
// cannot be resolved (does not exist) contains nothing by real path.
func relUnderRealAncestor(root, abs string) (rel string, ok bool) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", false
	}
	cur := abs
	var tail []string
	found := false
	rel = ""
	for {
		if curReal, rerr := filepath.EvalSymlinks(cur); rerr == nil && curReal == realRoot {
			found = true
			if len(tail) == 0 {
				rel = "."
			} else {
				rel = strings.Join(tail, "/")
			}
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		tail = append([]string{filepath.Base(cur)}, tail...)
		cur = parent
	}
	return rel, found
}

// splitLeaf splits a slash-relative path into its parent directory and leaf. The
// root itself (".") yields an empty leaf, which callers treat as "no leaf".
func splitLeaf(rel string) (dir, leaf string) {
	rel = path.Clean(rel)
	if rel == "." || rel == "" {
		return "", ""
	}
	d, l := path.Split(rel)
	return strings.TrimSuffix(d, "/"), l
}

// dirOrDot returns "." for an empty directory (the root itself), else dir.
func dirOrDot(dir string) string {
	if dir == "" {
		return "."
	}
	return dir
}

// relComponents splits a cleaned slash-relative path into components, returning
// nil for "" or ".".
func relComponents(rel string) []string {
	rel = path.Clean(rel)
	if rel == "." || rel == "" {
		return nil
	}
	return strings.Split(rel, "/")
}

// pathUnder reports whether p is strictly beneath (or equal to) dir. It mirrors
// sandbox.pathUnder, kept local so execenv does not reach into an unexported
// helper of another package.
func pathUnder(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
