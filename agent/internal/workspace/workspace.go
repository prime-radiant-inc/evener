package workspace

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// WorkspaceInfo captures the directory structure and key files in the
// working directory. Injected into the system prompt so the model starts
// with awareness of what's available — no discovery round needed.
type WorkspaceInfo struct {
	Tree      string `json:"tree,omitempty"`       // indented directory listing
	BuildInfo string `json:"build_info,omitempty"` // build system summary
}

const (
	maxDirDepth  = 3   // how deep to show directories
	maxFileDepth = 2   // how deep to show files (shallower to save tokens)
	maxEntries   = 200 // cap tree entries to keep prompt manageable
)

// excludedDirs are directories skipped during workspace scanning.
var excludedDirs = map[string]bool{
	".git":          true,
	".hg":           true,
	".svn":          true,
	"node_modules":  true,
	"__pycache__":   true,
	".tox":          true,
	".mypy_cache":   true,
	".pytest_cache": true,
	"venv":          true,
	".venv":         true,
	".eggs":         true,
	"dist":          true,
	".cache":        true,
}

// ScanWorkspace walks the working directory and returns structured context
// about its contents: a directory tree, detected test files, and build
// system information.
func ScanWorkspace(root string) WorkspaceInfo {
	var ws WorkspaceInfo

	entries, truncated := walkTree(root)
	ws.Tree = formatTree(root, entries, truncated)
	ws.BuildInfo = detectBuildSystem(root)

	return ws
}

// treeEntry represents a file or directory in the workspace.
type treeEntry struct {
	RelPath string // relative to workspace root
	IsDir   bool
	Size    int64
	Depth   int
}

// walkTree collects directory entries up to maxDepth and maxEntries.
func walkTree(root string) ([]treeEntry, bool) {
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return nil, false
	}

	var entries []treeEntry
	truncated := false

	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if depth > maxDirDepth {
			return
		}
		if len(entries) >= maxEntries {
			truncated = true
			return
		}

		dirEntries, err := os.ReadDir(dir)
		if err != nil {
			return
		}

		// Sort: directories first, then files, alphabetical within each group.
		sort.Slice(dirEntries, func(i, j int) bool {
			di, dj := dirEntries[i].IsDir(), dirEntries[j].IsDir()
			if di != dj {
				return di // dirs first
			}
			return dirEntries[i].Name() < dirEntries[j].Name()
		})

		for _, de := range dirEntries {
			if len(entries) >= maxEntries {
				truncated = true
				return
			}

			name := de.Name()

			// Skip hidden files/dirs and excluded directories.
			if strings.HasPrefix(name, ".") {
				continue
			}
			if de.IsDir() && excludedDirs[name] {
				continue
			}

			// Skip files deeper than maxFileDepth (dirs can go deeper).
			if !de.IsDir() && depth > maxFileDepth {
				continue
			}

			rel, _ := filepath.Rel(root, filepath.Join(dir, name))

			var size int64
			if fi, err := de.Info(); err == nil {
				size = fi.Size()
			}

			entries = append(entries, treeEntry{
				RelPath: rel,
				IsDir:   de.IsDir(),
				Size:    size,
				Depth:   depth,
			})

			if de.IsDir() {
				walk(filepath.Join(dir, name), depth+1)
			}
		}
	}

	walk(root, 0)
	return entries, truncated
}

