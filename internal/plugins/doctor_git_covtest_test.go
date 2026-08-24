package plugins

import (
	"context"
	"strings"
	"testing"
)

// TestCovRenderDoctorFindings covers RenderDoctorFindings (doctor.go:360),
// which renders findings as a grouped report with OK/WARN/FAIL counts.
func TestCovRenderDoctorFindings(t *testing.T) {
	// Empty findings: just the summary line.
	out := RenderDoctorFindings(nil)
	if !strings.HasPrefix(out, "0 OK, 0 WARN, 0 FAIL\n") {
		t.Fatalf("empty findings summary: %q", out)
	}

	// Mixed findings with categories, levels, and remediation.
	findings := []DoctorFinding{
		{Level: LevelOK, Category: "registry", Message: "registry ok"},
		{Level: LevelWarn, Category: "marketplace", Message: "marketplace stale", Remediation: "run refresh"},
		{Level: LevelFail, Category: "marketplace", Message: "marketplace broken"},
		{Level: LevelOK, Category: "component", Message: "component ok", Remediation: ""},
		{Level: LevelWarn, Category: "registry", Message: "registry warn"},
	}
	out = RenderDoctorFindings(findings)

	// Summary line counts.
	if !strings.Contains(out, "2 OK, 2 WARN, 1 FAIL\n") {
		t.Fatalf("summary line: %q", out)
	}

	// Categories appear in first-seen order: registry, marketplace, component.
	regIdx := strings.Index(out, "[registry]")
	mktIdx := strings.Index(out, "[marketplace]")
	compIdx := strings.Index(out, "[component]")
	if regIdx < 0 || mktIdx < 0 || compIdx < 0 {
		t.Fatalf("missing category header in:\n%s", out)
	}
	if regIdx >= mktIdx || mktIdx >= compIdx {
		t.Fatalf("categories not in first-seen order in:\n%s", out)
	}

	// Remediation line present for the warn finding.
	if !strings.Contains(out, "-> run refresh") {
		t.Fatalf("missing remediation line in:\n%s", out)
	}

	// No remediation line for the fail finding without one.
	brokenIdx := strings.Index(out, "FAIL marketplace broken")
	if brokenIdx < 0 {
		t.Fatalf("missing fail finding in:\n%s", out)
	}
	afterBroken := out[brokenIdx:]
	if strings.Contains(afterBroken, "-> ") {
		// The next remediation after "marketplace broken" should not exist
		// (the next line is the component header).
		nextComp := strings.Index(afterBroken, "[component]")
		nextRemediation := strings.Index(afterBroken, "-> ")
		if nextRemediation > 0 && (nextComp < 0 || nextRemediation < nextComp) {
			t.Fatalf("unexpected remediation after fail finding:\n%s", afterBroken)
		}
	}
}

// TestCovGitPull covers gitPull (git.go:109). It requires a real git repo,
// so it skips when git is unavailable (matching the existing git_test.go
// convention).
func TestCovGitPull(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	makeGitRepo(t, dir, "file.txt", "hello")

	// A fresh repo with no upstream should error on pull.
	if err := gitPull(context.Background(), dir); err == nil {
		t.Fatalf("gitPull on repo with no upstream should error")
	}
}
