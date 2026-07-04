package plugins

import (
	"bytes"
	"encoding/json"
	"fmt"
)

type SourceKind string

const (
	SourceDirectory SourceKind = "directory"
	SourceGitHub    SourceKind = "github"
	SourceURL       SourceKind = "url"
	SourceGitSubdir SourceKind = "git-subdir"
)

// Source is a marketplace-container or plugin source. Field meaning depends on
// Kind: Repo (github), URL (url/git-subdir), Path (directory local path, or
// git-subdir subdirectory, or the bare-string relative path). Rel marks the
// Claude "./subdir" bare-string plugin-source form (relative to marketplace root).
type Source struct {
	Kind SourceKind
	Repo string
	URL  string
	Path string
	Ref  string
	Sha  string
	Rel  bool
}

// sourceJSON is the on-disk object shape.
type sourceJSON struct {
	Source string `json:"source"`
	Repo   string `json:"repo,omitempty"`
	URL    string `json:"url,omitempty"`
	Path   string `json:"path,omitempty"`
	Ref    string `json:"ref,omitempty"`
	Sha    string `json:"sha,omitempty"`
}

func (s *Source) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '"' { // bare string form: "./subdir"
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return err
		}
		*s = Source{Kind: SourceDirectory, Path: str, Rel: true}
		return nil
	}
	var j sourceJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return err
	}
	kind := SourceKind(j.Source)
	if kind == "git" { // read-only legacy alias
		kind = SourceURL
	}
	switch kind {
	case SourceDirectory, SourceGitHub, SourceURL, SourceGitSubdir:
	default:
		return fmt.Errorf("unknown plugin source type %q", j.Source)
	}
	*s = Source{Kind: kind, Repo: j.Repo, URL: j.URL, Path: j.Path, Ref: j.Ref, Sha: j.Sha}
	return nil
}

func (s Source) MarshalJSON() ([]byte, error) {
	return json.Marshal(sourceJSON{
		Source: string(s.Kind), Repo: s.Repo, URL: s.URL, Path: s.Path, Ref: s.Ref, Sha: s.Sha,
	})
}
