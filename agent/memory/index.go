package memory

import (
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/util"
)

type indexDocument struct {
	Source  string
	Entries []indexEntry
}
type indexEntry struct {
	PageID, Label, Summary string
	// Half-open source byte range for the complete physical row, including its
	// line ending. A final row without a newline ends at len(Source).
	Start, End int
	Line       int
}

var dateSuffix = regexp.MustCompile(` \(created ([0-9]{4}-[0-9]{2}-[0-9]{2}), updated ([0-9]{4}-[0-9]{2}-[0-9]{2}), reviewed ([0-9]{4}-[0-9]{2}-[0-9]{2}|never)\)$`)

// Extra closing delimiters still mark a terminal clause. Opening or internal
// delimiters require the full field list, so nested ordinary prose stays valid.
var trailingDateClause = regexp.MustCompile(` \(created(?:[ \t][^()\r\n]*)?\)*$| \(+created [^,\r\n]*, updated [^,\r\n]*, reviewed [^)\r\n]*\)*$`)

func parseIndex(source string) (indexDocument, error) {
	document := indexDocument{Source: source, Entries: []indexEntry{}}
	if !utf8.ValidString(source) {
		return indexDocument{}, invalidInput()
	}
	root, ranges := markdown(source)
	err := ast.Walk(root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		item, ok := n.(*ast.ListItem)
		if !ok || item.Parent().Parent() != root {
			return ast.WalkContinue, nil
		}
		// Only a top-level row with an active ordinary topic link is an entry.
		// Prose, headings, nested lists, reference links and code are not entries.
		position := -1
		err := ast.Walk(item, func(child ast.Node, entering bool) (ast.WalkStatus, error) {
			if !entering {
				return ast.WalkContinue, nil
			}
			if child != item {
				switch child.(type) {
				case *ast.ListItem, *ast.CodeSpan, *ast.CodeBlock, *ast.FencedCodeBlock, *ast.Image:
					return ast.WalkSkipChildren, nil
				}
			}
			if link, ok := child.(*ast.Link); ok {
				if span, ordinary := ranges[link]; ordinary {
					id, err := wikiDestination(string(link.Destination))
					if err != nil {
						return ast.WalkStop, err
					}
					if id != "" && id != "index" && (position < 0 || span.start < position) {
						position = span.start
					}
				}
				return ast.WalkSkipChildren, nil
			}
			return ast.WalkContinue, nil
		})
		if err != nil {
			return ast.WalkStop, err
		}
		if position < 0 {
			return ast.WalkContinue, nil
		}
		block := item.FirstChild()
		if block == nil || block.NextSibling() != nil || block.Lines().Len() != 1 {
			return ast.WalkStop, invalidInput()
		}
		start := strings.LastIndex(source[:position], "\n") + 1
		end := len(source)
		if i := strings.IndexByte(source[start:], '\n'); i >= 0 {
			end = start + i + 1
		}
		line := strings.TrimRight(source[start:end], " \t\r\n")
		if !strings.HasPrefix(line, "- [") {
			return ast.WalkStop, invalidInput()
		}
		close := -1
		for i := 3; i < len(line); i++ {
			if line[i] == '\\' && i+1 < len(line) && util.IsPunct(line[i+1]) {
				i++
				continue
			}
			if line[i] == ']' {
				close = i
				break
			}
		}
		if close < 0 || !strings.HasPrefix(line[close:], "](") {
			return ast.WalkStop, invalidInput()
		}
		tail := line[close+2:]
		boundary := strings.Index(tail, "): ")
		if boundary < 0 {
			return ast.WalkStop, invalidInput()
		}
		destination := tail[:boundary]
		if !strings.HasSuffix(destination, ".md") {
			return ast.WalkStop, invalidInput()
		}
		id := strings.TrimSuffix(destination, ".md")
		if !validPageID(id) {
			return ast.WalkStop, invalidInput()
		}
		summary := tail[boundary+3:]
		if match := dateSuffix.FindStringSubmatchIndex(summary); match != nil {
			for _, value := range []string{summary[match[2]:match[3]], summary[match[4]:match[5]], summary[match[6]:match[7]]} {
				if value != "never" {
					if _, err := time.Parse("2006-01-02", value); err != nil {
						return ast.WalkStop, invalidInput()
					}
				}
			}
			summary = summary[:match[0]]
		}
		// Only the trailing clause is reserved for dates. After removing one
		// valid suffix, another trailing clause is a repeated or malformed suffix.
		if trailingDateClause.MatchString(strings.TrimRight(summary, " \t")) {
			return ast.WalkStop, invalidInput()
		}
		label := strings.TrimSpace(decoded(line[3:close]))
		semanticSummary := strings.TrimSpace(decoded(summary))
		if label == "" || semanticSummary == "" {
			return ast.WalkStop, invalidInput()
		}
		document.Entries = append(document.Entries, indexEntry{PageID: id, Label: label, Summary: semanticSummary, Start: start, End: end, Line: 1 + strings.Count(source[:start], "\n")})
		return ast.WalkContinue, nil
	})
	if err != nil {
		return indexDocument{}, err
	}
	return document, nil
}

func renderIndex(document indexDocument, pages map[string]PageMeta) string {
	var out strings.Builder
	offset := 0
	for _, entry := range document.Entries {
		meta := pages[entry.PageID]
		reviewed := "never"
		if meta.Reviewed != nil {
			reviewed = meta.Reviewed.UTC().Format("2006-01-02")
		}
		row := strings.TrimRight(document.Source[entry.Start:entry.End], " \t\r\n")
		suffixStart := len(row)
		if match := dateSuffix.FindStringIndex(row); match != nil {
			suffixStart = match[0]
		}
		out.WriteString(document.Source[offset : entry.Start+suffixStart])
		out.WriteString(" (created " + meta.Created.UTC().Format("2006-01-02") + ", updated " + meta.Updated.UTC().Format("2006-01-02") + ", reviewed " + reviewed + ")")
		offset = entry.Start + len(row)
	}
	out.WriteString(document.Source[offset:])
	return out.String()
}
