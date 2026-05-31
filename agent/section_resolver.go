package agent

import (
	"bytes"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"primeradiant.com/serf/frontmatter"
)

// promptSource describes one component of the composed system prompt.
type promptSource struct {
	Label string
	Size  int
}

// sectionSource provides read access to a directory of section files.
type sectionSource interface {
	ReadFile(name string) ([]byte, bool)
}

// diskSource reads section files from a filesystem directory.
type diskSource struct {
	dir string
}

func (d diskSource) ReadFile(name string) ([]byte, bool) {
	if d.dir == "" {
		return nil, false
	}
	data, err := os.ReadFile(filepath.Join(d.dir, name))
	if err != nil {
		return nil, false
	}
	return data, true
}

// embedSource reads section files from an embedded filesystem.
type embedSource struct {
	fs     embed.FS
	prefix string // e.g. "prompts/sections/"
}

func (e embedSource) ReadFile(name string) ([]byte, bool) {
	data, err := e.fs.ReadFile(e.prefix + name)
	if err != nil {
		return nil, false
	}
	return data, true
}

// sectionResolver resolves prompt sections using provider/agent layering.
// It checks sources in order (first match wins) and supports .md.tmpl templates.
type sectionResolver struct {
	provider string
	agent    string
	sources  []sectionSource
	tracked  []promptSource
	agentFS  embed.FS // for role section: reads agents/{agent}.md
}

// Section resolves a named section with provider and agent layering.
//
// Provider layer: prepend, body (provider-specific or base fallback), append.
// Agent layer: prepend, body, append.
// If an agent body exists, it replaces the entire provider result.
// Otherwise, agent prepend/append are additive on top of the provider result.
// Non-empty parts are joined with "\n\n".
func (r *sectionResolver) Section(name string, data promptData) string {
	if name == "role" {
		return r.resolveRole(data)
	}

	// Provider layer.
	providerPrepend := r.readAndRender(fmt.Sprintf("%s.provider-%s_prepend", name, r.provider), data)
	providerBody := r.readAndRender(fmt.Sprintf("%s.provider-%s", name, r.provider), data)
	if providerBody == "" {
		providerBody = r.readAndRender(name, data) // fallback to base
	}
	providerAppend := r.readAndRender(fmt.Sprintf("%s.provider-%s_append", name, r.provider), data)

	// Agent layer.
	agentPrepend := r.readAndRender(fmt.Sprintf("%s.agent-%s_prepend", name, r.agent), data)
	agentBody := r.readAndRender(fmt.Sprintf("%s.agent-%s", name, r.agent), data)
	agentAppend := r.readAndRender(fmt.Sprintf("%s.agent-%s_append", name, r.agent), data)

	var parts []string
	if agentBody != "" {
		// Agent body replaces the provider result entirely.
		for _, s := range []string{agentPrepend, agentBody, agentAppend} {
			if s != "" {
				parts = append(parts, s)
			}
		}
	} else {
		// Agent prepend/append layer on top of provider result.
		for _, s := range []string{providerPrepend, providerBody, providerAppend, agentPrepend, agentAppend} {
			if s != "" {
				parts = append(parts, s)
			}
		}
	}

	return strings.Join(parts, "\n\n")
}

// resolveRole handles the "role" section specially: it checks disk sources for
// a role.agent-{agent}.md override, then falls back to the embedded agent
// definition (with YAML frontmatter stripped).
func (r *sectionResolver) resolveRole(data promptData) string {
	if body := strings.TrimSpace(data.RolePromptOverride); body != "" {
		r.tracked = append(r.tracked, promptSource{
			Label: "config:role_prompt_override",
			Size:  len(body),
		})
		return body
	}
	if r.agent == "" {
		return ""
	}
	// Check disk sources first for a role.agent-{agent}.md override.
	stem := "role.agent-" + r.agent
	for _, src := range r.sources {
		if content, ok := src.ReadFile(stem + ".md"); ok {
			s := strings.TrimRight(string(content), "\n")
			r.tracked = append(r.tracked, promptSource{
				Label: r.sourceLabel(src, stem+".md"),
				Size:  len(s),
			})
			return s
		}
	}
	// Fall back to embedded agents dir, strip frontmatter.
	path := "agents/" + r.agent + ".md"
	raw, err := r.agentFS.ReadFile(path)
	if err != nil {
		return ""
	}
	doc, err := frontmatter.Parse(string(raw))
	if err != nil {
		return ""
	}
	body := strings.TrimSpace(doc.Body)
	r.tracked = append(r.tracked, promptSource{
		Label: "agent:" + r.agent,
		Size:  len(body),
	})
	return body
}

