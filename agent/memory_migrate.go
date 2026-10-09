package agent

import (
	"bytes"
	"cmp"
	"crypto/rand"
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
	"primeradiant.com/evener/agent/internal/frontmatter"
)

// memoryLegacyIndexBackup is where migration keeps a scope's hand-written
// index. Its dot name keeps it out of the pages and out of memory_search.
const memoryLegacyIndexBackup = ".MEMORY.md.pre-generated"

// freeMemoryBackupPath is the first backup name that no entry in root has (a
// symlink, even a dangling one, counts), compared ignoring case since on a
// case-insensitive filesystem names differing in case are one file:
// memoryLegacyIndexBackup, then the same name with ".2", ".3" and so on. An
// older Evener build can write MEMORY.md again after migration; numbering
// keeps the first backup, the one holding the original index, from being
// overwritten when that file is migrated too. The name can be taken by the
// time it is used; moveLegacyMemoryIndex never relies on it being free.
func freeMemoryBackupPath(env *execenv.LocalExecutionEnvironment, root string) (string, error) {
	entries, err := env.ListDirectory(root, 1)
	if err != nil {
		return "", err
	}
	taken := make(map[string]bool, len(entries))
	for _, entry := range entries {
		taken[strings.ToLower(entry.Name)] = true
	}
	backup := memoryLegacyIndexBackup
	for n := 2; taken[strings.ToLower(backup)]; n++ {
		backup = memoryLegacyIndexBackup + "." + strconv.Itoa(n)
	}
	return filepath.Join(root, backup), nil
}

// errLegacyMemoryIndexChanged reports that a hand-written index changed
// after migration read it, so it stays in place for the next run.
var errLegacyMemoryIndexChanged = errors.New("hand-written memory index changed during migration; it stays for the next run")

// memoryIndexStagingPrefix starts the private name a migration moves
// MEMORY.md to while it checks the bytes. It is a dot name, so never a page,
// and differs from memoryLegacyIndexBackup, so never taken for a backup.
const memoryIndexStagingPrefix = ".MEMORY.md.migrating-"

// moveLegacyMemoryIndex moves the migrated hand-written index at legacy,
// whose bytes the migration read as raw, to a backup in root, trying the
// name backup first (see backUpMemoryIndex), then removes MEMORY.md only if
// it still holds raw (see removeMigratedMemoryIndex). No lock guards the
// move: another migration, or an older build writing MEMORY.md again, may
// change the scope at any point, and neither step relies on it holding
// still. A failure leaves MEMORY.md in place for the next run.
func moveLegacyMemoryIndex(env *execenv.LocalExecutionEnvironment, root, legacy string, raw []byte, backup string) error {
	if err := backUpMemoryIndex(env, root, legacy, raw, backup); err != nil {
		// fs.ErrNotExist: another migration moved the index first.
		return ignoreNotExist(err)
	}
	return removeMigratedMemoryIndex(env, root, legacy, raw)
}

// backUpMemoryIndex gives the file at src, expected to hold raw, a backup
// name in root, trying backup first, unless a backup already holds raw byte
// for byte, so alternating builds that write the same index add nothing. A
// backup is never replaced or removed, since each holds what a person or an
// older build wrote: the file is hard-linked to the name, which fails rather
// than replace a file put there since the name was chosen, and a taken name
// moves on to the next free one. Any other link failure, such as a
// filesystem without hard links, is returned. A link to bytes other than raw
// backs up nothing migrated, so it is dropped (src still holds them) and
// errLegacyMemoryIndexChanged returned.
func backUpMemoryIndex(env *execenv.LocalExecutionEnvironment, root, src string, raw []byte, backup string) error {
	for {
		// Checked again after a taken name: another migration may have just
		// backed up the same bytes.
		if repeated, err := memoryBackupHolds(env, root, raw); err != nil || repeated {
			return err
		}
		err := env.LinkConfinedFile(src, backup)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrExist) {
			return err
		}
		if backup, err = freeMemoryBackupPath(env, root); err != nil {
			return err
		}
	}
	if moved, err := env.ReadFileRaw(backup); err != nil || !bytes.Equal(moved, raw) {
		return cmp.Or(errors.Join(err, env.RemoveConfinedFile(backup)), errLegacyMemoryIndexChanged)
	}
	return nil
}

