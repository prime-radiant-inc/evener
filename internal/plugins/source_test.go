package plugins

import (
	"encoding/json"
	"testing"
)

func TestSource_UnmarshalObjectForms(t *testing.T) {
	cases := map[string]Source{
		`{"source":"github","repo":"o/r"}`:                          {Kind: SourceGitHub, Repo: "o/r"},
		`{"source":"url","url":"https://x/y.git"}`:                   {Kind: SourceURL, URL: "https://x/y.git"},
		`{"source":"directory","path":"/abs"}`:                       {Kind: SourceDirectory, Path: "/abs"},
		`{"source":"git-subdir","url":"https://x.git","path":"a/b"}`: {Kind: SourceGitSubdir, URL: "https://x.git", Path: "a/b"},
		`{"source":"git","url":"https://x/y.git"}`:                   {Kind: SourceURL, URL: "https://x/y.git"}, // legacy alias
	}
	for in, want := range cases {
		var s Source
		if err := json.Unmarshal([]byte(in), &s); err != nil {
			t.Fatalf("Unmarshal(%s): %v", in, err)
		}
		if s != want {
			t.Errorf("Unmarshal(%s) = %+v, want %+v", in, s, want)
		}
	}
}

func TestSource_UnmarshalStringForm(t *testing.T) {
	var s Source
	if err := json.Unmarshal([]byte(`"./plugins/widget"`), &s); err != nil {
		t.Fatalf("Unmarshal string: %v", err)
	}
	if s.Kind != SourceDirectory || s.Path != "./plugins/widget" || !s.Rel {
		t.Fatalf("string source = %+v, want directory/rel", s)
	}
}

func TestSource_MarshalNeverWritesGit(t *testing.T) {
	b, _ := json.Marshal(Source{Kind: SourceURL, URL: "https://x/y.git"})
	if got := string(b); got == `{"source":"git","url":"https://x/y.git"}` || !json.Valid(b) {
		t.Fatalf("marshalled = %s", got)
	}
	var round Source
	json.Unmarshal(b, &round)
	if round.Kind != SourceURL {
		t.Fatalf("round-trip kind = %q", round.Kind)
	}
}
