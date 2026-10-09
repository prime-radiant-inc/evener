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

// freeMemoryBackupPath is the first backup name that no entry in root has (a
// symlink, even a dangling one, counts): memoryLegacyIndexBackup, then the same name with ".2", ".3" and so on. An
// older Evener build can write MEMORY.md again after migration; numbering
// keeps the first backup, the one holding the original index, from being
// overwritten when that file is migrated too.
//
// The name is checked, then renamed onto, and execenv has no rename that
// refuses to replace. That is safe between migrators: concurrent runs list the
// same indexes and rename them in the same order, so of two runs picking one
// name for one index only one rename finds its source and the other gets
// fs.ErrNotExist. A backup is lost only if an older build writes MEMORY.md
// again between one run's rename and another's earlier check; that window is
// accepted, since the lost index's descriptions are already in its pages.
func freeMemoryBackupPath(env *execenv.LocalExecutionEnvironment, root string) (string, error) {
	entries, err := env.ListDirectory(root, 1)
	if err != nil {
		return "", err
	}
	taken := make(map[string]bool, len(entries))
	for _, entry := range entries {
		taken[entry.Name] = true
	}
	backup := memoryLegacyIndexBackup
	for n := 2; taken[backup]; n++ {
		backup = memoryLegacyIndexBackup + "." + strconv.Itoa(n)
	}
	return filepath.Join(root, backup), nil
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
// only top-level entry for its key, replacing an existing one and the lines of
// its value, or adding it at the end of the block, or adding a block. Every
// other byte of the page is kept. Keys are found as YAML reads them (quoted,
// or with a space before the colon); a block YAML can't read as a mapping
// gains the line at its end.
func setMemoryFrontmatterField(raw []byte, line string) []byte {
	key, _, _ := strings.Cut(line, ":")
	text := string(raw)
	block, body, ok := splitMemoryFrontmatter(text)
	if !ok {
		return []byte("---\n" + line + "---\n" + text)
	}
	keys := memoryFrontmatterKeyLines(block)
	lines := slices.Collect(strings.Lines(block))
	var kept []string
	replaced := false
	for i := 0; i < len(lines); i++ {
		if k, isKey := keys[i]; !isKey || k != key {
			kept = append(kept, lines[i])
			continue
		}
		if !replaced {
			kept = append(kept, line)
			replaced = true
		}
		// The value runs to the next key; blank and comment lines ending it
		// belong to what follows and are kept.
		next := i + 1
		for ; next < len(lines); next++ {
			if _, isKey := keys[next]; isKey {
				break
			}
		}
		spacers := next
		for spacers > i+1 && (strings.TrimSpace(lines[spacers-1]) == "" || strings.HasPrefix(lines[spacers-1], "#")) {
			spacers--
		}
		kept = append(kept, lines[spacers:next]...)
		i = next - 1
	}
	// splitMemoryFrontmatter's block is empty or ends in a newline, so the
	// line can be appended as it is.
	if !replaced {
		kept = append(kept, line)
	}
	return []byte("---\n" + strings.Join(kept, "") + "---\n" + body)
}

// memoryFrontmatterKeyLines maps the 0-based line of each top-level key in a
// block-style frontmatter mapping to the key as YAML reads it, or is nil when
// the block is not one.
func memoryFrontmatterKeyLines(block string) map[int]string {
	var doc yaml.Node
	if yaml.Unmarshal([]byte(block), &doc) != nil || len(doc.Content) == 0 {
		return nil
	}
	mapping := doc.Content[0]
	if mapping.Kind != yaml.MappingNode || mapping.Style&yaml.FlowStyle != 0 {
		return nil
	}
	keys := make(map[int]string, len(mapping.Content)/2)
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		keys[mapping.Content[i].Line-1] = mapping.Content[i].Value
	}
	return keys
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

// legacyIndexEntry is one line of a hand-written index: the page path it
// links to and the description it gives that page.
type legacyIndexEntry struct {
	Link, Description string
}

// parseLegacyMemoryIndex reads a hand-written MEMORY.md: for each line naming
// a page, by a Markdown link or a bare path ending in .md, the description
// the rest of the line gives it, in line order. The first line naming a page
// wins.
func parseLegacyMemoryIndex(index string) []legacyIndexEntry {
	var out []legacyIndexEntry
	seen := make(map[string]bool)
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
		if !ok || seen[page] {
			continue
		}
		source := trimLegacyIndexDescription(rest)
		if strings.TrimSpace(source) == "" {
			source = text
		}
		if description := strings.Join(strings.Fields(source), " "); description != "" {
			out = append(out, legacyIndexEntry{Link: page, Description: description})
			seen[page] = true
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
// target that can't be read (missing, a directory, a refused symlink), or
// whose frontmatter does not parse or can't take the description in place, is
// skipped. A page that fails to write does not stop the others; any failed
// write, or failing to read or rename the index, returns an error and leaves
// the index in place, so the next run finishes the job.
//
// Migration runs once per scope and the window between a page's read and its
// write is tiny; a concurrent edit landing in it would be overwritten, which is
// accepted.
func migrateMemoryScope(env *execenv.LocalExecutionEnvironment) error {
	legacies, err := legacyMemoryIndexes(env)
	if err != nil || len(legacies) == 0 {
		return err
	}
	return migrateLegacyMemoryIndexes(env, legacies)
}

// migrateLegacyMemoryIndexes is migrateMemoryScope's work for the root
// indexes legacyMemoryIndexes found, in that order.
func migrateLegacyMemoryIndexes(env *execenv.LocalExecutionEnvironment, legacies []string) error {
	root := env.WorkingDirectory()
	// Only regular page files are read or written; a FIFO, directory or
	// symlink named like a page is not listed and so is skipped.
	listed, err := listMemoryPages(env)
	if err != nil {
		return err
	}
	isListed := make(map[string]bool, len(listed))
	for _, p := range listed {
		isListed[p.Path] = true
	}
	// Each link resolves to a listed page, and the first description a page
	// gets is kept: an earlier index wins over a later one, and within one
	// index a link naming the page exactly wins over the earliest line naming
	// it in another case.
	descriptions := make(map[string]string)
	for _, legacy := range legacies {
		raw, err := env.ReadFileRaw(legacy)
		if errors.Is(err, fs.ErrNotExist) {
			// A concurrent run renamed it after the listing, so it is migrated.
			continue
		}
		if err != nil {
			return err
		}
		entries := parseLegacyMemoryIndex(string(raw))
		for _, exact := range []bool{true, false} {
			for _, entry := range entries {
				page, ok := resolveLegacyIndexPage(entry.Link, isListed)
				if _, taken := descriptions[page]; ok && !taken && (entry.Link == page) == exact {
					descriptions[page] = entry.Description
				}
			}
		}
	}
	var writeErrs []error
	for _, page := range slices.Sorted(maps.Keys(descriptions)) {
		abs := filepath.Join(root, filepath.FromSlash(page))
		body, err := env.ReadFileRaw(abs)
		if err != nil {
			// Removed since the listing or unreadable, the page is skipped so the
			// scope still finishes.
			continue
		}
		// A page that has a description keeps it; one whose frontmatter does not
		// parse is left as it is.
		if parsed := parseMemoryPage(page, body, time.Time{}); parsed.HasDescription || parsed.Unreadable {
			continue
		}
		described := setMemoryFrontmatterField(body, memoryYAMLField("description", descriptions[page]))
		// Frontmatter the editor can't extend in place (a flow mapping, a block
		// ended by "...") would no longer read back; such a page is left as it
		// is, like one whose frontmatter does not parse.
		if parsed := parseMemoryPage(page, described, time.Time{}); !parsed.HasDescription || parsed.Description != descriptions[page] {
			continue
		}
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
		backup, err := freeMemoryBackupPath(env, root)
		if err != nil {
			return err
		}
		if err := env.RenamePath(legacy, backup); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// legacyMemoryIndexes is the scope's hand-written root indexes: every regular
// root file whose name is MEMORY.md ignoring case, as isMemoryPagePath
// excludes them all from the pages. Like a page, an index that is not a
// regular file (a symlink, FIFO or directory) is skipped. The one named exactly
// MEMORY.md comes first. A case-insensitive filesystem holds at most one,
// under whatever case it was created with.
func legacyMemoryIndexes(env *execenv.LocalExecutionEnvironment) ([]string, error) {
	root := env.WorkingDirectory()
	entries, err := env.ListDirectory(root, 1)
	if errors.Is(err, fs.ErrNotExist) {
		// A scope directory that does not exist (or, on Linux, was removed under
		// the environment's open handle) holds no index.
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, entry := range entries {
		if !entry.IsRegular || !strings.EqualFold(entry.Name, memoryIndexFile) {
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