// removeMigratedMemoryIndex removes the index at legacy only if it holds
// raw. A read followed by an unlink could delete a rewrite landing between
// the two, and no filesystem call removes a name only if its contents match.
// So whatever legacy names is first renamed, atomically, to a name only this
// run uses: a rewrite either lands before the rename and is captured, or
// after it, as a new MEMORY.md that stays. The captured file is removed only
// if it holds raw. Otherwise it goes back to MEMORY.md, or, when MEMORY.md
// was written yet again meanwhile, to a backup of its own; either way
// errLegacyMemoryIndexChanged reports that an index is left for the next
// run. If even that fails, the captured file keeps its private name.
func removeMigratedMemoryIndex(env *execenv.LocalExecutionEnvironment, root, legacy string, raw []byte) error {
	staged := filepath.Join(root, memoryIndexStagingPrefix+rand.Text())
	if err := env.RenamePath(legacy, staged); err != nil {
		return ignoreNotExist(err)
	}
	captured, err := env.ReadFileRaw(staged)
	if err == nil && bytes.Equal(captured, raw) {
		return env.RemoveConfinedFile(staged)
	}
	if err != nil {
		return err
	}
	restored := env.LinkConfinedFile(staged, legacy)
	if errors.Is(restored, fs.ErrExist) {
		backup, err := freeMemoryBackupPath(env, root)
		if err != nil {
			return err
		}
		restored = backUpMemoryIndex(env, root, staged, captured, backup)
	}
	if restored != nil {
		return restored
	}
	return errors.Join(env.RemoveConfinedFile(staged), errLegacyMemoryIndexChanged)
}

// memoryBackupHolds reports whether a regular file in root named like a
// backup (memoryLegacyIndexBackup, numbered or not, in any case) holds
// exactly raw.
func memoryBackupHolds(env *execenv.LocalExecutionEnvironment, root string, raw []byte) (bool, error) {
	entries, err := env.ListDirectory(root, 1)
	if err != nil {
		return false, err
	}
	prefix := strings.ToLower(memoryLegacyIndexBackup)
	for _, entry := range entries {
		if !entry.IsRegular || entry.Size != int64(len(raw)) || !strings.HasPrefix(strings.ToLower(entry.Name), prefix) {
			continue
		}
		if existing, err := env.ReadFileRaw(filepath.Join(root, entry.Name)); err == nil && bytes.Equal(existing, raw) {
			return true, nil
		}
	}
	return false, nil
}

// ignoreNotExist is err, or nil when err says a file was already gone.
func ignoreNotExist(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
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
// or with a space before the colon); a block that does not parse gains the
// line at its end and stays as unreadable as it was (frontmatter.Parse
// reads only a mapping). Readable frontmatter that would not read the line
// back as its key, such as a flow mapping or a block
// with a "..." document end, is returned as it is.
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
	out := "---\n" + strings.Join(kept, "") + "---\n" + body
	// Checked by reading the page back as pages are read, rather than
	// predicted: the line can break a flow mapping, or land after a document
	// end YAML never reads past.
	if _, err := frontmatter.Parse(text); err == nil {
		doc, err := frontmatter.Parse(out)
		if _, set := doc.Meta[key]; err != nil || !set {
			return raw
		}
	}
	return []byte(out)
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
// gets the one its index line gave, then the index is moved to a backup
// (see moveLegacyMemoryIndex). It is idempotent and takes no lock (the write
// loop and moveLegacyMemoryIndex say why), and the run that finds the index
// already moved is done. A linked target that can't be read (missing, a directory, a
// refused symlink), or whose frontmatter does not parse or can't take the
// description in place, is skipped. A page that fails to write does not stop
// the others; any failed write, or failing to read or move the index,
// returns an error and leaves the index in place, so the next run finishes
// the job.
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
	// read is each index that still existed, with the bytes migrated from it.
	type readIndex struct {
		path string
		raw  []byte
	}
	var read []readIndex
	for _, legacy := range legacies {
		raw, err := env.ReadFileRaw(legacy)
		if errors.Is(err, fs.ErrNotExist) {
			// A concurrent run moved it after the listing, so it is migrated.
			continue
		}
		if err != nil {
			return err
		}
		read = append(read, readIndex{legacy, raw})
		entries := parseLegacyMemoryIndex(string(raw))
		for _, exact := range []bool{true, false} {
			for _, entry := range entries {
				page, ok := matchMemoryNameCase(entry.Link, maps.Keys(isListed))
				if _, taken := descriptions[page]; ok && !taken && (entry.Link == page) == exact {
					descriptions[page] = entry.Description
				}
			}
		}
	}
	// No lock guards these read-then-write pairs. Each page is re-read just
	// before its write and written only when it still has no description, so
	// a page another session described meanwhile is left alone, and two
	// migrations racing write the same description. A page edit landing in
	// the instant between one page's read and write would be overwritten;
	// that is accepted, because migration runs once per scope (the index is
	// moved when it finishes) and touches only pages with no description.
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
		// ended by "...") comes back without the description; such a page is
		// left as it is, like one whose frontmatter does not parse, and the
		// index is still moved. Keeping the index for it instead would retry
		// every run until someone rewrites the page. The description stays
		// recoverable in the backup, and the page renders with its fallback
		// description meanwhile.
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
	for _, index := range read {
		backup, err := freeMemoryBackupPath(env, root)
		if err != nil {
			return err
		}
		if err := moveLegacyMemoryIndex(env, root, index.path, index.raw, backup); err != nil {
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
		if !entry.IsRegular || !isMemoryIndexPath(entry.Name) {
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
