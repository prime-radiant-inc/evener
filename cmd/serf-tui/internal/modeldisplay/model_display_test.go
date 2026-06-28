package modeldisplay

import (
	"testing"
)

func TestAbbreviatePath(t *testing.T) {
	// AbbreviatePath recognizes a home directory by the literal "/home/<user>/"
	// prefix (it does not consult $HOME or os/user), so a fixed /home path keeps
	// these cases deterministic regardless of who runs the test.
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
			p:      "/home/someuser/projects/myapp/main.go",
			maxLen: 30,
			want:   "~/projects/myapp/main.go",
		},
		{
			name:   "home_prefix_replaced_then_truncated",
			p:      "/home/someuser/very/long/path/to/project/file.txt",
			maxLen: 20,
			want:   "~/very/lo…t/file.txt",
		},
		{
			// No slash after the username: the IndexByte == -1 branch fires,
			// so the /home prefix is NOT replaced with ~. maxLen is below the
			// path length so it still reaches that branch (rather than the
			// short-path early return) and then middle-truncates.
			name:   "home_no_subdir_not_replaced",
			p:      "/home/verylongusername",
			maxLen: 15,
			want:   "/home/v…sername",
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
			// maxLen (35) exceeds the path's byte length (31), so this returns
			// via the len(p) <= maxLen short-circuit: multibyte runes pass
			// through untouched and truncation is never reached.
			name:   "unicode_path_under_maxLen_unchanged",
			p:      "/tmp/日本語/ファイル.txt",
			maxLen: 35,
			want:   "/tmp/日本語/ファイル.txt",
		},
		{
			// maxLen (15) is below the byte length (31), so middle-truncation
			// fires. AbbreviatePath slices on BYTE offsets, so the head cut
			// (p[:7]) lands in the middle of the 3-byte "日" rune, emitting the
			// dangling bytes "\xe6\x97". This pins the current byte-truncation
			// behaviour: the head is split mid-rune (invalid UTF-8) while the
			// tail happens to start on a rune boundary ("ル.txt").
			name:   "unicode_path_truncated_splits_rune",
			p:      "/tmp/日本語/ファイル.txt",
			maxLen: 15,
			want:   "/tmp/\xe6\x97…ル.txt",
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
		// Date suffix containing '9' — exercises the r > '9' upper digit bound.
		{"openai/gpt-5-20190901", "gpt-5"},
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
