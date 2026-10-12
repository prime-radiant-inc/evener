package agent

import (
	"errors"
	"os"
	"strings"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

// Spike: EVENER_MEMORY_WRITE_FIELDS=on gives memory_write one parameter per
// frontmatter field instead of a whole-file content string: when and body,
// and the tool writes the frontmatter itself.
const memoryWriteFieldsEnv = "EVENER_MEMORY_WRITE_FIELDS"

func memoryWriteFieldsEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv(memoryWriteFieldsEnv)), "on")
}

// memoryWriteDefinition is memory_write's definition under the fields switch.
func memoryWriteDefinition() llm.ToolDefinition {
	if memoryWriteFieldsEnabled() {
		return tool.MemoryDefinition(tool.DefMemoryWriteFields(), "memory_write")
	}
	return tool.MemoryDefinition(tool.DefWriteFile(), "memory_write")
}

var errMemoryBodyFrontmatter = errors.New("body starts with a frontmatter block; put the when line in its own field and pass only the page's Markdown as body")

// memoryPageFromFields assembles a page from memory_write's field arguments:
// a frontmatter block holding when, a blank line, then body, whose first
// heading is the page's title. It refuses a missing when and a body that
// opens with its own frontmatter block.
func memoryPageFromFields(args map[string]any) (string, error) {
	when := strings.TrimSpace(stringArg(args, "when"))
	if when == "" {
		return "", errors.New("memory_write needs a when: one line saying when a future session needs the page")
	}
	body := stringArg(args, "body")
	if strings.HasPrefix(strings.TrimLeft(body, " \t\r\n"), "---") {
		return "", errMemoryBodyFrontmatter
	}
	return "---\n" + memoryYAMLField("when", when) + "---\n\n" + body, nil
}
