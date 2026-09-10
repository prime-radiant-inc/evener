package agent

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Round-15 ledger tests: goalArgScalar truncates fingerprint values at 512
// chars. The truncation must be rune-safe — byte slicing can split a
// multi-byte UTF-8 sequence and emit an invalid string (replacement chars
// downstream, unstable fingerprints).

// TestFixWave15_GoalArgScalarRuneSafeTruncation pins the round-15 LOW: a
// 600-rune CJK value truncates to exactly 512 runes of valid UTF-8 with no
// replacement chars. Before the fix s[:512] split a 3-byte rune at the
// boundary and produced invalid UTF-8.
func TestFixWave15_GoalArgScalarRuneSafeTruncation(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("表", 600)
	got := goalArgScalar(long)
	if !utf8.ValidString(got) {
		t.Fatalf("goalArgScalar(CJK×600) is not valid UTF-8 (byte split mid-rune): %.40q...", got)
	}
	if n := len([]rune(got)); n != 512 {
		t.Fatalf("goalArgScalar runes = %d, want exactly 512", n)
	}
	if strings.ContainsRune(got, '�') {
		t.Fatal("goalArgScalar must not emit replacement chars")
	}
	// Mixed-width value: emoji (4-byte) plus ASCII.
	mixed := strings.Repeat("🚀", 200) + strings.Repeat("x", 200)
	got = goalArgScalar(mixed)
	if !utf8.ValidString(got) {
		t.Fatalf("goalArgScalar(mixed) is not valid UTF-8: %.40q...", got)
	}
	if n := len([]rune(got)); n > 512 {
		t.Fatalf("goalArgScalar runes = %d, want ≤512", n)
	}
	// Short values pass through untouched.
	if got := goalArgScalar("plain value"); got != "plain value" {
		t.Fatalf("goalArgScalar = %q, want the value unchanged", got)
	}
}
