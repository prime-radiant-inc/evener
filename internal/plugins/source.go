package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

var (
	sourceStat        = os.Stat
	sourceWalk        = filepath.Walk
	sourceRel         = filepath.Rel
	sourceOpen        = os.Open
	sourceMkdirAll    = os.MkdirAll
	sourceOpenFile    = os.OpenFile
	sourceCopy        = io.Copy
	sourceClose       = func(f *os.File) error { return f.Close() }
	sourceRemoveAll   = os.RemoveAll
	sourceGitClone    = gitClone
	sourceGitHeadSHA  = gitHeadSHA
	sourceSparseClone = gitSparseClone
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
	Rel    bool   `json:"rel,omitempty"`
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
	*s = Source{Kind: kind, Repo: j.Repo, URL: j.URL, Path: j.Path, Ref: j.Ref, Sha: j.Sha, Rel: j.Rel}
	return nil
}

func (s Source) MarshalJSON() ([]byte, error) {
	return json.Marshal(sourceJSON{
		Source: string(s.Kind), Repo: s.Repo, URL: s.URL, Path: s.Path, Ref: s.Ref, Sha: s.Sha, Rel: s.Rel,
	})
}

// fetchPluginSource materializes a plugin's source into destDir. It returns the
// resolved commit sha (empty for directory/relative sources).
func fetchPluginSource(ctx context.Context, src Source, marketplaceRoot, destDir string) (string, error) {
	switch {
	case src.Rel || src.Kind == SourceDirectory:
		from := src.Path
		if src.Rel {
			from = filepath.Join(marketplaceRoot, src.Path)
		}
		if err := copyTree(from, destDir); err != nil {
			return "", err
		}
		return "", nil
	case src.Kind == SourceGitHub:
		url := "https://github.com/" + src.Repo + ".git"
		if err := sourceGitClone(ctx, url, destDir, src.Ref, src.Sha); err != nil {
			return "", err
		}
		return sourceGitHeadSHA(ctx, destDir)
	case src.Kind == SourceURL:
		if err := sourceGitClone(ctx, src.URL, destDir, src.Ref, src.Sha); err != nil {
			return "", err
		}
		return sourceGitHeadSHA(ctx, destDir)
	case src.Kind == SourceGitSubdir:
		clone := destDir + ".clone"
		defer func() { _ = sourceRemoveAll(clone) }()
		if err := sourceSparseClone(ctx, src.URL, clone, src.Path, src.Ref, src.Sha); err != nil {
			return "", err
		}
		if err := copyTree(filepath.Join(clone, src.Path), destDir); err != nil {
			return "", err
		}
		return sourceGitHeadSHA(ctx, clone)
	default:
		return "", fmt.Errorf("unsupported plugin source %q", src.Kind)
	}
}

// copyTree recursively copies src to dst (files, dirs, and symlink targets as
// regular files), creating directories as needed and overwriting existing files.
func copyTree(src, dst string) error {
	info, err := sourceStat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("source %q is not a directory", src)
	}
	return sourceWalk(src, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := sourceRel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if fi.IsDir() {
			return sourceMkdirAll(target, 0o755)
		}
		in, err := sourceOpen(path)
		if err != nil {
			return err
		}
		defer func() { _ = sourceClose(in) }()
		if err := sourceMkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		out, err := sourceOpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, fi.Mode().Perm())
		if err != nil {
			return err
		}
		_, err = sourceCopy(out, in)
		if closeErr := sourceClose(out); err == nil {
			err = closeErr
		}
		return err
	})
}
