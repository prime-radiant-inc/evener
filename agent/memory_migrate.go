package agent

import (
	"errors"
	"io/fs"
	"maps"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"primeradiant.com/evener/agent/execenv"
)

// memoryLegacyIndexBackup is where migration keeps a scope's hand-written
// index. Its dot name keeps it out of the pages and out of memory_search.
const memoryLegacyIndexBackup = ".MEMORY.md.pre-generated"

// freeMemoryBackupPath is the first backup name in root that names nothing:
// memoryLegacyIndexBackup, then the same name with ".2", ".3" and so on. An
// older Evener build can write MEMORY.md again after migration; numbering
// keeps the first backup, the one holding the original index, from being
// overwritten when that file is migrated too.
func freeMemoryBackupPath(env *execenv.LocalExecutionEnvironment, root string) string {
	backup := filepath.Join(root, memoryLegacyIndexBackup)
	for n := 2; env.FileExists(backup); n++ {
		backup = filepath.Join(root, memoryLegacyIndexBackup+"."+strconv.Itoa(n))
	}
	return backup
}

// memoryYAMLField encodes one frontmatter line, quoting value as YAML needs.
func memoryYAMLField(key, value string) string {
	encoded, err := yaml.Marshal(map[string]string{key: value})
	if err != nil {
		// a string-to-string map always encodes.
		panic(err)
	}
	return string(encoded)
}

// setMemoryFrontmatterField makes line (an encoded "key: value\n") the page's
// only top-level entry for its key, replacing an existing one and its continuation
// lines, or adding it at the end of the block, or adding a block. Every other
// byte of the page is kept.
func setMemoryFrontmatterField(raw []byte, line string) []byte {
	key, _, _ := strings.Cut(line, ":")
	text := string(raw)
	block, body, ok := splitMemoryFrontmatter(text)
	if !ok {
		return []byte("---\n" + line + "---\n" + text)
	}
	var kept []string
	replaced, skipping := false, false
	lines := slices.Collect(strings.Lines(block))
	for i, existing := range lines {
		if skipping && memoryFrontmatterContinuation(lines[i:]) {
			continue
		}
		skipping = false
		if strings.HasPrefix(existing, key+":") {
			if !replaced {
				kept = append(kept, line)
				replaced = true
			}
			skipping = true
			continue
		}
		kept = append(kept, existing)
	}
	if !replaced {
		if n := len(kept); n > 0 && !strings.HasSuffix(kept[n-1], "\n") {
			kept[n-1] += "\n"
		}
		kept = append(kept, line)
	}
	return []byte("---\n" + strings.Join(kept, "") + "---\n" + body)
}

// memoryFrontmatterContinuation reports whether lines[0] continues the value
// above it: an indented or list line, or a blank line followed, past any
// more blank lines, by one. A blank line before the next key ends the value.
func memoryFrontmatterContinuation(lines []string) bool {
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		return strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "-")
	}
	return false
}

var (
	legacyIndexLink     = regexp.MustCompile(`\[([^\]]*)\]\(([^)\s]+)\)`)
	legacyIndexBarePage = regexp.MustCompile(`[` + "`" + `*]*([^\s\[\]()` + "`" + `*]+\.md)[` + "`" + `*]*`)
)

