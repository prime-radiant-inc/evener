package agent

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

//go:embed prompts/*.md
var embeddedPrompts embed.FS

// PromptSource describes one component of the composed system prompt.
type PromptSource struct {
	Label string // e.g. "embedded:base.md", "project:.serf/prompts/system.openai.md"
	Size  int    // byte length of this section
}

// ResolveSystemPrompt composes the system prompt from layered sources.
// It discards source metadata; use ResolveSystemPromptWithSources when you need it.
func ResolveSystemPrompt(provider, model, cliPath, projectPromptsDir, globalPromptsDir string, appendPaths []string) (string, error) {
	prompt, _, err := ResolveSystemPromptWithSources(provider, model, cliPath, projectPromptsDir, globalPromptsDir, appendPaths)
	return prompt, err
}

// ResolveSystemPromptWithSources composes the system prompt and returns the
// individual sources that contributed to it. Sources are ordered as they appear
// in the final prompt.
//
// When cliPath is set (--system-prompt flag), it replaces the embedded base entirely.
// Otherwise, the prompt is composed from:
//  1. Embedded provider file (identity + tool docs)
//  2. Embedded base.md (common guidance)
//  3. Global additions (~/.config/serf/prompts/system.md, system.<provider>.md)
//  4. Project additions (.serf/prompts/system.md, system.<provider>.md)
//
// appendPaths (--system-prompt-append) are always appended last, regardless of
// whether cliPath is set. This allows orchestrators to layer guidance on any base.
func ResolveSystemPromptWithSources(provider, model, cliPath, projectPromptsDir, globalPromptsDir string, appendPaths []string) (string, []PromptSource, error) {
	var sections []string
	var sources []PromptSource

	if cliPath != "" {
		// CLI override replaces the embedded base + all directory additions.
		b, err := os.ReadFile(cliPath)
		if err != nil {
			return "", nil, fmt.Errorf("reading --system-prompt %s: %w", cliPath, err)
		}
		sections = append(sections, string(b))
		sources = append(sources, PromptSource{Label: "cli:" + cliPath, Size: len(b)})
	} else {
		// Compose from embedded files + directory additions.

		// Embedded provider-specific (identity + tool docs, comes first).
		candidates := embeddedProviderCandidates(provider, model)
		if prompt, name, ok := firstEmbedMatchNamed(candidates); ok {
			sections = append(sections, prompt)
			sources = append(sources, PromptSource{Label: "embedded:" + name, Size: len(prompt)})
		}

		// Embedded base (common guidance).
		if base, err := embeddedPrompts.ReadFile("prompts/base.md"); err == nil {
			sections = append(sections, string(base))
			sources = append(sources, PromptSource{Label: "embedded:base.md", Size: len(base)})
		}

		// Global additions (general → provider-specific).
		addCandidates := additionCandidateNames(provider)
		if globalPromptsDir != "" {
			for _, a := range collectAdditionsNamed(globalPromptsDir, addCandidates) {
				sections = append(sections, a.content)
				sources = append(sources, PromptSource{Label: "global:" + a.name, Size: len(a.content)})
			}
		}

		// Project additions (general → provider-specific).
		if projectPromptsDir != "" {
			for _, a := range collectAdditionsNamed(projectPromptsDir, addCandidates) {
				sections = append(sections, a.content)
				sources = append(sources, PromptSource{Label: "project:" + a.name, Size: len(a.content)})
			}
		}
	}

	// CLI appends always apply, even with --system-prompt override.
	for _, p := range appendPaths {
		b, err := os.ReadFile(p)
		if err != nil {
			return "", nil, fmt.Errorf("reading --system-prompt-append %s: %w", p, err)
		}
		sections = append(sections, string(b))
		sources = append(sources, PromptSource{Label: "append:" + p, Size: len(b)})
	}

	if len(sections) == 0 {
		return "", nil, fmt.Errorf("no system prompt available")
	}

	return strings.Join(sections, "\n\n"), sources, nil
}

// embeddedProviderCandidates returns filenames for embedded provider lookup,
// most-specific first. Does not include base.md (that's loaded separately).
func embeddedProviderCandidates(provider, model string) []string {
	var names []string
	if model != "" && provider != "" {
		names = append(names, fmt.Sprintf("system.%s.%s.md", provider, model))
	}
	if provider != "" {
		names = append(names, fmt.Sprintf("system.%s.md", provider))
	}
	return names
}

// additionCandidateNames returns filenames for directory additions,
// ordered general → provider-specific so broader rules come first.
func additionCandidateNames(provider string) []string {
	names := []string{"system.md"}
	if provider != "" {
		names = append(names, fmt.Sprintf("system.%s.md", provider))
	}
	return names
}

// namedAddition pairs a filename with its content.
type namedAddition struct {
	name    string
	content string
}

// collectAdditions reads all matching candidate files from dir.
// Files that don't exist are silently skipped.
func collectAdditions(dir string, candidates []string) []string {
	var parts []string
	for _, a := range collectAdditionsNamed(dir, candidates) {
		parts = append(parts, a.content)
	}
	return parts
}

// collectAdditionsNamed is like collectAdditions but returns filenames alongside content.
func collectAdditionsNamed(dir string, candidates []string) []namedAddition {
	var parts []namedAddition
	for _, name := range candidates {
		path := filepath.Join(dir, name)
		b, err := os.ReadFile(path)
		if err == nil {
			parts = append(parts, namedAddition{name: name, content: string(b)})
		}
	}
	return parts
}

// firstEmbedMatch checks the embedded filesystem for the first matching candidate.
func firstEmbedMatch(candidates []string) (string, bool) {
	s, _, ok := firstEmbedMatchNamed(candidates)
	return s, ok
}

// firstEmbedMatchNamed is like firstEmbedMatch but also returns the matched filename.
func firstEmbedMatchNamed(candidates []string) (string, string, bool) {
	for _, name := range candidates {
		b, err := embeddedPrompts.ReadFile("prompts/" + name)
		if err == nil {
			return string(b), name, true
		}
	}
	return "", "", false
}

// GlobalPromptsDir returns the path to the global prompts directory.
// Uses XDG_CONFIG_HOME if set, otherwise ~/.config.
func GlobalPromptsDir() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "serf", "prompts")
}

// ProjectPromptsDir returns the prompts directory for a project, given the git root.
func ProjectPromptsDir(gitRoot string) string {
	if gitRoot == "" {
		return ""
	}
	return filepath.Join(gitRoot, ".serf", "prompts")
}
