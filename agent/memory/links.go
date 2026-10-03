package memory

import (
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

type linkRange struct{ start, end int }

// Goldmark uses ast.Link for both inline and reference-style links. Record
// ordinary inline origins while its real link parser consumes source, including
// empty or formatted labels whose children cannot supply the opening offset.
type sourceLinkParser struct {
	parser.InlineParser
	openings map[ast.Node]int
	links    map[*ast.Link]linkRange
}

func (p *sourceLinkParser) Parse(parent ast.Node, reader text.Reader, pc parser.Context) ast.Node {
	_, pos := reader.Position()
	source := reader.Source()
	start := -1
	if source[pos.Start] == ']' {
		for n := parent.LastChild(); n != nil; n = n.PreviousSibling() {
			if offset, ok := p.openings[n]; ok {
				start = offset
				break
			}
		}
	}
	n := p.InlineParser.Parse(parent, reader, pc)
	if n != nil && (source[pos.Start] == '[' || source[pos.Start] == '!') {
		p.openings[n] = pos.Start
	}
	if link, ok := n.(*ast.Link); ok && start >= 0 && pos.Start+1 < len(source) && source[pos.Start+1] == '(' {
		_, end := reader.Position()
		if end.Start > pos.Start+1 {
			p.links[link] = linkRange{start: start, end: end.Start}
		}
	}
	return n
}
func (p *sourceLinkParser) CloseBlock(parent ast.Node, reader text.Reader, pc parser.Context) {
	p.InlineParser.(parser.CloseBlocker).CloseBlock(parent, reader, pc)
	clear(p.openings)
}
func markdown(source string) (ast.Node, map[*ast.Link]linkRange) {
	links := &sourceLinkParser{InlineParser: parser.NewLinkParser(), openings: map[ast.Node]int{}, links: map[*ast.Link]linkRange{}}
	inlines := parser.DefaultInlineParsers()
	for i := range inlines {
		if inlines[i].Value == links.InlineParser {
			inlines[i].Value = links
		}
	}
	p := parser.NewParser(parser.WithBlockParsers(parser.DefaultBlockParsers()...), parser.WithInlineParsers(inlines...), parser.WithParagraphTransformers(parser.DefaultParagraphTransformers()...))
	return p.Parse(text.NewReader([]byte(source))), links.links
}
func decoded(s string) string {
	return string(util.ResolveEntityNames(util.ResolveNumericReferences(util.UnescapePunctuations([]byte(s)))))
}

var uriScheme = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)

// wikiDestination returns a validated topic ID, index, or empty for inert
// citations. It never resolves a destination on disk or fetches a URL.
func wikiDestination(destination string) (string, error) {
	path, _, _ := strings.Cut(destination, "#")
	plain, _, _ := strings.Cut(decoded(destination), "#")
	windowsPath := len(plain) >= 3 && plain[1] == ':' && (plain[2] == '/' || plain[2] == '\\')
	if uriScheme.MatchString(plain) && !windowsPath && !strings.HasPrefix(strings.ToLower(plain), "file:") {
		return "", nil
	}
	unescaped, err := url.PathUnescape(plain)
	if err != nil {
		if strings.Contains(strings.ToLower(plain), ".md") {
			return "", invalidInput()
		}
		return "", nil
	}
	isMarkdown := strings.Contains(strings.ToLower(unescaped), ".md") || strings.Contains(strings.ToLower(path), ".md")
	if !isMarkdown {
		return "", nil
	}
	if path != plain || plain != unescaped || !strings.HasSuffix(path, ".md") {
		return "", invalidInput()
	}
	id := strings.TrimSuffix(path, ".md")
	if id == "index" {
		return id, nil
	}
	if !validPageID(id) {
		return "", invalidInput()
	}
	return id, nil
}

func localLinks(source string) ([]Reference, error) {
	if !utf8.ValidString(source) {
		return nil, invalidInput()
	}
	root, ranges := markdown(source)
	references := []Reference{}
	err := ast.Walk(root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n.(type) {
		case *ast.CodeSpan, *ast.CodeBlock, *ast.FencedCodeBlock, *ast.Image:
			return ast.WalkSkipChildren, nil
		}
		link, ok := n.(*ast.Link)
		if !ok {
			return ast.WalkContinue, nil
		}
		span, ordinary := ranges[link]
		if !ordinary {
			return ast.WalkSkipChildren, nil
		}
		destination := string(link.Destination)
		id, err := wikiDestination(destination)
		if err != nil {
			return ast.WalkStop, err
		}
		if id != "" {
			references = append(references, Reference{Destination: destination, Line: 1 + strings.Count(source[:span.start], "\n")})
		}
		return ast.WalkSkipChildren, nil
	})
	if err != nil {
		return nil, err
	}
	return references, nil
}
