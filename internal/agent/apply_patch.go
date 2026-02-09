package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ApplyPatch applies a codex-rs-style apply_patch v4a patch to files under rootDir.
// This is a best-effort implementation intended for local agent loops.
func ApplyPatch(rootDir string, patch string) (string, error) {
	ops, err := parseV4APatch(patch)
	if err != nil {
		return "", err
	}
	var touched []string
	for _, op := range ops {
		paths, err := op.apply(rootDir)
		if err != nil {
			return "", err
		}
		touched = append(touched, paths...)
	}
	if len(touched) == 0 {
		return "no changes", nil
	}
	return "applied patch to:\n" + strings.Join(touched, "\n"), nil
}

type patchOp interface {
	apply(rootDir string) ([]string, error)
}

type addFileOp struct {
	path  string
	lines []string
}

func (o addFileOp) apply(rootDir string) ([]string, error) {
	p, err := safeJoin(rootDir, o.path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, err
	}
	content := strings.Join(o.lines, "\n")
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		return nil, err
	}
	return []string{o.path}, nil
}

type deleteFileOp struct {
	path string
}

func (o deleteFileOp) apply(rootDir string) ([]string, error) {
	p, err := safeJoin(rootDir, o.path)
	if err != nil {
		return nil, err
	}
	_ = os.Remove(p)
	return []string{o.path}, nil
}

type updateFileOp struct {
	path   string
	moveTo string
	hunks  [][]string // diff lines without @@ separators
}

func (o updateFileOp) apply(rootDir string) ([]string, error) {
	p, err := safeJoin(rootDir, o.path)
	if err != nil {
		return nil, err
	}
	origBytes, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	origText := strings.ReplaceAll(string(origBytes), "\r\n", "\n")
	hasFinalNL := strings.HasSuffix(origText, "\n")
	origLines := strings.Split(strings.TrimSuffix(origText, "\n"), "\n")

	out := make([]string, 0, len(origLines))
	pos := 0

	for _, h := range o.hunks {
		anchor := firstAnchor(h)
		if anchor != "" {
			k := indexOfLine(origLines, anchor, pos)
			if k < 0 {
				return nil, fmt.Errorf("apply_patch: anchor not found in %s: %q", o.path, anchor)
			}
			out = append(out, origLines[pos:k]...)
			pos = k
		}

		for _, l := range h {
			if strings.HasPrefix(l, "@@") {
				continue
			}
			if l == "" {
				// Diff lines always have a prefix; ignore empty (best-effort).
				continue
			}
			prefix := l[0]
			body := ""
			if len(l) > 1 {
				body = l[1:]
			}
			switch prefix {
			case ' ':
				if pos >= len(origLines) || origLines[pos] != body {
					return nil, fmt.Errorf("apply_patch: context mismatch in %s: want %q at line %d", o.path, body, pos+1)
				}
				out = append(out, body)
				pos++
			case '-':
				if pos >= len(origLines) || origLines[pos] != body {
					return nil, fmt.Errorf("apply_patch: delete mismatch in %s: want %q at line %d", o.path, body, pos+1)
				}
				pos++
			case '+':
				out = append(out, body)
			default:
				// ignore
			}
		}
	}

	out = append(out, origLines[pos:]...)
	newText := strings.Join(out, "\n")
	if hasFinalNL {
		newText += "\n"
	}
	if err := os.WriteFile(p, []byte(newText), 0o644); err != nil {
		return nil, err
	}
	paths := []string{o.path}
	if strings.TrimSpace(o.moveTo) != "" && o.moveTo != o.path {
		dst, err := safeJoin(rootDir, o.moveTo)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return nil, err
		}
		if err := os.Rename(p, dst); err != nil {
			return nil, err
		}
		paths = append(paths, o.moveTo)
	}
	return paths, nil
}

func parseV4APatch(patch string) ([]patchOp, error) {
	lines := strings.Split(strings.ReplaceAll(patch, "\r\n", "\n"), "\n")
	return parseV4APatchLines(lines)
}

func parseV4APatchLines(lines []string) ([]patchOp, error) {
	i := 0
	if i >= len(lines) || strings.TrimSpace(lines[i]) != "*** Begin Patch" {
		return nil, fmt.Errorf("apply_patch: expected '*** Begin Patch'")
	}
	i++

	var ops []patchOp
	for i < len(lines) {
		l := lines[i]
		i++
		if strings.TrimSpace(l) == "*** End Patch" {
			return ops, nil
		}
		if strings.TrimSpace(l) == "" {
			continue
		}
		switch {
		case strings.HasPrefix(l, "*** Add File: "):
			path := strings.TrimSpace(strings.TrimPrefix(l, "*** Add File: "))
			var content []string
			for i < len(lines) {
				if strings.HasPrefix(lines[i], "*** ") {
					break
				}
				if strings.TrimSpace(lines[i]) == "*** End Patch" {
					break
				}
				if !strings.HasPrefix(lines[i], "+") {
					return nil, fmt.Errorf("apply_patch: add file %s: expected '+' line, got %q", path, lines[i])
				}
				content = append(content, strings.TrimPrefix(lines[i], "+"))
				i++
			}
			ops = append(ops, addFileOp{path: path, lines: content})
		case strings.HasPrefix(l, "*** Delete File: "):
			path := strings.TrimSpace(strings.TrimPrefix(l, "*** Delete File: "))
			ops = append(ops, deleteFileOp{path: path})
		case strings.HasPrefix(l, "*** Update File: "):
			path := strings.TrimSpace(strings.TrimPrefix(l, "*** Update File: "))
			moveTo := ""
			if i < len(lines) && strings.HasPrefix(lines[i], "*** Move to: ") {
				moveTo = strings.TrimSpace(strings.TrimPrefix(lines[i], "*** Move to: "))
				i++
			}
			var hunks [][]string
			var cur []string
			for i < len(lines) {
				if strings.HasPrefix(lines[i], "*** ") || strings.TrimSpace(lines[i]) == "*** End Patch" {
					break
				}
				if strings.HasPrefix(lines[i], "@@") && len(cur) > 0 {
					hunks = append(hunks, cur)
					cur = nil
					i++
					continue
				}
				cur = append(cur, lines[i])
				i++
			}
			if len(cur) > 0 {
				hunks = append(hunks, cur)
			}
			ops = append(ops, updateFileOp{path: path, moveTo: moveTo, hunks: hunks})
		default:
			return nil, fmt.Errorf("apply_patch: unexpected line: %q", l)
		}
	}
	return nil, fmt.Errorf("apply_patch: missing '*** End Patch'")
}

func safeJoin(rootDir, rel string) (string, error) {
	r := strings.TrimSpace(rel)
	if r == "" {
		return "", fmt.Errorf("empty path")
	}
	if filepath.IsAbs(r) {
		return "", fmt.Errorf("absolute paths not allowed: %s", rel)
	}
	clean := filepath.Clean(r)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path traversal not allowed: %s", rel)
	}
	return filepath.Join(rootDir, clean), nil
}

func firstAnchor(hunk []string) string {
	for _, l := range hunk {
		if l == "" {
			continue
		}
		if strings.HasPrefix(l, "@@") {
			continue
		}
		if l[0] == ' ' || l[0] == '-' {
			return l[1:]
		}
	}
	return ""
}

func indexOfLine(lines []string, want string, start int) int {
	for i := start; i < len(lines); i++ {
		if lines[i] == want {
			return i
		}
	}
	return -1
}
