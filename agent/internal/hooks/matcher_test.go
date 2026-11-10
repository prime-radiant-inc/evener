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

func TestMatchTarget_CommaAndSpaceSeparator(t *testing.T) {
	cases := []struct {
		matcher, target string
		want            bool
	}{
		{"Edit, Write", "Write", true},
		{"Edit,Write", "Edit", true},
		{"Edit, Write", "Read", false},
		{" Edit , Write ", "Write", true},
	}
	for _, c := range cases {
		got, err := matchTarget(c.matcher, c.target)
		if err != nil {
			t.Fatalf("matchTarget(%q,%q): %v", c.matcher, c.target, err)
		}
		if got != c.want {
			t.Errorf("matchTarget(%q,%q) = %v, want %v", c.matcher, c.target, got, c.want)
		}
	}
}

func TestMatchTarget_HyphenatedNameIsExact(t *testing.T) {
	if got, err := matchTarget("code-reviewer", "code-reviewer"); err != nil || !got {
		t.Errorf("code-reviewer must match itself, got %v err %v", got, err)
	}
	if got, _ := matchTarget("code-reviewer", "code-reviewers"); got {
		t.Errorf("code-reviewer must not match code-reviewers")
	}
}
