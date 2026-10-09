package agent

import (
	"errors"
	"fmt"
	"io/fs"
	"iter"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/frontmatter"
	"primeradiant.com/evener/agent/internal/tool"
)

// memoryPage is one page of a memory scope, as its generated index line needs it.
type memoryPage struct {
	Path           string // slash-separated, relative to the scope root
	Title          string
	Description    string // one line; the fallback when HasDescription is false
	HasDescription bool
	Unreadable     bool // the page's frontmatter did not parse
	Frontmatter    bool // the page opens with a complete frontmatter block, readable or not
	Tags           []string
	Updated        string    // the YYYY-MM-DD stamp, "" when there is none
	By             string    // the by: stamp, who last wrote the page; "" when there is none
	ModTime        time.Time // orders a page that has no stamp
}

const (
	memoryNoDescription         = "(no description)"
	memoryFrontmatterUnreadable = "(frontmatter unreadable)"
	// memoryFallbackRunes bounds a fallback description, in characters.
	memoryFallbackRunes = 120
	// memoryPageWalkDepth bounds how deep a scope's pages are listed.
	memoryPageWalkDepth = 64
)

// isMemoryPagePath reports whether rel, a slash path relative to the scope
// root, is a page: no segment starts with "." (memory_search's rule) and it
// is not the root MEMORY.md, which is generated. The root name is matched
// without regard to case: on a case-insensitive filesystem memory.md is the
// same file.
func isMemoryPagePath(rel string) bool {
	return !isMemoryIndexPath(rel) && !execenv.IsDotPath(rel)
}

// matchMemoryNameCase is the name in names equal to name, else the only one
// equal to it ignoring case, since on a case-insensitive filesystem a name's
// case need not match the file's. An ambiguous or missing name matches
// nothing.
func matchMemoryNameCase(name string, names iter.Seq[string]) (string, bool) {
	var match string
	folded := 0
	for candidate := range names {
		if candidate == name {
			return candidate, true
		}
		if strings.EqualFold(candidate, name) {
			match = candidate
			folded++
		}
	}
	if folded != 1 {
		return "", false
	}
	return match, true
}

// listedMemoryPagePath is the slash path the scope lists the file at rel
// under, which on a case-insensitive filesystem can differ from rel's case:
// rel with each segment in its directory's case (matchMemoryNameCase). From
// the first directory that can't be listed, such as one a write is about to
// create, the rest of rel stays as it is.
func listedMemoryPagePath(env *execenv.LocalExecutionEnvironment, rel string) string {
	listed := ""
	segments := strings.Split(rel, "/")
	for i, segment := range segments {
		entries, err := env.ListDirectory(filepath.Join(env.WorkingDirectory(), filepath.FromSlash(listed)), 1)
		if err != nil {
			return path.Join(append([]string{listed}, segments[i:]...)...)
		}
		names := func(yield func(string) bool) {
			for _, entry := range entries {
				if !yield(entry.Name) {
					return
				}
			}
		}
		if match, ok := matchMemoryNameCase(segment, names); ok {
			segment = match
		}
		listed = path.Join(listed, segment)
	}
	return listed
}

// splitMemoryFrontmatter splits text into its frontmatter block and body
// exactly as frontmatter.Parse does, so a page whose YAML fails to parse
// still has a body to fall back on.
func splitMemoryFrontmatter(text string) (block, body string, ok bool) {
	return frontmatter.Split(text)
}

// filenameMemoryPage is the entry for a file whose content says nothing: a
// non-Markdown file, or a page that could not be read.
func filenameMemoryPage(rel string, modTime time.Time) memoryPage {
	base := path.Base(rel)
	return memoryPage{Path: rel, Title: strings.TrimSuffix(base, path.Ext(base)), Description: base, ModTime: modTime}
}

// parseMemoryPage builds the index entry for the page at rel from its bytes.
func parseMemoryPage(rel string, raw []byte, modTime time.Time) memoryPage {
	p := filenameMemoryPage(rel, modTime)
	if path.Ext(rel) != ".md" {
		return p
	}
	text := string(raw)
	doc, err := frontmatter.Parse(text)
	body := doc.Body
	_, splitBody, hasBlock := splitMemoryFrontmatter(text)
	if err != nil {
		p.Unreadable = true
		body = splitBody
	}
	// The block's presence, not its parsed value: a block holding only a YAML
	// null parses to no metadata but is still frontmatter.
	p.Frontmatter = hasBlock
	heading := firstMarkdownHeading(body)
	if heading != "" {
		p.Title = heading
	}
	if description, ok := doc.Meta["description"].(string); ok {
		p.Description = strings.Join(strings.Fields(description), " ")
		p.HasDescription = p.Description != ""
	}
	if !p.HasDescription {
		p.Description = memoryFallbackDescription(heading, body)
	}
	p.Tags = normalizeMemoryTags(doc.Meta["tags"])
	p.Updated = memoryStampDate(doc.Meta["updated"])
	p.By, _ = doc.Meta["by"].(string)
	return p
}

