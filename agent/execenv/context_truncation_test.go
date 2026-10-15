package execenv

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// ctxOmissionPattern captures the count a context-truncation marker reports.
var ctxOmissionPattern = regexp.MustCompile(`^\[\.\.\. (\d+) bytes omitted \.\.\.\]`)

// TestTruncateContextLineReportsTheBytesItRemoves pins the omission count to
// the bytes the marker actually replaced. The marker's own length depends on
// the digit width of the count it reports, so a count computed from a
// one-digit placeholder while the tail is sliced with the final marker's
// length removes one more byte than it admits: a 150-byte line trimmed to 100
// reported 75 omitted but dropped 76.
func TestTruncateContextLineReportsTheBytesItRemoves(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ n, keep int }{
		{150, 100},
		{128, 64},
		{1000, 100},
		{1500, 200},
		{20000, 32},
		{1000, 20}, // no room for any tail: the marker alone, disclosing all of it
	} {
		text := strings.Repeat("x", tc.n)
		out := truncateContextLine(text, tc.keep)
		m := ctxOmissionPattern.FindStringSubmatch(out)
		if m == nil {
			t.Fatalf("truncateContextLine(%d-byte line, keep %d) = %q, want an omission marker", tc.n, tc.keep, out)
		}
		reported, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("truncateContextLine(%d-byte line, keep %d): unparsable count %q", tc.n, tc.keep, m[1])
		}
		removed := tc.n - (len(out) - len(m[0]))
		if reported != removed {
			t.Fatalf("truncateContextLine(%d-byte line, keep %d) reports %d bytes omitted but removes %d (%q)", tc.n, tc.keep, reported, removed, out)
		}
	}
}
