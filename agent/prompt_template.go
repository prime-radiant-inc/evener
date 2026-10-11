package agent

import (
	"bytes"
	_ "embed"
	"strings"
	"text/template"
)

// systemPromptTemplateLabel names the embedded template in PROMPT_LOADED
// events.
const systemPromptTemplateLabel = "embedded:prompts/system.md.tmpl"

//go:embed prompts/system.md.tmpl
var systemPromptTemplateSource string

// systemPromptTemplate is parsed once, at package initialization: a template
// that does not parse fails every test in the package instead of failing a
// session at render time.
var systemPromptTemplate = template.Must(template.New("system.md.tmpl").Parse(systemPromptTemplateSource))

// executeSystemPromptTemplate renders the system prompt for data. It is a
// variable so a test can force a render failure.
var executeSystemPromptTemplate = func(data promptData) (string, error) {
	var buf bytes.Buffer
	if err := systemPromptTemplate.Execute(&buf, data); err != nil {
		return "", err
	}
	return strings.TrimSpace(collapseBlankLines(buf.String())), nil
}

// promptSource describes one input to the composed system prompt, reported in
// a PROMPT_LOADED event.
type promptSource struct {
	Label string
	Size  int
}

// collapseBlankLines reduces runs of 3+ consecutive newlines to 2
// (one blank line between sections).
func collapseBlankLines(s string) string {
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return s
}
