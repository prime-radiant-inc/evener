package hooks

import "testing"

func TestMatchTarget(t *testing.T) {
	cases := []struct {
		matcher string
		target  string
		want    bool
		wantErr bool
	}{
		{"", "Bash", true, false},
		{"*", "anything", true, false},
		{"Bash", "Bash", true, false},
		{"Bash", "BashOutput", false, false}, // exact mode — the headline fix
		{"Edit|Write|MultiEdit", "Write", true, false},
		{"Edit|Write", "Read", false, false},
		{"mcp__memory__.*", "mcp__memory__search", true, false}, // regex (has '.')
		{"mcp__memory", "mcp__memory__search", false, false},    // exact, no substring
		{"mcp__.*__write.*", "mcp__fs__write_file", true, false},
		{"(", "Bash", false, true}, // invalid regex => no match + error
	}
	for _, c := range cases {
		got, err := matchTarget(c.matcher, c.target)
		if got != c.want || (err != nil) != c.wantErr {
			t.Errorf("matchTarget(%q,%q)=%v,err=%v want %v,err=%v", c.matcher, c.target, got, err, c.want, c.wantErr)
		}
	}
}
