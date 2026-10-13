package agent

import (
	"errors"
	"strings"

	"primeradiant.com/evener/agent/internal/frontmatter"
)

var (
	errMemoryWriteNeedsWhen  = errors.New("memory_write needs a when: one line saying when a future session needs the page")
	errMemoryBodyFrontmatter = errors.New("body starts with a frontmatter block; put the when line in its own field and pass only the page's Markdown as body")
)

// memoryPageFromFields assembles a page from memory_write's arguments: a
// frontmatter block holding when, a blank line, then body. It refuses a missing when and a body that
// opens with its own complete frontmatter block.
func memoryPageFromFields(args map[string]any) (string, error) {
	when := strings.TrimSpace(stringArg(args, "when"))
	if when == "" {
		return "", errMemoryWriteNeedsWhen
	}
	body := stringArg(args, "body")
	if _, _, hasBlock := frontmatter.Split(strings.TrimLeft(body, " \t\r\n")); hasBlock {
		return "", errMemoryBodyFrontmatter
	}
	return "---\n" + memoryYAMLField("when", when) + "---\n\n" + body, nil
}
