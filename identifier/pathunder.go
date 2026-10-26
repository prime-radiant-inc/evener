package identifier

import (
	"path/filepath"
	"strings"
)

// PathUnder reports whether path is dir itself or lies beneath it. The
// comparison is lexical: both arguments are cleaned, and callers that need
// symlink-aware containment resolve them first (ResolveClean). An empty dir
// contains nothing, and a relative path is never under an absolute dir.
func PathUnder(path, dir string) bool {
	if strings.TrimSpace(dir) == "" {
		return false
	}
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