// firstMarkdownHeading returns the text of body's first ATX heading outside a
// fenced code block, without closing hashes, or "".
func firstMarkdownHeading(body string) string {
	var fence string // the open fence's run ("```", "~~~~", ...), "" outside one
	for line := range strings.SplitSeq(body, "\n") {
		text := strings.TrimLeft(line, " ")
		if len(line)-len(text) > 3 {
			continue // indented code
		}
		if run := fenceRun(text); run != "" {
			switch {
			case fence == "":
				fence = run
			case run[0] == fence[0] && len(run) >= len(fence) && strings.TrimSpace(text[len(run):]) == "":
				fence = ""
			}
			continue
		}
		if fence != "" {
			continue
		}
		trimmed := strings.TrimLeft(text, "#")
		level := len(text) - len(trimmed)
		if level >= 1 && level <= 6 && (trimmed == "" || trimmed[0] == ' ' || trimmed[0] == '\t') {
			heading := strings.TrimSpace(trimmed)
			if stripped := strings.TrimRight(heading, "#"); stripped != heading && (stripped == "" || strings.HasSuffix(stripped, " ") || strings.HasSuffix(stripped, "\t")) {
				heading = strings.TrimSpace(stripped)
			}
			if heading != "" {
				return heading
			}
		}
	}
	return ""
}

// fenceRun returns the leading run of three or more backticks or tildes in
// line, which opens or closes a code fence, or "". A backtick run followed by
// another backtick on the line is no fence (CommonMark: a backtick fence's
// info string holds no backtick), so "```a`b" is inline code.
func fenceRun(line string) string {
	if line == "" || (line[0] != '`' && line[0] != '~') {
		return ""
	}
	run := line[:len(line)-len(strings.TrimLeft(line, line[:1]))]
	if len(run) < 3 || (run[0] == '`' && strings.Contains(line[len(run):], "`")) {
		return ""
	}
	return run
}

// memoryFallbackDescription is the description of a page without one: its
// first heading, else its first non-blank line, cut to memoryFallbackRunes
// characters and marked as having no description.
func memoryFallbackDescription(heading, body string) string {
	text := heading
	if text == "" {
		for line := range strings.SplitSeq(body, "\n") {
			if text = strings.TrimSpace(line); text != "" {
				break
			}
		}
	}
	text = tool.TruncateRunes(text, memoryFallbackRunes)
	if text == "" {
		return memoryNoDescription
	}
	return text + " " + memoryNoDescription
}

// normalizeMemoryTags reads a tags value: a list, or a single string as one
// tag. Each tag is trimmed and lowercased, runs of whitespace become "-",
// and empty tags and duplicates are dropped.
func normalizeMemoryTags(v any) []string {
	var items []any
	switch value := v.(type) {
	case string:
		items = []any{value}
	case []any:
		items = value
	default:
		return nil
	}
	var tags []string
	for _, item := range items {
		var text string
		switch scalar := item.(type) {
		case string:
			text = scalar
		case int, float64, bool:
			text = fmt.Sprint(scalar)
		default:
			continue
		}
		tag := strings.Join(strings.Fields(strings.ToLower(text)), "-")
		if tag != "" && !slices.Contains(tags, tag) {
			tags = append(tags, tag)
		}
	}
	return tags
}

// memoryStampDate reads an updated value: a YAML date or a YYYY-MM-DD string.
func memoryStampDate(v any) string {
	switch value := v.(type) {
	case time.Time:
		return value.UTC().Format(time.DateOnly)
	case string:
		if _, err := time.Parse(time.DateOnly, value); err == nil {
			return value
		}
	}
	return ""
}

// listMemoryPages reads every page of env's scope. Only regular files on
// page paths are listed; a page removed since the listing is skipped,
// and one that cannot be read is listed by its filename.
func listMemoryPages(env *execenv.LocalExecutionEnvironment) ([]memoryPage, error) {
	root := env.WorkingDirectory()
	entries, err := env.ListDirectory(root, memoryPageWalkDepth)
	if err != nil {
		return nil, err
	}
	var pages []memoryPage
	for _, entry := range entries {
		rel := filepath.ToSlash(entry.Name)
		if !entry.IsRegular || !isMemoryPagePath(rel) {
			continue
		}
		if path.Ext(rel) != ".md" {
			pages = append(pages, filenameMemoryPage(rel, entry.ModTime))
			continue
		}
		raw, err := env.ReadFileRaw(filepath.Join(root, entry.Name))
		if page, ok := memoryPageFromRead(rel, raw, err, entry.ModTime); ok {
			pages = append(pages, page)
		}
	}
	return pages, nil
}

// memoryPageFromRead is the index entry for a read of the page at rel that
// returned raw and err, reporting false when the page does not exist. A page
// that cannot be read is its filename entry.
func memoryPageFromRead(rel string, raw []byte, err error, modTime time.Time) (memoryPage, bool) {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return memoryPage{}, false
	case err != nil:
		return filenameMemoryPage(rel, modTime), true
	}
	return parseMemoryPage(rel, raw, modTime), true
}
