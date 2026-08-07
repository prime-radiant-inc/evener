package execenv

import (
	"path/filepath"

	"primeradiant.com/serf/agent/sandbox"
)

// AuditRecord is one redacted sandbox-denial audit line. Its JSON keys are
// snake_case (the wire contract enforced by serf-namingcheck). The Path is
// ALREADY redacted (a basename, or the "<denied>" token for a masked/secret
// surface); the raw path and file contents never appear here.
type AuditRecord struct {
	Mode   string `json:"mode"`
	Tool   string `json:"tool"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// auditSink receives one AuditRecord per file-tool denial. It defaults to nil (a
// no-op) so unit runs stay pristine — nothing is logged unless a caller opts in.
// Production wiring (M5, when --sandbox goes live) points it at the real logger;
// tests set it to capture and assert the redacted record. It is a plain package
// var rather than a per-environment field so the enforcement primitives in
// securepath.go can emit without threading a logger through every call.
var auditSink func(AuditRecord)

// auditDenial emits one redacted audit record for a denial, if a sink is set.
func auditDenial(mode sandbox.Mode, tool, denyPath, reason string) {
	sink := auditSink
	if sink == nil {
		return
	}
	sink(AuditRecord{Mode: mode.String(), Tool: tool, Path: redactAuditPath(denyPath, reason), Reason: reason})
}

// redactAuditPath reduces a denied path to a form safe to persist: a masked
// (secret/pseudo-fs) or git-protected surface collapses to "<denied>" (even its
// basename can be sensitive, e.g. id_rsa); any other absolute path keeps only its
// basename; an in-tree relative path is returned as-is.
func redactAuditPath(denyPath, reason string) string {
	if denyPath == "" {
		return ""
	}
	if reason == denyReasonMasked || reason == denyReasonProtected {
		return "<denied>"
	}
	if filepath.IsAbs(denyPath) {
		return filepath.Base(denyPath)
	}
	return denyPath
}
