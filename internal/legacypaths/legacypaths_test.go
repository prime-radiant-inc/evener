package legacypaths

import "testing"

func TestRewrite(t *testing.T) {
	tests := []struct {
		name    string
		content string
		old     string
		new     string
		want    string
		wantN   int
	}{
		{
			name:    "prefix followed by path separator",
			content: "/a/.serf/plugins/x",
			old:     "/a/.serf",
			new:     "/a/.evener",
			want:    "/a/.evener/plugins/x",
			wantN:   1,
		},
		{
			name:    "exact match at end of quoted JSON string",
			content: `"installLocation":"/a/.serf"`,
			old:     "/a/.serf",
			new:     "/a/.evener",
			want:    `"installLocation":"/a/.evener"`,
			wantN:   1,
		},
		{
			name:    "match at end of content with no trailing byte",
			content: "/a/.serf",
			old:     "/a/.serf",
			new:     "/a/.evener",
			want:    "/a/.evener",
			wantN:   1,
		},
		{
			name:    "unrelated longer path component is not touched",
			content: "/a/.serfbackup/x",
			old:     "/a/.serf",
			new:     "/a/.evener",
			want:    "/a/.serfbackup/x",
			wantN:   0,
		},
		{
			name:    "multiple occurrences all replaced",
			content: "/a/.serf/one /a/.serf/two",
			old:     "/a/.serf",
			new:     "/a/.evener",
			want:    "/a/.evener/one /a/.evener/two",
			wantN:   2,
		},
		{
			name:    "no occurrences leaves content untouched",
			content: "nothing to see here",
			old:     "/a/.serf",
			new:     "/a/.evener",
			want:    "nothing to see here",
			wantN:   0,
		},
		{
			name:    "empty old is a no-op",
			content: "/a/.serf/x",
			old:     "",
			new:     "/a/.evener",
			want:    "/a/.serf/x",
			wantN:   0,
		},
		{
			name:    "equal old and new is a no-op",
			content: "/a/.serf/x",
			old:     "/a/.serf",
			new:     "/a/.serf",
			want:    "/a/.serf/x",
			wantN:   0,
		},
		{
			name:    "idempotent: rewritten content has nothing left to match",
			content: "/a/.evener/plugins/x",
			old:     "/a/.serf",
			new:     "/a/.evener",
			want:    "/a/.evener/plugins/x",
			wantN:   0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, n := Rewrite(tt.content, tt.old, tt.new)
			if got != tt.want || n != tt.wantN {
				t.Errorf("Rewrite(%q, %q, %q) = (%q, %d), want (%q, %d)",
					tt.content, tt.old, tt.new, got, n, tt.want, tt.wantN)
			}
		})
	}
}
