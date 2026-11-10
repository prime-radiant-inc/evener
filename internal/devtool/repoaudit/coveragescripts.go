package repoaudit

import (
	"os"
	"strings"
)

// coverageScriptsUseGoCovstmt is the static side of the consolidation
// guard: neither coverage script may count statements with its own regex, and
// neither may run python3. Per the testing doc, "that every consumer actually
// goes through the library is a static audit's job, not a reason to re-run
// consumers under sabotage" — so this stays a source audit rather than a
// fake-`go` harness (faking the toolchain to test a script is banned outright).
// It strips full-line comments so a comment cannot satisfy or defeat it. The
// root TestCoverageGapsScriptReportsFixture runs the real script.
//
// It reads the two paths by name rather than globbing: a new coverage script
// should get its own decision, not inherit (or dodge) this one by accident.
func coverageScriptsUseGoCovstmt(a *audit) {
	for _, path := range []string{
		"scripts/coverage/coverage-gaps.sh",
		"scripts/coverage/e2e-cover.sh",
	} {
		body, err := os.ReadFile(path)
		if err != nil {
			a.Fatalf("read %s: %v", path, err)
		}
		var code strings.Builder
		for line := range strings.SplitSeq(string(body), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			code.WriteString(line)
			code.WriteByte('\n')
		}
		src := code.String()
		if !strings.Contains(src, "evener-dev/bin dev covstmt") {
			a.Errorf("%s does not count through the Go covstmt primitive "+
				"(`evener-dev/bin dev covstmt`); its statement counting must not "+
				"be a second implementation free to drift from "+
				"internal/devtool/covstmt", path)
		}
		if strings.Contains(src, "python3") {
			a.Errorf("%s still runs python3; its statement-counter was supposed to "+
				"be deleted in favor of the Go covstmt primitive", path)
		}
	}
}
