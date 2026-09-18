package evener_test

import (
	"os"
	"strings"
	"testing"
)

// TestCoverageScriptsUseGoCovstmt guards the #616 consolidation: the two
// coverage scripts that used to carry their own embedded-Python
// statement-counter (a regex parse plus position dedup plus any-hit union, the
// same algorithm internal/devtool/covstmt owns) must count through the Go
// primitive instead. Before this test the drift was invisible — the scripts
// parsed fine, they just parsed DIFFERENTLY over time as either side evolved.
//
// Two properties, both load-bearing:
//
//   - the script invokes `evener-dev/bin dev covstmt`, so its numbers come from
//     the one tested implementation, and
//   - the script runs no Python at all, so a third regex counter cannot quietly
//     reappear next to the Go call.
//
// It reads the two paths by name rather than globbing: a new coverage script
// should get its own decision, not inherit (or dodge) this one by accident.
func TestCoverageScriptsUseGoCovstmt(t *testing.T) {
	t.Parallel()
	for _, path := range []string{
		"scripts/coverage/coverage-gaps.sh",
		"scripts/coverage/e2e-cover.sh",
	} {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		src := string(body)
		if !strings.Contains(src, "evener-dev/bin dev covstmt") {
			t.Errorf("%s does not count through the Go covstmt primitive "+
				"(`evener-dev/bin dev covstmt`); its statement counting must not "+
				"be a second implementation free to drift from "+
				"internal/devtool/covstmt", path)
		}
		if strings.Contains(src, "python3") {
			t.Errorf("%s still runs python3; its statement-counter was supposed to "+
				"be deleted in favor of the Go covstmt primitive", path)
		}
	}
}
