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

// ResolveSystemPrompt composes the system prompt from layered sources.
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
func ResolveSystemPrompt(provider, model, cliPath, projectPromptsDir, globalPromptsDir string, appendPaths []string) (string, error) {
	var sections []string

	if cliPath != "" {
		// CLI override replaces the embedded base + all directory additions.
		b, err := os.ReadFile(cliPath)
		if err != nil {
			return "", fmt.Errorf("reading --system-prompt %s: %w", cliPath, err)
		}
		sections = append(sections, string(b))
	} else {
		// Compose from embedded files + directory additions.

		// Embedded provider-specific (identity + tool docs, comes first).
		candidates := embeddedProviderCandidates(provider, model)
		if prompt, ok := firstEmbedMatch(candidates); ok {
			sections = append(sections, prompt)
		}

		// Embedded base (common guidance).
		if base, err := embeddedPrompts.ReadFile("prompts/base.md"); err == nil {
			sections = append(sections, string(base))
		}

		// Global additions (general → provider-specific).
		addCandidates := additionCandidateNames(provider)
		if globalPromptsDir != "" {
			sections = append(sections, collectAdditions(globalPromptsDir, addCandidates)...)
		}

		// Project additions (general → provider-specific).
		if projectPromptsDir != "" {
			sections = append(sections, collectAdditions(projectPromptsDir, addCandidates)...)
		}
	}

	// CLI appends always apply, even with --system-prompt override.
	for _, p := range appendPaths {
		b, err := os.ReadFile(p)
		if err != nil {
			return "", fmt.Errorf("reading --system-prompt-append %s: %w", p, err)
		}
		sections = append(sections, string(b))
	}

	if len(sections) == 0 {
		return "", fmt.Errorf("no system prompt available")
	}

	return strings.Join(sections, "\n\n"), nil
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

// collectAdditions reads all matching candidate files from dir.
// Files that don't exist are silently skipped.
func collectAdditions(dir string, candidates []string) []string {
	var parts []string
	for _, name := range candidates {
		path := filepath.Join(dir, name)
		b, err := os.ReadFile(path)
		if err == nil {
			parts = append(parts, string(b))
		}
	}
	return parts
}

// firstEmbedMatch checks the embedded filesystem for the first matching candidate.
func firstEmbedMatch(candidates []string) (string, bool) {
	for _, name := range candidates {
		b, err := embeddedPrompts.ReadFile("prompts/" + name)
		if err == nil {
			return string(b), true
		}
	}
	return "", false
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
