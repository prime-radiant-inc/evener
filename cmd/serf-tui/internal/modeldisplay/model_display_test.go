package modeldisplay

import (
	"os/user"
	"testing"
)

func TestAbbreviatePath(t *testing.T) {
	usr, err := user.Current()
	var homeDir string
	if err == nil {
		homeDir = usr.HomeDir
	}

	tests := []struct {
		name   string
		p      string
		maxLen int
		want   string
	}{
		{
			name:   "shorter_than_maxLen",
			p:      "/usr/local/bin",
			maxLen: 20,
			want:   "/usr/local/bin",
		},
		{
			name:   "exactly_maxLen",
			p:      "/usr/local/bin/foo",
			maxLen: 18,
			want:   "/usr/local/bin/foo",
		},
		{
			name:   "home_prefix_replaced",
			p:      homeDir + "/projects/myapp/main.go",
			maxLen: 30,
			want:   "~/projects/myapp/main.go",
		},
		{
			name:   "home_prefix_replaced_then_truncated",
			p:      homeDir + "/very/long/path/to/project/file.txt",
			maxLen: 20,
			want:   "~/very/lo…t/file.txt",
		},
		{
			name:   "home_no_subdir_unchanged",
			p:      "/home/someuser",
			maxLen: 50,
			want:   "/home/someuser",
		},
		{
			name:   "empty_path",
			p:      "",
			maxLen: 10,
			want:   "",
		},
		{
			name:   "maxLen_zero",
			p:      "/anything",
			maxLen: 0,
			want:   "/anything",
		},
		{
			name:   "unicode_path",
			p:      "/tmp/日本語/ファイル.txt",
			maxLen: 35,
			want:   "/tmp/日本語/ファイル.txt",
		},
		{
			name:   "long_path_no_home_middle_truncated",
			p:      "/var/log/very/long/path/to/some/file.log",
			maxLen: 20,
			want:   "/var/log/…e/file.log",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := AbbreviatePath(tc.p, tc.maxLen)
			if got != tc.want {
				t.Errorf("AbbreviatePath(%q, %d) = %q, want %q", tc.p, tc.maxLen, got, tc.want)
			}
		})
	}
}

func TestAbbreviateModel(t *testing.T) {
	tests := []struct {
		id   string
		want string
	}{
		// Known provider prefix — stripped generically.
		{"openai/gpt-5", "gpt-5"},
		// Custom instance name — same behaviour; instance stripped, model preserved.
		{"work/gpt-5", "gpt-5"},
		// Meta-provider: only the first segment is stripped.
		{"openrouter/anthropic/claude-opus", "anthropic/claude-opus"},
		// Date suffix stripped after instance prefix.
		{"openai/gpt-5-20260101", "gpt-5"},
		// Custom instance + date suffix.
		{"work/gpt-5-20260101", "gpt-5"},
		// No slash — bare model, returned unchanged.
		{"bare-model", "bare-model"},
		// Empty string.
		{"", ""},
	}
	for _, tc := range tests {
		got := AbbreviateModel(tc.id)
		if got != tc.want {
			t.Errorf("AbbreviateModel(%q) = %q, want %q", tc.id, got, tc.want)
		}
	}
}
