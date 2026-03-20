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
	hunks  [][]string // diff lines per hunk (may include leading @@ hint line)
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
			hint := hintFromHunk(h)
			searchStart := pos
			if hint != "" {
				hintIdx := indexOfLine(origLines, hint, pos)
				if hintIdx >= 0 {
					searchStart = hintIdx
				}
			}
			k := indexOfLine(origLines, anchor, searchStart)
			if k < 0 {
				// Fall back to searching from pos if hint-narrowed search failed
				k = indexOfLine(origLines, anchor, pos)
			}
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
				if pos >= len(origLines) {
					return nil, fmt.Errorf("apply_patch: context mismatch in %s: want %q at line %d but file only has %d lines", o.path, body, pos+1, len(origLines))
				}
				if !fuzzyLineMatch(origLines[pos], body) {
					return nil, fmt.Errorf("apply_patch: context mismatch in %s: want %q at line %d, got %q", o.path, body, pos+1, origLines[pos])
				}
				out = append(out, origLines[pos])
				pos++
			case '-':
				if pos >= len(origLines) {
					return nil, fmt.Errorf("apply_patch: delete mismatch in %s: want %q at line %d but file only has %d lines", o.path, body, pos+1, len(origLines))
				}
				if !fuzzyLineMatch(origLines[pos], body) {
					return nil, fmt.Errorf("apply_patch: delete mismatch in %s: want %q at line %d, got %q", o.path, body, pos+1, origLines[pos])
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
		case strings.TrimSpace(l) == "*** End of File":
			continue
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
					cur = []string{lines[i]}
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
	// Allow absolute paths that fall under rootDir by stripping the prefix.
	if filepath.IsAbs(r) {
		cleanRoot := filepath.Clean(rootDir) + string(filepath.Separator)
		cleanR := filepath.Clean(r)
		if strings.HasPrefix(cleanR, cleanRoot) {
			r = cleanR[len(cleanRoot):]
		} else if cleanR == filepath.Clean(rootDir) {
			return "", fmt.Errorf("path is rootDir itself: %s", rel)
		} else {
			return "", fmt.Errorf("absolute path outside working directory: %s", rel)
		}
	}
	clean := filepath.Clean(r)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path traversal not allowed: %s", rel)
	}
	return filepath.Join(rootDir, clean), nil
}

// hintFromHunk extracts the positioning hint text after @@ in a hunk.
// The hint (typically a function signature) narrows where we search for the anchor.
func hintFromHunk(hunkLines []string) string {
	for _, l := range hunkLines {
		if strings.HasPrefix(l, "@@") {
			return strings.TrimSpace(strings.TrimPrefix(l, "@@"))
		}
	}
	return ""
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

// normalizeWS collapses all whitespace runs to single spaces and trims ends.
func normalizeWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

var unicodePunctReplacer = strings.NewReplacer(
	"\u2018", "'", "\u2019", "'", // left/right single quote
	"\u201C", "\"", "\u201D", "\"", // left/right double quote
	"\u2013", "-", "\u2014", "-", // en-dash, em-dash
	"\u2026", "...", // ellipsis
	"\u00A0", " ", // non-breaking space
)

func normalizeUnicode(s string) string {
	return unicodePunctReplacer.Replace(s)
}

// fuzzyLineMatch returns true if a and b match after whitespace normalization
// and (if needed) Unicode punctuation normalization.
func fuzzyLineMatch(a, b string) bool {
	if a == b {
		return true
	}
	na, nb := normalizeWS(a), normalizeWS(b)
	if na == nb {
		return true
	}
	if na == "" {
		return false
	}
	return normalizeUnicode(na) == normalizeUnicode(nb)
}

func indexOfLine(lines []string, want string, start int) int {
	// Try exact match first.
	for i := start; i < len(lines); i++ {
		if lines[i] == want {
			return i
		}
	}
	// Fuzzy: whitespace-normalized match.
	normWant := normalizeWS(want)
	if normWant == "" {
		return -1
	}
	for i := start; i < len(lines); i++ {
		if normalizeWS(lines[i]) == normWant {
			return i
		}
	}
	// Fuzzy: Unicode punctuation equivalence.
	normUniWant := normalizeUnicode(normWant)
	for i := start; i < len(lines); i++ {
		if normalizeUnicode(normalizeWS(lines[i])) == normUniWant {
			return i
		}
	}
	return -1
}
