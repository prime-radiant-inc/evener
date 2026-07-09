package sandbox

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// AsDenied reports whether err (or anything it wraps) is a sandbox DeniedError,
// returning the typed value so callers — notably M7's escalation seam — never
// type-switch on the concrete type. It is the single predicate the session layer
// uses to recognize a sandbox denial in a tool result.
func AsDenied(err error) (*DeniedError, bool) {
	var d *DeniedError
	if errors.As(err, &d) {
		return d, true
	}
	return nil, false
}

// DeniedError is the single typed error every sandbox denial surfaces to the
// model, across both enforcement layers. M2's in-process file tools and M3/M6's
// kernel-wrapped spawned processes both return it at a denial; M7's in-UI
// escalation consumes it to build the approval card. Defining it once here (in
// the backend-independent policy package) keeps the three milestones from each
// declaring or widening their own shape.
//
// It carries no live behavior in M1 — no code path produces one yet.
type DeniedError struct {
	// Mode is the session's sandbox mode, for the legible message.
	Mode Mode
	// Tool is the tool whose call was denied (e.g. "write_file", "shell").
	Tool string
	// Path is the offending filesystem path, when the denial is path-scoped
	// (file-tool denials always set it; a shell denial may not know it). Callers
	// that log a DeniedError should log Redacted(), not Path.
	Path string
	// Reason is a short human-legible explanation ("outside the writable roots",
	// "credential path masked", …).
	Reason string

	// Sensitive marks a denial whose PATH must never be echoed — even by basename —
	// in the message or the audit log. Callers set it for denylist / credential /
	// pseudo-fs denials, where the basename itself (id_rsa, credentials, environ)
	// leaks which secret was probed. The zero value (false) keeps the basename-
	// showing behavior, so existing callers are unaffected. When true, Error()
	// omits the offending path and Redacted() returns the "<denied>" token.
	// M2/M3 set Sensitive=true at their denylist/credential/pseudo-fs denial sites.
	Sensitive bool

	// Command is the full shell command, set ONLY for shell/kernel denials (empty
	// for file-tool denials). M7's shell approval card renders it.
	Command string
	// OutputSoFar is whatever the denied command emitted before it was stopped,
	// set ONLY for shell/kernel denials. M7 shows it so a human approving a
	// re-run understands the command already partially executed.
	OutputSoFar string

	// ReasonKind classifies WHY the denial occurred, so a caller can decide
	// programmatically WITHOUT matching the display text of Reason. M7's escalation
	// eligibility uses it: only a CONTAINMENT denial (outside the roots) can be
	// cured by a per-invocation single-leaf grant, so only those raise an approval
	// card — a masked/git-protected/symlinked/escape denial re-denies deterministically
	// on re-run and must stay final. The zero value (DenialUnspecified) is NOT
	// curable, so a denial constructed without a kind fails closed (stays final).
	ReasonKind DenialReason
}

// DenialReason classifies a sandbox denial by cause, independent of its display
// text, so eligibility decisions never string-match a human-facing message.
type DenialReason int

const (
	// DenialUnspecified is the zero value: an unclassified denial. It is NOT curable,
	// so a hand-built or legacy DeniedError fails closed (stays final).
	DenialUnspecified DenialReason = iota
	// DenialOutsideReadRoots: a read outside the readable roots (restricted mode).
	// A per-invocation grant of that one path CAN cure it.
	DenialOutsideReadRoots
	// DenialOutsideWriteRoots: a write outside the writable roots. Curable.
	DenialOutsideWriteRoots
	// DenialWritesDisabled: no writable roots in this mode (read-only). Curable.
	DenialWritesDisabled
	// DenialMasked: a secrets/pseudo-fs denylist hit (also Sensitive). NOT curable —
	// a grant must never relax the secrets floor.
	DenialMasked
	// DenialGitProtected: a git config/hook surface write. NOT curable — the grant
	// widens containment only, so the protected re-check re-denies on re-run.
	DenialGitProtected
	// DenialSymlink: a refused symlinked / non-directory path component. NOT curable —
	// the grant opens with symlinks refused, so it re-denies.
	DenialSymlink
	// DenialEscape: a path that resolves outside the sandbox root. NOT curable.
	DenialEscape
	// DenialRootTarget: an attempt to operate on a sandbox root itself. NOT curable.
	DenialRootTarget
)

// Curable reports whether a per-invocation single-leaf grant could actually let this
// denial succeed on re-run. ONLY the containment denials (outside the roots) qualify;
// masking, git-protection, symlink refusal, root-escape, and the unclassified zero
// value all re-deny deterministically, so escalating them would show the human an
// approval that the model still sees as a denial. Fail-closed for unknown kinds.
func (r DenialReason) Curable() bool {
	switch r {
	case DenialOutsideReadRoots, DenialOutsideWriteRoots, DenialWritesDisabled:
		return true
	default:
		return false
	}
}

// Error implements error with a message safe to return to the model: it names
// the mode, the tool, and the reason, and includes the path only by basename so
// a denylisted absolute path is not echoed back in full. A Sensitive denial
// omits the path entirely (not even the basename), showing the "<denied>" token
// so the message still signals that a path was refused.
func (e *DeniedError) Error() string {
	var b strings.Builder
	b.WriteString("sandbox: ")
	if e.Tool != "" {
		b.WriteString(e.Tool)
		b.WriteString(" ")
	}
	b.WriteString("denied")
	if e.Path != "" {
		if e.Sensitive {
			b.WriteString(" (<denied>)")
		} else {
			fmt.Fprintf(&b, " (%s)", filepath.Base(e.Path))
		}
	}
	if e.Reason != "" {
		b.WriteString(": ")
		b.WriteString(e.Reason)
	}
	if e.Mode != ModeOff {
		// Name the MODE, not a CLI flag: a per-delegate box is set by the delegate
		// tool, not --sandbox, so "[--sandbox X]" would misdescribe how it arose.
		fmt.Fprintf(&b, " [sandbox mode: %s]", e.Mode)
	}
	return b.String()
}

// Redacted returns the path with sensitive components elided for audit logging:
// a home-anchored or otherwise absolute path is reduced to its basename so the
// audit trail records that a denial happened without persisting the full secret
// path. An in-tree relative path is returned as-is. A Sensitive denial returns
// the "<denied>" token instead — even the basename (id_rsa, credentials) would
// leak which secret was probed into the audit log.
func (e *DeniedError) Redacted() string {
	if e.Path == "" {
		return ""
	}
	if e.Sensitive {
		return "<denied>"
	}
	if filepath.IsAbs(e.Path) {
		return filepath.Base(e.Path)
	}
	return e.Path
}
