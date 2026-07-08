package sandbox

import (
	"fmt"
	"path/filepath"
	"strings"
)

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

	// Command is the full shell command, set ONLY for shell/kernel denials (empty
	// for file-tool denials). M7's shell approval card renders it.
	Command string
	// OutputSoFar is whatever the denied command emitted before it was stopped,
	// set ONLY for shell/kernel denials. M7 shows it so a human approving a
	// re-run understands the command already partially executed.
	OutputSoFar string
}

// Error implements error with a message safe to return to the model: it names
// the mode, the tool, and the reason, and includes the path only by basename so
// a denylisted absolute path is not echoed back in full.
func (e *DeniedError) Error() string {
	var b strings.Builder
	b.WriteString("sandbox: ")
	if e.Tool != "" {
		b.WriteString(e.Tool)
		b.WriteString(" ")
	}
	b.WriteString("denied")
	if e.Path != "" {
		fmt.Fprintf(&b, " (%s)", filepath.Base(e.Path))
	}
	if e.Reason != "" {
		b.WriteString(": ")
		b.WriteString(e.Reason)
	}
	if e.Mode != ModeOff {
		fmt.Fprintf(&b, " [--sandbox %s]", e.Mode)
	}
	return b.String()
}

// Redacted returns the path with sensitive components elided for audit logging:
// a home-anchored or otherwise absolute path is reduced to its basename so the
// audit trail records that a denial happened without persisting the full secret
// path. An in-tree relative path is returned as-is.
func (e *DeniedError) Redacted() string {
	if e.Path == "" {
		return ""
	}
	if filepath.IsAbs(e.Path) {
		return filepath.Base(e.Path)
	}
	return e.Path
}