// readAndRender tries to read a section file (template first, then plain),
// renders it if needed, and tracks the source.
func (r *sectionResolver) readAndRender(stem string, data promptData) string {
	// Try .md.tmpl first.
	if raw, label := r.readFirst(stem + ".md.tmpl"); raw != nil {
		result, err := r.renderTemplate(label, string(raw), data)
		if err != nil {
			r.tracked = append(r.tracked, promptSource{Label: "ERROR:" + label})
			return ""
		}
		result = strings.TrimRight(result, "\n")
		r.tracked = append(r.tracked, promptSource{Label: label, Size: len(result)})
		return result
	}

	// Try plain .md.
	if raw, label := r.readFirst(stem + ".md"); raw != nil {
		result := strings.TrimRight(string(raw), "\n")
		r.tracked = append(r.tracked, promptSource{Label: label, Size: len(result)})
		return result
	}

	return ""
}

// readFirst checks each source in order and returns the first match.
func (r *sectionResolver) readFirst(name string) ([]byte, string) {
	for _, src := range r.sources {
		if data, ok := src.ReadFile(name); ok {
			return data, r.sourceLabel(src, name)
		}
	}
	return nil, ""
}

// sourceLabel returns a human-readable label for a source/file combination.
func (r *sectionResolver) sourceLabel(src sectionSource, name string) string {
	switch s := src.(type) {
	case diskSource:
		return "disk:" + filepath.Join(s.dir, name)
	case embedSource:
		return "embedded:" + s.prefix + name
	default:
		return "unknown:" + name
	}
}

// renderTemplate parses and executes a text/template with the given data.
func (r *sectionResolver) renderTemplate(name, content string, data promptData) (string, error) {
	tmpl, err := template.New(name).Parse(content)
	if err != nil {
		return "", fmt.Errorf("parsing template %s: %w", name, err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("executing template %s: %w", name, err)
	}
	return buf.String(), nil
}

// Render reads a top-level template from disk at {tmplDir}/{name}.md.tmpl,
// executes it with a "section" FuncMap that resolves sections through this
// resolver, and returns the rendered text with tracked sources.
func (r *sectionResolver) Render(tmplDir string, name string, data promptData) (string, []promptSource, error) {
	r.tracked = nil
	content, err := os.ReadFile(filepath.Join(tmplDir, name+".md.tmpl"))
	if err != nil {
		return "", nil, fmt.Errorf("reading template %s: %w", name, err)
	}
	return r.renderFromContent(name, content, data)
}

// RenderEmbedded reads a top-level template from an embedded FS at
// {prefix}{name}.md.tmpl and renders it the same way as Render.
func (r *sectionResolver) RenderEmbedded(fs embed.FS, prefix, name string, data promptData) (string, []promptSource, error) {
	r.tracked = nil
	content, err := fs.ReadFile(prefix + name + ".md.tmpl")
	if err != nil {
		return "", nil, fmt.Errorf("reading embedded template %s: %w", name, err)
	}
	return r.renderFromContent(name, content, data)
}

// renderFromContent is the shared implementation for Render and RenderEmbedded.
func (r *sectionResolver) renderFromContent(name string, content []byte, data promptData) (string, []promptSource, error) {
	funcMap := template.FuncMap{
		"section": func(sectionName string) string {
			return r.Section(sectionName, data)
		},
	}
	tmpl, err := template.New(name).Funcs(funcMap).Parse(string(content))
	if err != nil {
		return "", nil, fmt.Errorf("parsing template %s: %w", name, err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", nil, fmt.Errorf("executing template %s: %w", name, err)
	}
	result := collapseBlankLines(buf.String())
	return strings.TrimSpace(result), r.tracked, nil
}

// collapseBlankLines reduces runs of 3+ consecutive newlines to 2
// (one blank line between sections).
func collapseBlankLines(s string) string {
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return s
}

// Sources returns the tracked prompt sources from all resolved sections.
func (r *sectionResolver) Sources() []promptSource {
	return r.tracked
}
