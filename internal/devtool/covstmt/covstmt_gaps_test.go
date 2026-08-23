package covstmt

import (
	"strings"
	"testing"
)

// TestStmtCountsReaderStmtCountOverflow covers the strconv.Atoi overflow error
// path for the stmt count field (lines 80-81).
func TestStmtCountsReaderStmtCountOverflow(t *testing.T) {
	// A valid block-line format but with a stmt count that overflows int.
	profile := "example.com/pkg/file.go:1.1,2.2 99999999999999999999999999999999 1\n"
	_, _, err := StmtCountsReader(strings.NewReader(profile))
	if err == nil {
		t.Fatalf("StmtCountsReader with overflowing stmt count should error")
	}
	if !strings.Contains(err.Error(), "parsing stmt count") {
		t.Fatalf("error should mention 'parsing stmt count', got %v", err)
	}
}

// TestStmtCountsReaderCountOverflow covers the strconv.Atoi overflow error
// path for the count field (lines 84-85).
func TestStmtCountsReaderCountOverflow(t *testing.T) {
	// A valid block-line format but with a count that overflows int.
	profile := "example.com/pkg/file.go:1.1,2.2 1 99999999999999999999999999999999\n"
	_, _, err := StmtCountsReader(strings.NewReader(profile))
	if err == nil {
		t.Fatalf("StmtCountsReader with overflowing count should error")
	}
	if !strings.Contains(err.Error(), "parsing count") {
		t.Fatalf("error should mention 'parsing count', got %v", err)
	}
}
