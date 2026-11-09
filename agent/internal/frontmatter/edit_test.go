package frontmatter

import "testing"

func TestSetField(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, raw, line, want string }{
		{"no frontmatter gains a block", "# Title\nbody\n", "updated: 2026-10-08\n",
			"---\nupdated: 2026-10-08\n---\n# Title\nbody\n"},
		{"appends a missing key, keeps every other byte", "---\ndescription: d\nodd:   spacing  \n---\nbody", "by: s1\n",
			"---\ndescription: d\nodd:   spacing  \nby: s1\n---\nbody"},
		{"replaces an existing key and its continuation lines", "---\nupdated: |\n  old\n  older\ntags:\n- a\n---\nb\n", "updated: 2026-10-08\n",
			"---\nupdated: 2026-10-08\ntags:\n- a\n---\nb\n"},
		{"does not match a longer key", "---\nbyline: x\n---\n", "by: s1\n",
			"---\nbyline: x\nby: s1\n---\n"},
		{"empty block", "---\n---\nbody\n", "by: s1\n", "---\nby: s1\n---\nbody\n"},
		{"replaces a block scalar with blank lines inside it", "---\nupdated: |\n  old\n\n  older\ntags: [a]\n---\nb\n", "updated: 2026-10-08\n",
			"---\nupdated: 2026-10-08\ntags: [a]\n---\nb\n"},
		{"keeps a blank line that ends the replaced value", "---\nnote: |\n  x\n\nkeep: 1\n---\n", "note: y\n",
			"---\nnote: y\n\nkeep: 1\n---\n"},
		{"a value ending in --- does not close the block", "---\ndescription: a---\nb: 1\n---\nbody\n", "by: s1\n",
			"---\ndescription: a---\nb: 1\nby: s1\n---\nbody\n"},
		{"replaces a double-quoted key", "---\n\"description\": \"\"\nb: 1\n---\nx\n", "description: d\n",
			"---\ndescription: d\nb: 1\n---\nx\n"},
		{"replaces a single-quoted key", "---\n'description': ''\n---\nx\n", "description: d\n",
			"---\ndescription: d\n---\nx\n"},
		{"replaces a key with a space before its colon", "---\ndescription : \"\"\n---\nx\n", "description: d\n",
			"---\ndescription: d\n---\nx\n"},
		{"replaces a flow value spread over lines", "---\ntags: [a,\nb]\nk: 1\n---\n", "tags: [c]\n",
			"---\ntags: [c]\nk: 1\n---\n"},
		{"keeps a comment that ends the replaced value", "---\nnote: x\n# about keep\nkeep: 1\n---\n", "note: y\n",
			"---\nnote: y\n# about keep\nkeep: 1\n---\n"},
		{"leaves a flow mapping as it is", "---\n{description: d}\n---\nx\n", "by: s1\n",
			"---\n{description: d}\n---\nx\n"},
		{"leaves a block ended by ... as it is", "---\ndescription: d\n...\n---\nx\n", "by: s1\n",
			"---\ndescription: d\n...\n---\nx\n"},
		{"leaves a block ended by ... and a comment as it is", "---\ndescription: d\n... # end\n---\nx\n", "by: s1\n",
			"---\ndescription: d\n... # end\n---\nx\n"},
		{"a block that does not parse gains the line", "---\ndescription: a: b\n---\nx\n", "by: s1\n",
			"---\ndescription: a: b\nby: s1\n---\nx\n"},
		{"CRLF frontmatter is edited in place, with \\n line endings", "---\r\ndescription: d\r\n---\r\nbody\r\n", "by: s1\n",
			"---\ndescription: d\nby: s1\n---\nbody\n"},
		{"carriage-return-only frontmatter is edited in place", "---\rdescription: d\r---\rbody\r", "by: s1\n",
			"---\ndescription: d\nby: s1\n---\nbody\n"},
		{"a key-like line inside a block scalar is not a key", "---\nnote: |\n  description: no\ndescription: old\n---\n", "description: new\n",
			"---\nnote: |\n  description: no\ndescription: new\n---\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := string(SetField([]byte(tc.raw), tc.line)); got != tc.want {
				t.Fatalf("got %q\nwant %q", got, tc.want)
			}
		})
	}
}
