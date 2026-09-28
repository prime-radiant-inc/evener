package evener_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The SAFE-10 architecture audit (#2403) found the canonical architecture prose
// and the sandbox package contract still describing superseded migration stages:
// that no file tool or spawned command consulted a ResolvedPolicy and that the
// --sandbox flag was gated off, and that the repeated-call breaker keyed both of
// its triggers on the raw argument bytes. Both claims are false on current main —
// cmd/evener.provisionSandbox enables enforcement at session start, and
// internal/tool.newDispatchKey computes an exact key over the raw bytes plus a
// semantic key over normalized arguments. These audits pin the corrected
// contracts so the superseded model cannot quietly return.
//
// The oracle is the production code the finding cites, not the prose alone: the
// forbidden wording is exactly what that code contradicts, so a future change to
// the code is what would justify changing these expectations.

// supersededStageClaim matches wording that asserts sandbox enforcement is not
// live. The sandbox package doc must not carry it.
var supersededStageClaim = regexp.MustCompile(`(?i)enforces nothing|gated off until M5|no file tool or spawned command`)

// bothKeysKeyedOnRawClaim matches wording that collapses the breaker's two
// triggers onto the raw argument bytes. Production keeps them distinct.
var bothKeysKeyedOnRawClaim = regexp.MustCompile(`(?i)both keyed on tool name`)

// TestSandboxPackageContractDescribesLiveEnforcement fails while
// agent/sandbox/policy.go's package doc still tells a reader that the package
// carries a policy nothing consults and that the --sandbox flag is gated off. It
// passes once the doc describes the live enforcement contract.
func TestSandboxPackageContractDescribesLiveEnforcement(t *testing.T) {
	t.Parallel()
	doc := safeAuditPackageDoc(t, "agent/sandbox/policy.go")
	if doc == "" {
		t.Fatal("agent/sandbox/policy.go has no package doc comment; the package contract moved and this audit no longer reads it")
	}
	if m := supersededStageClaim.FindString(doc); m != "" {
		t.Errorf("agent/sandbox/policy.go package doc still claims the superseded stage %q; enforcement is live via cmd/evener.provisionSandbox -> execenv.EnableSandbox", m)
	}
	if !strings.Contains(strings.ToLower(doc), "enforc") {
		t.Errorf("agent/sandbox/policy.go package doc does not describe the live enforcement contract:\n%s", doc)
	}
}

// TestArchitectureBreakerDescribesBothLedgerKeys fails while docs/architecture.md's
// repeated-call breaker section still says both triggers are keyed on the raw
// argument bytes. It passes once the section names the normalized failure key and
// still names the raw-bytes repetition key.
func TestArchitectureBreakerDescribesBothLedgerKeys(t *testing.T) {
	t.Parallel()
	section := safeAuditMarkdownSection(t, "docs/architecture.md", "### The repeated-call breaker")
	if m := bothKeysKeyedOnRawClaim.FindString(section); m != "" {
		t.Errorf("docs/architecture.md repeated-call breaker still says %q; internal/tool.newDispatchKey computes an exact key over raw argument bytes and a semantic key over normalized arguments", m)
	}
	if !strings.Contains(section, "normalized") {
		t.Errorf("docs/architecture.md repeated-call breaker does not describe the normalized failure key:\n%s", section)
	}
	if !strings.Contains(section, "raw argument bytes") {
		t.Errorf("docs/architecture.md repeated-call breaker no longer describes the raw-bytes repetition key:\n%s", section)
	}
}

// safeAuditPackageDoc returns the package doc comment at the top of a Go file:
// the leading run of // lines up to the package clause. It fails the test when
// the file cannot be read so a moved path is not read as an empty doc.
func safeAuditPackageDoc(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var b strings.Builder
	for _, line := range strings.Split(string(body), "\n") {
		switch {
		case strings.HasPrefix(line, "//"):
			b.WriteString(strings.TrimPrefix(line, "//"))
			b.WriteByte('\n')
		case strings.TrimSpace(line) == "":
			b.WriteByte('\n')
		default:
			return b.String()
		}
	}
	return b.String()
}

// safeAuditMarkdownSection returns the body of the first section whose trimmed
// line equals heading, up to the next ATX heading at the same or a higher level.
// It fails when the heading is missing, so a renamed section trips the audit
// rather than silently scanning nothing.
func safeAuditMarkdownSection(t *testing.T, path, heading string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	level := safeAuditHeadingLevel(heading)
	if level == 0 {
		t.Fatalf("heading %q is not an ATX heading", heading)
	}
	lines := strings.Split(string(body), "\n")
	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == heading {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("%s no longer carries the heading %q; the audit would otherwise pass vacuously", path, heading)
	}
	var b strings.Builder
	for _, line := range lines[start+1:] {
		if l := safeAuditHeadingLevel(line); l != 0 && l <= level {
			break
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// safeAuditHeadingLevel reports the ATX heading level of a line (1-6), or 0 when
// the line is not a heading.
func safeAuditHeadingLevel(line string) int {
	line = strings.TrimSpace(line)
	n := 0
	for n < len(line) && line[n] == '#' {
		n++
	}
	if n == 0 || n > 6 {
		return 0
	}
	if n == len(line) || line[n] == ' ' {
		return n
	}
	return 0
}
