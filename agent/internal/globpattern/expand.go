// Package globpattern provides the small, bounded pattern expansion shared by
// Serf's file-search tools. It deliberately implements brace alternatives only;
// shell evaluation is outside this package's contract.
package globpattern

import (
	"fmt"
)

// MaxExpansions bounds the number of patterns a single tool call may create.
// The cap keeps a model-supplied pattern from multiplying filesystem work
// without putting a large shell-expansion language into the file tools.
const MaxExpansions = 256

// Expand expands nested brace alternatives in pattern. Existing glob syntax is
// preserved for the caller's glob implementation. Braces without a top-level
// comma remain literal, matching the useful subset of standard brace syntax;
// escaped braces are never treated as expansion syntax.
func Expand(pattern string) ([]string, error) {
	expanded, err := expand(pattern, 0)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(expanded))
	seen := make(map[string]struct{}, len(expanded))
	for _, candidate := range expanded {
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		result = append(result, candidate)
	}
	return result, nil
}

func expand(pattern string, depth int) ([]string, error) {
	if depth > MaxExpansions {
		return nil, fmt.Errorf("glob pattern expansion limit exceeded (maximum %d patterns)", MaxExpansions)
	}
	start, end, alternatives, err := findExpandableGroup(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid brace pattern %q: %w", pattern, err)
	}
	if !alternatives {
		return []string{pattern}, nil
	}

	parts := splitAlternatives(pattern[start+1 : end])
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		candidate := pattern[:start] + part + pattern[end+1:]
		expanded, err := expand(candidate, depth+1)
		if err != nil {
			return nil, err
		}
		if len(result)+len(expanded) > MaxExpansions {
			return nil, fmt.Errorf("glob pattern expansion limit exceeded (maximum %d patterns)", MaxExpansions)
		}
		result = append(result, expanded...)
	}
	return result, nil
}

// findExpandableGroup returns the first innermost brace group containing a
// top-level comma. Scanning the complete pattern also validates unmatched
// braces before returning a no-expansion result. Character classes [...]
// are treated as opaque, so braces within them are not expanded.
func findExpandableGroup(pattern string) (start, end int, alternatives bool, err error) {
	stack := make([]int, 0, 2)
	for i := 0; i < len(pattern); i++ {
		if pattern[i] == '\\' {
			i++
			continue
		}
		// Skip over character classes; contents are literal, including braces
		if pattern[i] == '[' {
			i++
			for i < len(pattern) {
				if pattern[i] == '\\' {
					i++
					if i >= len(pattern) {
						break
					}
					i++
					continue
				}
				if pattern[i] == ']' {
					break
				}
				i++
			}
			continue
		}
		switch pattern[i] {
		case '{':
			stack = append(stack, i)
		case '}':
			if len(stack) == 0 {
				return 0, 0, false, fmt.Errorf("unmatched closing brace at byte %d", i)
			}
			open := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if hasTopLevelComma(pattern[open+1 : i]) {
				return open, i, true, nil
			}
		}
	}
	if len(stack) != 0 {
		return 0, 0, false, fmt.Errorf("unmatched opening brace at byte %d", stack[len(stack)-1])
	}
	return 0, 0, false, nil
}

func hasTopLevelComma(content string) bool {
	depth := 0
	for i := 0; i < len(content); i++ {
		if content[i] == '\\' {
			i++
			continue
		}
		// Skip over character classes; contents are literal, including commas
		if content[i] == '[' {
			i++
			for i < len(content) {
				if content[i] == '\\' {
					i++
					if i >= len(content) {
						break
					}
					i++
					continue
				}
				if content[i] == ']' {
					break
				}
				i++
			}
			continue
		}
		switch content[i] {
		case '{':
			depth++
		case '}':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				return true
			}
		}
	}
	return false
}

func splitAlternatives(content string) []string {
	parts := []string{}
	start := 0
	for i := 0; i < len(content); i++ {
		if content[i] == '\\' {
			i++
			continue
		}
		// Skip over character classes; contents are literal, including commas
		if content[i] == '[' {
			i++
			for i < len(content) {
				if content[i] == '\\' {
					i++
					if i >= len(content) {
						break
					}
					i++
					continue
				}
				if content[i] == ']' {
					break
				}
				i++
			}
			continue
		}
		if content[i] == ',' {
			parts = append(parts, content[start:i])
			start = i + 1
		}
	}
	return append(parts, content[start:])
}
