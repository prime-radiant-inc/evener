package agent

import (
	"testing"
	"testing/fstest"
)

func TestResolveRolePrompt(t *testing.T) {
	t.Parallel()
	agents := fstest.MapFS{
		"scout.md":  {Data: []byte("---\nname: scout\n---\n\n  Scout body.  \n")},
		"empty.md":  {Data: []byte("---\nname: empty\n---\n")},
		"broken.md": {Data: []byte("---\n: bad: yaml: [unclosed\n---\nbody\n")},
	}
	cases := []struct {
		name       string
		override   string
		agent      string
		wantBody   string
		wantSource *promptSource
	}{
		{"override wins, trimmed", "  Override body. ", "scout", "Override body.", &promptSource{Label: "config:role_prompt_override", Size: len("Override body.")}},
		{"bundled body without frontmatter", "", "scout", "Scout body.", &promptSource{Label: "agent:scout", Size: len("Scout body.")}},
		{"an empty body keeps its source", "", "empty", "", &promptSource{Label: "agent:empty", Size: 0}},
		{"unparseable definition", "", "broken", "", nil},
		{"no definition", "", "missing", "", nil},
	}
	for _, tc := range cases {
		body, source := resolveRolePrompt(tc.override, tc.agent, agents)
		if body != tc.wantBody {
			t.Errorf("%s: body = %q, want %q", tc.name, body, tc.wantBody)
		}
		switch {
		case tc.wantSource == nil && source != nil:
			t.Errorf("%s: source = %+v, want none", tc.name, *source)
		case tc.wantSource != nil && (source == nil || *source != *tc.wantSource):
			t.Errorf("%s: source = %v, want %+v", tc.name, source, *tc.wantSource)
		}
	}
}