// legacyIndexPathByte reports whether c can continue a path, so a bare
// "a.md" followed by it ("a.md.txt", "a.md/x") names no page.
func legacyIndexPathByte(c byte) bool {
	return c == '_' || c == '.' || c == '/' || c == '-' || '0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}

// legacyIndexBarePageMatch is the submatch indexes of line's first bare page
// path that nothing path-like follows, or nil.
func legacyIndexBarePageMatch(line string) []int {
	for _, m := range legacyIndexBarePage.FindAllStringSubmatchIndex(line, -1) {
		if m[3] == len(line) || !legacyIndexPathByte(line[m[3]]) {
			return m
		}
	}
	return nil
}

// legacyIndexTrim is what a description loses at both ends: separators.
const legacyIndexTrim = " \t-—–:"

// trimLegacyIndexDescription strips one list marker ("-", "*" or "+" and a
// space) from the start of rest, then separators from both ends, so
// "**important** note" and "learned C++" keep their own punctuation.
func trimLegacyIndexDescription(rest string) string {
	rest = strings.TrimLeft(rest, " \t")
	if len(rest) >= 2 && strings.ContainsRune("-*+", rune(rest[0])) && rest[1] == ' ' {
		rest = rest[2:]
	}
	return strings.Trim(rest, legacyIndexTrim)
}

// legacyIndexPage turns a link target into a page path in the scope, or
// reports false for anything that is not a local Markdown page.
func legacyIndexPage(target string) (string, bool) {
	target, _, _ = strings.Cut(target, "#")
	if strings.Contains(target, "://") {
		return "", false
	}
	page := path.Clean(target)
	if !filepath.IsLocal(page) || path.Ext(page) != ".md" || !isMemoryPagePath(page) {
		return "", false
	}
	return page, true
}

// resolveLegacyIndexPage is the listed page a legacy link names: the exact
// path, else the only listed path equal to it ignoring case, since on a
// case-insensitive filesystem a link's case need not match the page's.
// An ambiguous or missing link resolves to nothing.
func resolveLegacyIndexPage(link string, listed map[string]bool) (string, bool) {
	if listed[link] {
		return link, true
	}
	var match string
	for page := range listed {
		if strings.EqualFold(page, link) {
			if match != "" {
				return "", false
			}
			match = page
		}
	}
	return match, match != ""
}

// parseLegacyMemoryIndex reads a hand-written MEMORY.md: for each line naming
// a page, by a Markdown link or a bare path ending in .md, the description
// the rest of the line gives it. The first line naming a page wins.
func parseLegacyMemoryIndex(index string) map[string]string {
	out := make(map[string]string)
	for line := range strings.SplitSeq(index, "\n") {
		var target, text, rest string
		if m := legacyIndexLink.FindStringSubmatchIndex(line); m != nil {
			text, target = line[m[2]:m[3]], line[m[4]:m[5]]
			rest = line[:m[0]] + line[m[1]:]
		} else if m := legacyIndexBarePageMatch(line); m != nil {
			target = line[m[2]:m[3]]
			rest = line[:m[0]] + line[m[1]:]
		} else {
			continue
		}
		page, ok := legacyIndexPage(target)
		if _, seen := out[page]; !ok || seen {
			continue
		}
		source := trimLegacyIndexDescription(rest)
		if strings.TrimSpace(source) == "" {
			source = text
		}
		if description := strings.Join(strings.Fields(source), " "); description != "" {
			out[page] = description
		}
	}
	return out
}

// migrateMemoryScope moves a scope's hand-written index (see
// legacyMemoryIndexes) into its pages: each linked page with no description
// gets the one its index line gave, then the index is renamed to a free
// backup name (see freeMemoryBackupPath). It is idempotent and needs no
// lock: concurrent runs write the same descriptions, skip pages that have
// one, and the run that finds the index already renamed is done. A linked
// target that can't be read (missing, a directory, a refused symlink) is
// skipped. A page that fails to write does not stop the others; any failed
// write, or failing to read or rename the index, returns an error and leaves
// the index in place, so the next run finishes the job.
//
// Migration runs once per scope and the window between a page's read and its
// write is tiny; a concurrent edit landing in it would be overwritten, which is
// accepted.
func migrateMemoryScope(env *execenv.LocalExecutionEnvironment) error {
	root := env.WorkingDirectory()
	legacies, err := legacyMemoryIndexes(env)
	if err != nil || len(legacies) == 0 {
		return err
	}
	// Only regular page files are read or written; a FIFO, directory or
	// symlink named like a page is not listed and so is skipped.
	listed, err := listMemoryPages(env)
	if err != nil {
		return err
	}
	needsDescription := make(map[string]bool, len(listed))
	isListed := make(map[string]bool, len(listed))
	for _, p := range listed {
		needsDescription[p.Path] = !p.HasDescription && !p.Unreadable
		isListed[p.Path] = true
	}
	// Each link resolves to a listed page; within one index a link naming the
	// page exactly wins over one that differs from it only in case, and an
	// earlier index wins over a later one.
	descriptions := make(map[string]string)
	for _, legacy := range legacies {
		raw, err := env.ReadFileRaw(legacy)
		if err != nil {
			return err
		}
		fromIndex := make(map[string]string)
		for link, description := range parseLegacyMemoryIndex(string(raw)) {
			page, ok := resolveLegacyIndexPage(link, isListed)
			if _, taken := fromIndex[page]; ok && (link == page || !taken) {
				fromIndex[page] = description
			}
		}
		for page, description := range fromIndex {
			if _, taken := descriptions[page]; !taken {
				descriptions[page] = description
			}
		}
	}
	var writeErrs []error
	for _, page := range slices.Sorted(maps.Keys(descriptions)) {
		if !needsDescription[page] {
			continue
		}
		abs := filepath.Join(root, filepath.FromSlash(page))
		body, err := env.ReadFileRaw(abs)
		if err != nil {
			// Removed since the listing or unreadable, the page is skipped so the
			// scope still finishes.
			continue
		}
		if parsed := parseMemoryPage(page, body, time.Time{}); parsed.HasDescription || parsed.Unreadable {
			continue
		}
		described := setMemoryFrontmatterField(body, memoryYAMLField("description", descriptions[page]))
		// WriteFileRaw keeps an existing file's mode, so the 0o644 applies only to
		// new files, which migration never creates.
		if err := env.WriteFileRaw(abs, described, 0o644); err != nil {
			writeErrs = append(writeErrs, err)
		}
	}
	if len(writeErrs) > 0 {
		return errors.Join(writeErrs...)
	}
	for _, legacy := range legacies {
		if err := env.RenamePath(legacy, freeMemoryBackupPath(env, root)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// legacyMemoryIndexes is the scope's hand-written root indexes: every root
// entry other than a directory whose name is MEMORY.md ignoring case, as
// isMemoryPagePath excludes them all from the pages. The one named exactly
// MEMORY.md comes first. A case-insensitive filesystem holds at most one,
// under whatever case it was created with.
func legacyMemoryIndexes(env *execenv.LocalExecutionEnvironment) ([]string, error) {
	root := env.WorkingDirectory()
	entries, err := env.ListDirectory(root, 1)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, entry := range entries {
		if entry.IsDir || !strings.EqualFold(entry.Name, memoryIndexFile) {
			continue
		}
		legacy := filepath.Join(root, entry.Name)
		if entry.Name == memoryIndexFile {
			out = slices.Insert(out, 0, legacy)
		} else {
			out = append(out, legacy)
		}
	}
	return out, nil
}
