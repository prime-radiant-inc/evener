package agent

import (
	"os"
	"path/filepath"
	"strings"

	"primeradiant.com/serf/agent/execenv"
)

// ProjectDoc holds a single loaded project instruction file: its identifier path and raw content.
type ProjectDoc struct {
	// Path is a stable, human-friendly identifier for the instruction file (relative to git root when available).
	Path string
	// Content is the raw file content (may be truncated when total budget is exceeded).
	Content string
}

const (
	projectDocByteBudget = 32 * 1024
	projectDocTruncMark  = "[Project instructions truncated at 32KB]"
)

// LoadProjectDocs discovers and loads project instruction files from git root (or working directory when not
// in a git repo) down to the current working directory. Files are loaded in depth order (root first; deeper
// files have higher precedence) and filtered by the active provider profile (caller-provided list).
func LoadProjectDocs(env execenv.ExecutionEnvironment, filenames ...string) ([]ProjectDoc, bool) {
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
	used := 0
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
				remain := projectDocByteBudget - used
				if remain < 0 {
					remain = 0
				}
				if remain < len(content) {
					content = content[:remain]
				}
				if !strings.HasSuffix(content, "\n") {
					content += "\n"
				}
				content += projectDocTruncMark + "\n"
				out = append(out, ProjectDoc{Path: key, Content: content})
				return out, true
			}
			used += len(content)
			out = append(out, ProjectDoc{Path: key, Content: content})
		}
	}
	return out, false
}

// (gitRootOrEmpty and dirsFromRootToCwd moved to agent/execenv as
// GitRootOrEmpty / DirsFromRootToCwd — see agent/execenv/gitpath.go.)
