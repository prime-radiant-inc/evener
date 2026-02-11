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

// ResolveSystemPrompt finds the best system prompt file using layered resolution.
// Priority (first match wins):
//  1. cliPath — explicit file path from --system-prompt flag
//  2. projectPromptsDir — .serf/prompts/ at project root
//  3. globalPromptsDir — ~/.config/serf/prompts/
//  4. Embedded defaults compiled into the binary
//
// Within each directory source, candidate filenames are tried most-specific first:
//
//	system.<provider>.<model>.md → system.<provider>.md → system.md
func ResolveSystemPrompt(provider, model, cliPath, projectPromptsDir, globalPromptsDir string) (string, error) {
	// 1. CLI override — exact file, no naming convention.
	if cliPath != "" {
		b, err := os.ReadFile(cliPath)
		if err != nil {
			return "", fmt.Errorf("reading --system-prompt %s: %w", cliPath, err)
		}
		return string(b), nil
	}

	candidates := promptCandidateNames(provider, model)

	// 2. Project-level overrides.
	if projectPromptsDir != "" {
		if prompt, ok := firstMatch(projectPromptsDir, candidates); ok {
			return prompt, nil
		}
	}

	// 3. Global overrides.
	if globalPromptsDir != "" {
		if prompt, ok := firstMatch(globalPromptsDir, candidates); ok {
			return prompt, nil
		}
	}

	// 4. Embedded defaults.
	if prompt, ok := firstEmbedMatch(candidates); ok {
		return prompt, nil
	}

	// Final fallback: generic embedded default (shouldn't normally be reached
	// since we always have provider-specific embedded files, but handles
	// unknown providers gracefully).
	return genericEmbeddedFallback()
}

// promptCandidateNames returns filenames to try, most-specific first.
func promptCandidateNames(provider, model string) []string {
	var names []string
	if model != "" {
		names = append(names, fmt.Sprintf("system.%s.%s.md", provider, model))
	}
	if provider != "" {
		names = append(names, fmt.Sprintf("system.%s.md", provider))
	}
	names = append(names, "system.md")
	return names
}

// firstMatch checks a directory for the first existing candidate file.
func firstMatch(dir string, candidates []string) (string, bool) {
	for _, name := range candidates {
		path := filepath.Join(dir, name)
		b, err := os.ReadFile(path)
		if err == nil {
			return string(b), true
		}
	}
	return "", false
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

// genericEmbeddedFallback returns any available embedded prompt as a last resort.
func genericEmbeddedFallback() (string, error) {
	entries, err := embeddedPrompts.ReadDir("prompts")
	if err != nil {
		return "", fmt.Errorf("no embedded prompts available")
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			b, err := embeddedPrompts.ReadFile("prompts/" + e.Name())
			if err == nil {
				return string(b), nil
			}
		}
	}
	return "", fmt.Errorf("no embedded prompts available")
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
