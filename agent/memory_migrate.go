package agent

import (
	"errors"
	"io/fs"
	"maps"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"primeradiant.com/evener/agent/execenv"
)

// memoryLegacyIndexBackup is where migration keeps a scope's hand-written
// index. Its dot name keeps it out of the pages and out of memory_search.
const memoryLegacyIndexBackup = ".MEMORY.md.pre-generated"

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
	for existing := range strings.Lines(block) {
		if skipping && (strings.HasPrefix(existing, " ") || strings.HasPrefix(existing, "\t") || strings.HasPrefix(existing, "-")) {
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

var (
	legacyIndexLink     = regexp.MustCompile(`\[([^\]]*)\]\(([^)\s]+)\)`)
	legacyIndexBarePage = regexp.MustCompile(`[` + "`" + `*]*([^\s\[\]()` + "`" + `*]+\.md)\b[` + "`" + `*]*`)
)

// A description loses list markers at its start and separators at both ends.
// Markers are trimmed from the left only so "learned C++" stays intact.
const (
	legacyIndexLeadingTrim = " \t-*+—–:"
	legacyIndexTrim        = " \t-—–:"
)

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
		} else if m := legacyIndexBarePage.FindStringSubmatchIndex(line); m != nil {
			target = line[m[2]:m[3]]
			rest = line[:m[0]] + line[m[1]:]
		} else {
			continue
		}
		page, ok := legacyIndexPage(target)
		if _, seen := out[page]; !ok || seen {
			continue
		}
		source := strings.TrimRight(strings.TrimLeft(rest, legacyIndexLeadingTrim), legacyIndexTrim)
		if strings.TrimSpace(source) == "" {
			source = text
		}
		if description := strings.Join(strings.Fields(source), " "); description != "" {
			out[page] = description
		}
	}
	return out
}

// migrateMemoryScope moves a scope's hand-written index into its pages: each
// linked page with no description gets the one its index line gave, then the
// index is renamed to memoryLegacyIndexBackup. It is idempotent and needs no
// lock: concurrent runs write the same descriptions, skip pages that have
// one, and the run that finds the index already renamed is done. A linked
// target that can't be read (missing, a directory, a refused symlink) is
// skipped. Failing to write a real page, or to read or rename the index,
// returns an error and leaves the index in place, so the next run finishes the
// job.
//
// Migration runs once per scope and the window between a page's read and its
// write is tiny; a concurrent edit landing in it would be overwritten, which is
// accepted.
func migrateMemoryScope(env *execenv.LocalExecutionEnvironment) error {
	root := env.WorkingDirectory()
	legacy := filepath.Join(root, memoryIndexFile)
	raw, err := env.ReadFileRaw(legacy)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	descriptions := parseLegacyMemoryIndex(string(raw))
	for _, page := range slices.Sorted(maps.Keys(descriptions)) {
		abs := filepath.Join(root, filepath.FromSlash(page))
		body, err := env.ReadFileRaw(abs)
		if err != nil {
			// Missing or unreadable, the page is skipped so the scope still finishes.
			continue
		}
		if parsed := parseMemoryPage(page, body, time.Time{}); parsed.HasDescription || parsed.Unreadable {
			continue
		}
		described := setMemoryFrontmatterField(body, memoryYAMLField("description", descriptions[page]))
		// WriteFileRaw keeps an existing file's mode, so the 0o644 applies only to
		// new files, which migration never creates.
		if err := env.WriteFileRaw(abs, described, 0o644); err != nil {
			return err
		}
	}
	if err := env.RenamePath(legacy, filepath.Join(root, memoryLegacyIndexBackup)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
