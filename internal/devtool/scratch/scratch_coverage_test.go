package scratch

import (
	"testing"
)

// TestOwnerPid covers the ownerPid function.
func TestOwnerPid(t *testing.T) {
	cases := []struct {
		name string
		want int
	}{
		{"prefix.12345", 12345},
		{"prefix.1", 1},
		{"prefix.0", 0},
		{"prefix.", 0},
		{"prefix", 0},
		{"prefix.notapid", 0},
		{"prefix.-1", 0},
		{"prefix.+1", 0},
	}
	for _, c := range cases {
		if got := ownerPid(c.name); got != c.want {
			t.Errorf("ownerPid(%q) = %d, want %d", c.name, got, c.want)
		}
	}
}

// TestOwnerPidLargePid covers the pid-overflow guard (pid > 1<<30 returns 0).
func TestOwnerPidLargePid(t *testing.T) {
	// 1<<30 = 1073741824, so a pid of 1073741825 should return 0.
	if got := ownerPid("prefix.1073741825"); got != 0 {
		t.Fatalf("ownerPid for pid > 1<<30 = %d, want 0", got)
	}
}

// TestValidPrefix covers the validPrefix function.
func TestValidPrefix(t *testing.T) {
	cases := []struct {
		prefix string
		want   bool
	}{
		{"valid", true},
		{"valid-prefix", true},
		{"valid_prefix", true},
		{"", false},
		{"invalid/path", false},
		{"invalid.path", false},
		{"/", false},
		{".", false},
	}
	for _, c := range cases {
		if got := validPrefix(c.prefix); got != c.want {
			t.Errorf("validPrefix(%q) = %v, want %v", c.prefix, got, c.want)
		}
	}
}

// TestReclaimOwnInvalidPrefix covers the invalid-prefix path in ReclaimOwn.
func TestReclaimOwnInvalidPrefix(t *testing.T) {
	// Should not panic and should not reclaim anything.
	ReclaimOwn("", nil)
	ReclaimOwn("invalid/path", nil)
	ReclaimOwn("invalid.path", nil)
}