// formatTree renders entries as a compact indented tree. Directories get their
// own lines; files within a directory are grouped on one comma-separated line.
func formatTree(root string, entries []treeEntry, truncated bool) string {
	if len(entries) == 0 {
		return ""
	}

	// Group files by their parent directory depth+path.
	type dirGroup struct {
		depth int
		files []string
	}

	var b strings.Builder
	b.WriteString(root + "/\n")

	var pendingFiles []string
	pendingDepth := -1

	flushFiles := func() {
		if len(pendingFiles) == 0 {
			return
		}
		indent := strings.Repeat("  ", pendingDepth+1)
		b.WriteString(indent + strings.Join(pendingFiles, ", ") + "\n")
		pendingFiles = nil
	}

	for _, e := range entries {
		if e.IsDir {
			flushFiles()
			indent := strings.Repeat("  ", e.Depth+1)
			b.WriteString(indent + filepath.Base(e.RelPath) + "/\n")
		} else {
			if e.Depth != pendingDepth {
				flushFiles()
				pendingDepth = e.Depth
			}
			pendingFiles = append(pendingFiles, filepath.Base(e.RelPath))
		}
	}
	flushFiles()

	if truncated {
		b.WriteString("  ... (truncated, >200 entries)\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// detectBuildSystem examines the workspace root for build system files and
// extracts useful metadata (targets, scripts, project type).
func detectBuildSystem(root string) string {
	var parts []string

	// Makefile: extract targets.
	if targets := parseMakefileTargets(filepath.Join(root, "Makefile")); len(targets) > 0 {
		parts = append(parts, fmt.Sprintf("Makefile targets: %s", strings.Join(targets, ", ")))
	} else if targets := parseMakefileTargets(filepath.Join(root, "makefile")); len(targets) > 0 {
		parts = append(parts, fmt.Sprintf("Makefile targets: %s", strings.Join(targets, ", ")))
	}

	// package.json: extract scripts.
	if scripts := parsePackageJsonScripts(filepath.Join(root, "package.json")); len(scripts) > 0 {
		parts = append(parts, fmt.Sprintf("package.json scripts: %s", strings.Join(scripts, ", ")))
	}

	// CMakeLists.txt.
	if _, err := os.Stat(filepath.Join(root, "CMakeLists.txt")); err == nil {
		parts = append(parts, "CMake project (CMakeLists.txt)")
	}

	// go.mod.
	if b, err := os.ReadFile(filepath.Join(root, "go.mod")); err == nil {
		lines := strings.Split(string(b), "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "module ") {
				parts = append(parts, fmt.Sprintf("Go module (go.mod): %s", strings.TrimPrefix(line, "module ")))
				break
			}
		}
	}

	// Cargo.toml.
	if _, err := os.Stat(filepath.Join(root, "Cargo.toml")); err == nil {
		parts = append(parts, "Rust project (Cargo.toml)")
	}

	// pyproject.toml or setup.py.
	if _, err := os.Stat(filepath.Join(root, "pyproject.toml")); err == nil {
		parts = append(parts, "Python project (pyproject.toml)")
	} else if _, err := os.Stat(filepath.Join(root, "setup.py")); err == nil {
		parts = append(parts, "Python project (setup.py)")
	}

	// pytest.ini or setup.cfg with pytest section.
	if _, err := os.Stat(filepath.Join(root, "pytest.ini")); err == nil {
		parts = append(parts, "pytest configured (pytest.ini)")
	}

	// Dockerfile.
	if _, err := os.Stat(filepath.Join(root, "Dockerfile")); err == nil {
		parts = append(parts, "Dockerfile present")
	}
	if _, err := os.Stat(filepath.Join(root, "docker-compose.yml")); err == nil {
		parts = append(parts, "docker-compose.yml present")
	} else if _, err := os.Stat(filepath.Join(root, "docker-compose.yaml")); err == nil {
		parts = append(parts, "docker-compose.yaml present")
	}

	return strings.Join(parts, "\n")
}

// parseMakefileTargets extracts target names from a Makefile.
// Only includes named targets (not pattern rules or implicit rules).
func parseMakefileTargets(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var targets []string
	seen := make(map[string]bool)
	scanner := bufio.NewScanner(f)

	for scanner.Scan() {
		line := scanner.Text()

		// Skip comments and empty lines.
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}

		// Skip lines starting with whitespace (recipe lines).
		if len(line) > 0 && (line[0] == '\t' || line[0] == ' ') {
			continue
		}

		// Skip variable assignments.
		if strings.Contains(line, "=") && !strings.Contains(line, ":") {
			continue
		}

		// Target lines: "name:" or "name: deps"
		if idx := strings.Index(line, ":"); idx > 0 {
			// Skip pattern rules (contain %).
			targetPart := line[:idx]
			if strings.Contains(targetPart, "%") {
				continue
			}

			// Skip special targets.
			if strings.HasPrefix(targetPart, ".") {
				continue
			}

			// May have multiple targets: "a b c: deps"
			for _, t := range strings.Fields(targetPart) {
				t = strings.TrimSpace(t)
				if t != "" && !seen[t] {
					targets = append(targets, t)
					seen[t] = true
				}
			}
		}
	}

	return targets
}

// parsePackageJsonScripts extracts script names from a package.json file.
func parsePackageJsonScripts(path string) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(b, &pkg); err != nil {
		return nil
	}

	var scripts []string
	for name := range pkg.Scripts {
		scripts = append(scripts, name)
	}
	sort.Strings(scripts)
	return scripts
}
