//go:build serffuzz

package doctor

import (
	"strings"
	"testing"
)

// FuzzParseRunbook drives the runbook parser, which was entirely unreached by
// fuzzing despite being a decode seam twice over: markdown structure on the
// outside, YAML on the inside, over a document a human (or a model) wrote.
//
// The parser's own doc states it fails LOUD rather than degrading, because a
// malformed runbook is a defect in the doctor's own machinery and must never
// silently become "no findings". These oracles hold it to that:
//
//   - Error and result are exclusive. A failed parse returns the zero Runbook,
//     never a half-built one a caller might run.
//   - A successful parse is audit-executable: at least one check or one manual
//     step, which is exactly the condition the parser promises to reject.
//   - Every parsed check carries a title, because normalizeCheck rejects a
//     titleless one and auditSignature keys on it.
//   - Category+title pairs are unique. addCheck refuses duplicates precisely
//     because two checks sharing that pair collapse into a single Finding, so a
//     survivor set containing a duplicate means a check silently disappeared.
//   - Parsing is deterministic and the name is passed through untouched.
//
// Pure function over bytes: no filesystem, no subprocess.
func FuzzParseRunbook(f *testing.F) {
	f.Add("rb", "## CLASSIFY\n- judge this by hand\n")
	f.Add("rb", "## CLASSIFY\n```\naudit:\n  - title: t\n    severity: warn\n    metric: apilog.errors\n    gt: 0\n```\n")
	f.Add("rb", "```\naudit:\n  - title: outside\n```\n")
	f.Add("rb", "## CLASSIFY\n- one\n  wrapped continuation\n- two\n")
	f.Add("rb", "")
	f.Add("rb", "## CLASSIFY\n```\nnot: yaml: at: all\n```\n")
	f.Add("rb", "## classify\n- case insensitive heading\n")
	f.Add("rb", "```\n```\n```\n")
	f.Add("rb", "## CLASSIFY\n- \n")

	f.Fuzz(func(t *testing.T, name, content string) {
		if len(content) > 32<<10 || len(name) > 512 {
			t.Skip()
		}

		rb, err := ParseRunbook(name, []byte(content))
		if err != nil {
			if rb.Name != "" || len(rb.Checks) != 0 || len(rb.ManualSteps) != 0 {
				t.Fatalf("failed parse returned a non-zero runbook %+v alongside %v", rb, err)
			}
			return
		}

		if rb.Name != name {
			t.Fatalf("parsed runbook name = %q, want %q", rb.Name, name)
		}
		if len(rb.Checks) == 0 && len(rb.ManualSteps) == 0 {
			t.Fatal("parse succeeded with neither a check nor a manual step, which it promises to reject")
		}

		seen := map[string]bool{}
		for _, check := range rb.Checks {
			if strings.TrimSpace(check.Title) == "" {
				t.Fatalf("parsed a check with an empty title: %+v", check)
			}
			key := check.Category + "\x00" + check.Title
			if seen[key] {
				t.Fatalf("parsed duplicate category+title %q/%q; the two collapse into one Finding",
					check.Category, check.Title)
			}
			seen[key] = true
		}

		again, againErr := ParseRunbook(name, []byte(content))
		if againErr != nil {
			t.Fatalf("second parse of the same document failed: %v", againErr)
		}
		if len(again.Checks) != len(rb.Checks) || len(again.ManualSteps) != len(rb.ManualSteps) {
			t.Fatalf("parse is not deterministic: %d/%d checks, %d/%d steps",
				len(rb.Checks), len(again.Checks), len(rb.ManualSteps), len(again.ManualSteps))
		}
	})
}
