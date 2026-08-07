package execenv

import (
	"fmt"
	"path/filepath"
	"strings"

	"primeradiant.com/serf/agent/internal/globpattern"
)

func expandSearchPattern(pattern string) ([]string, error) {
	expanded, err := globpattern.Expand(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid glob pattern: %w", err)
	}
	return expanded, nil
}

func expandGrepFilter(filter string) ([]string, error) {
	if strings.TrimSpace(filter) == "" {
		return nil, nil
	}
	return expandSearchPattern(filter)
}

func matchesAnyGrepFilter(name string, filters []string) (bool, error) {
	for _, filter := range filters {
		matched, err := filepath.Match(filter, name)
		if err != nil {
			// Preserve the existing grep behavior for malformed [] patterns:
			// brace syntax is validated by Expand, while filepath.Match errors
			// simply mean that this filter cannot match this filename.
			continue
		}
		if matched {
			return true, nil
		}
	}
	return false, nil
}
