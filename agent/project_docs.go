package agent

import (
	"os"
	"path/filepath"
	"strings"

	"primeradiant.com/evener/agent/execenv"
)

// ProjectDoc holds a single loaded project instruction file: its identifier path and raw content.
type ProjectDoc struct {
	// Path is a stable, human-friendly identifier for the instruction file (relative to git root when available).
	// The personal doc is the exception: a display path outside the repo, with the home directory collapsed to "~".
	Path string
	// Content is the raw file content (may be truncated when total budget is exceeded).
	Content string
}

const (
	projectDocByteBudget = 32 * 1024
	projectDocTruncMark  = "[Project instructions truncated at 32KB]"
	userDocTruncMark     = "[Personal instructions truncated at 32KB]"
)

// UserDocFile is the personal instructions file evener loads from the user
// config root ahead of every repo's own project docs.
const UserDocFile = "AGENTS.md"

// LoadProjectDocs discovers and loads project instruction files from git root (or working directory when not
// in a git repo) down to the current working directory. Files are loaded in depth order (root first; deeper
// files have higher precedence) and filtered by the active provider profile (caller-provided list).
func LoadProjectDocs(env execenv.ExecutionEnvironment, filenames ...string) ([]ProjectDoc, bool) {
	return loadProjectDocs(env, 0, filenames...)
}

// LoadInstructionDocs is a session's whole instruction set: the personal doc
// at userDocPath first, then the repo's project docs. The two byte budgets are
// separate, each the size of projectDocByteBudget, so a long personal doc can
// never silence a repo's instructions and a long repo doc can never silence
// the user's own; each side truncates with its own marker.
func LoadInstructionDocs(env execenv.ExecutionEnvironment, userDocPath string, filenames ...string) ([]ProjectDoc, bool) {
	out := []ProjectDoc{}
	userTruncated := false
	if user, ok := LoadUserDoc(userDocPath); ok {
		if len(user.Content) > projectDocByteBudget {
			user.Content = truncateDoc(user.Content, projectDocByteBudget, userDocTruncMark)
			userTruncated = true
		}
		out = append(out, user)
	}
	project, truncated := loadProjectDocs(env, 0, filenames...)
	return append(out, project...), truncated || userTruncated
}

// LoadUserDoc reads the personal instructions file at path. ok is false when
// the path is empty or the file is missing or blank. Path is the display path
// the prompt labels the block with: the home directory collapsed to "~", so
// the model sees "~/.config/evener/AGENTS.md" rather than a machine-specific
// absolute path.
func LoadUserDoc(path string) (ProjectDoc, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return ProjectDoc{}, false
	}
	b, err := os.ReadFile(path)
	if err != nil || strings.TrimSpace(string(b)) == "" {
		return ProjectDoc{}, false
	}
	return ProjectDoc{Path: tildeCollapse(path), Content: string(b)}, true
}

// tildeCollapse rewrites a path under the home directory as "~/...".
func tildeCollapse(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	rel, err := filepath.Rel(home, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return path
	}
	return "~/" + filepath.ToSlash(rel)
}

// truncateDoc cuts content to remain bytes and appends mark, the truncation
// marker naming the kind of doc that was cut.
func truncateDoc(content string, remain int, mark string) string {
	if remain < len(content) {
		content = content[:remain]
	}
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return content + mark + "\n"
}

// loadProjectDocs is LoadProjectDocs with `used` bytes of the budget already
// spent by a doc loaded before the repo's own.
func loadProjectDocs(env execenv.ExecutionEnvironment, used int, filenames ...string) ([]ProjectDoc, bool) {
	if env == nil {
		return nil, false
	}

	cwd := strings.TrimSpace(env.WorkingDirectory())
	if cwd == "" {
		return nil, false
	}
	// Resolve symlinks so cwd and git root use consistent paths (macOS /var -> /private/var).
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = resolved
	}

	root := cwd
	if gr := execenv.GitRootOrEmpty(env, cwd); gr != "" {
		root = gr
	}

	dirs := execenv.DirsFromRootToCwd(root, cwd)
	out := []ProjectDoc{}
	for _, dir := range dirs {
		relDir := "."
		if r, err := filepath.Rel(root, dir); err == nil {
			relDir = r
		}
		for _, name := range filenames {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			path := filepath.Join(dir, name)
			b, err := os.ReadFile(path)
			if err != nil {
				continue
			}

			key := name
			if relDir != "." && relDir != "" {
				key = filepath.Join(relDir, name)
			}

			content := string(b)
			if used+len(content) > projectDocByteBudget {
				out = append(out, ProjectDoc{Path: key, Content: truncateDoc(content, projectDocByteBudget-used, projectDocTruncMark)})
				return out, true
			}
			used += len(content)
			out = append(out, ProjectDoc{Path: key, Content: content})
		}
	}
	return out, false
}
