package agent

import (
	"errors"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

// Spike: EVENER_MEMORY_WRITE_FIELDS=on gives memory_write one parameter per
// frontmatter field instead of a whole-file content string, and the tool
// assembles the frontmatter itself.
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

var errMemoryBodyFrontmatter = errors.New("body starts with a frontmatter block; put the description, when, tags and evidence in their own fields and pass only the Markdown after the frontmatter as body")

// memoryPageFromFields assembles a page from memory_write's field arguments:
// a frontmatter block of description, when and the optional tags and
// evidence, a blank line, then body. It refuses a missing description or when
// and a body that opens with its own frontmatter block.
func memoryPageFromFields(args map[string]any) (string, error) {
	type fields struct {
		Description string   `yaml:"description"`
		When        string   `yaml:"when"`
		Tags        []string `yaml:"tags,omitempty"`
		Evidence    string   `yaml:"evidence,omitempty"`
	}
	f := fields{
		Description: strings.TrimSpace(stringArg(args, "description")),
		When:        strings.TrimSpace(stringArg(args, "when")),
		Evidence:    strings.TrimSpace(stringArg(args, "evidence")),
	}
	if f.Description == "" {
		return "", errors.New("memory_write needs a description: one line saying what the page tells you")
	}
	if f.When == "" {
		return "", errors.New("memory_write needs a when: one line saying when a future session needs the page")
	}
	if tags, ok := args["tags"].([]any); ok {
		for _, tag := range tags {
			if s, ok := tag.(string); ok && strings.TrimSpace(s) != "" {
				f.Tags = append(f.Tags, strings.TrimSpace(s))
			}
		}
	}
	body := stringArg(args, "body")
	if strings.HasPrefix(strings.TrimLeft(body, " \t\r\n"), "---") {
		return "", errMemoryBodyFrontmatter
	}
	encoded, err := yaml.Marshal(f)
	if err != nil {
		return "", err
	}
	return "---\n" + string(encoded) + "---\n\n" + body, nil
}
