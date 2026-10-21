package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
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
	sourcePathTree    = gitPathTree
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

// gitRemoteURL is the clone URL of a git-backed source (github, url or
// git-subdir), or "" for a directory or relative source, which has no remote.
func gitRemoteURL(src Source) string {
	switch {
	case src.Rel || src.Kind == SourceDirectory:
		return ""
	case src.Kind == SourceGitHub:
		return "https://github.com/" + src.Repo + ".git"
	default:
		return src.URL
	}
}

// fetchPluginSource materializes a plugin's source into destDir. It returns the
// resolved commit sha: for a relative source, the id of the tree the
// marketplace clone holds at the plugin's folder (gitPathTree), so an Upgrade
// after a refresh copies the folder again exactly when its contents changed;
// empty for a directory source.
func fetchPluginSource(ctx context.Context, src Source, marketplaceRoot, destDir string) (string, error) {
	if err := validateSourceSelection(ctx, src); err != nil {
		return "", err
	}
	switch {
	case src.Rel:
		sha, err := sourcePathTree(ctx, marketplaceRoot, src.Path)
		if err != nil {
			return "", err
		}
		if err := copyTree(filepath.Join(marketplaceRoot, src.Path), destDir); err != nil {
			return "", err
		}
		return sha, nil
	case src.Kind == SourceDirectory:
		return "", copyTree(src.Path, destDir)
	case src.Kind == SourceGitHub || src.Kind == SourceURL:
		if err := sourceGitClone(ctx, gitRemoteURL(src), destDir, src.Ref, src.Sha); err != nil {
			return "", err
		}
		sha, err := sourceGitHeadSHA(ctx, destDir)
		if err != nil {
			return "", err
		}
		// The clone lives in private staging. Its work tree is what gets
		// published, so the Git directory is removed once the commit is known.
		if err := removeGitMetadata(destDir); err != nil {
			return "", err
		}
		return sha, nil
	case src.Kind == SourceGitSubdir:
		clone := destDir + ".clone"
		defer func() { _ = sourceRemoveAll(clone) }()
		if err := sourceSparseClone(ctx, gitRemoteURL(src), clone, src.Path, src.Ref, src.Sha); err != nil {
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

// validateSourceSelection refuses a copied source that names Git
// administration rather than plugin content. For a copied directory it looks
// for a real administrative directory; for a repository-relative selection any
// .git component is metadata because Git never tracks one. A legitimate
// marketplace/plugin namespace named .git, or plugin content beneath one, stays
// selectable. In-place directory-marketplace sources return from stagePlugin
// before this function and remain user-owned.
func validateSourceSelection(ctx context.Context, src Source) error {
	switch {
	case src.Rel:
		return rejectGitPathComponent(src.Path, "relative plugin source")
	case src.Kind == SourceGitSubdir:
		return rejectGitPathComponent(src.Path, "git-subdir plugin source")
	case src.Kind == SourceDirectory:
		administration, err := selectedDirectoryIsGitAdministration(ctx, src.Path)
		if err != nil {
			return err
		}
		if administration {
			return fmt.Errorf("plugin source %q selects Git administration", src.Path)
		}
	}
	return nil
}

func rejectGitPathComponent(selected, kind string) error {
	if hasGitComponent(selected) {
		return fmt.Errorf("%s %q names Git metadata", kind, selected)
	}
	return nil
}

func selectedDirectoryIsGitAdministration(ctx context.Context, selected string) (bool, error) {
	path := resolvedOrSelf(filepath.Clean(selected))
	if gitAvailable() {
		// Git's own structural verdict avoids treating an ordinary plugin tree
		// that happens to contain HEAD/config/objects as administration. A
		// definite true or false on stdout settles it; an error or any other
		// output falls through to the structural check below.
		if out, err := gitStdout(ctx, path, "rev-parse", "--is-inside-git-dir"); err == nil {
			switch strings.TrimSpace(out) {
			case "true":
				return true, nil
			case "false":
				return false, nil
			}
		}
	}
	// Without Git, recognize only an administrative-looking directory whose own
	// name or nearest .git ancestor marks it as such. A bare or separate git
	// directory with an unrelated name is refused with an explicit cannot-verify
	// error rather than guessed at or silently published.
	for {
		if info, err := os.Stat(path); err == nil && info.IsDir() && looksLikeGitAdministration(path) &&
			strings.HasSuffix(strings.ToLower(filepath.Base(path)), ".git") {
			return true, nil
		}
		if info, err := os.Stat(path); err == nil && info.IsDir() && looksLikeGitAdministration(path) {
			return false, fmt.Errorf("cannot verify plugin source %q", selected)
		}
		parent := filepath.Dir(path)
		if parent == path {
			return false, nil
		}
		path = parent
	}
}

// looksLikeGitAdministration recognizes a normal, bare, or separate Git
// directory by its HEAD and config files and objects directory, not by its
// name. A linked worktree's administrative directory has HEAD but no objects or
// config; its enclosing .git directory supplies the markers.
func looksLikeGitAdministration(dir string) bool {
	head, err := readPrefix(filepath.Join(dir, "HEAD"), 4<<10)
	if err != nil {
		return false
	}
	headText := strings.TrimSpace(string(head))
	if !strings.HasPrefix(headText, "ref: refs/") && !isHexObjectID(headText) {
		return false
	}
	config, err := readPrefix(filepath.Join(dir, "config"), 64<<10)
	if err != nil || !strings.Contains(string(config), "[core]") {
		return false
	}
	objects, err := os.Stat(filepath.Join(dir, "objects"))
	if err != nil || !objects.IsDir() {
		return false
	}
	entries, err := os.ReadDir(filepath.Join(dir, "objects"))
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.Name() == "info" || entry.Name() == "pack" || isHexPair(entry.Name()) {
			return true
		}
	}
	return false
}

func readPrefix(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	return io.ReadAll(io.LimitReader(file, limit))
}

func isHexObjectID(text string) bool {
	if len(text) != 40 && len(text) != 64 {
		return false
	}
	for _, r := range text {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}

func isHexPair(name string) bool {
	if len(name) != 2 {
		return false
	}
	for _, r := range name {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}

// copyTree recursively copies src to dst (files, dirs, and symlink targets as
// regular files), creating directories as needed and overwriting existing files.
// Every entry named .git is skipped, and a symlink reaching Git metadata is
// refused: those bytes must never enter a published plugin tree (#4768).
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
		if path != src && isGitName(fi.Name()) {
			if fi.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			if err := refuseGitSymlink(src, path); err != nil {
				return err
			}
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

// refuseGitSymlink refuses a source symlink whose lexical or resolved target
// names a .git component. Ordinary links keep copyTree's existing dereference
// behavior; only links into Git metadata are refused.
func refuseGitSymlink(src, path string) error {
	reachesGit, err := symlinkReachesGit(src, path)
	if err != nil {
		return err
	}
	if reachesGit {
		return gitSymlinkError(src, path)
	}
	return nil
}

func gitSymlinkError(src, path string) error {
	rel, err := filepath.Rel(src, path)
	if err != nil {
		rel = path
	}
	return fmt.Errorf("plugin source link %q reaches Git metadata", rel)
}

// hasGitComponent reports whether any path component names .git, in any case.
func hasGitComponent(path string) bool {
	return slices.ContainsFunc(strings.Split(filepath.ToSlash(path), "/"), isGitName)
}

// removeGitMetadata removes a work tree's .git entry without following a
// symlink. A link is unlinked, a file removed, and a directory removed whole;
// the target of an external link is never touched.
func removeGitMetadata(dir string) error {
	path := filepath.Join(dir, ".git")
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() {
		return os.RemoveAll(path)
	}
	return os.Remove(path)
}
