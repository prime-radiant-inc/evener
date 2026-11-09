package hooks

import (
	"regexp"
	"slices"
	"strings"
)

// matchTarget reports whether matcher matches target using Claude-compatible
// matcher semantics (claude-compatible-subset tier).
//
// Rules, in priority order:
//  1. Trimmed matcher "" or "*" matches everything.
//  2. Matcher containing only [A-Za-z0-9_|,-] and space is treated as exact
//     string matching or an exact-alternative list separated by "|" or ",".
//     "Bash" matches only "Bash", not "BashOutput". "Edit|Write" and
//     "Edit, Write" both match "Edit" or "Write". Alternatives are trimmed, so
//     " Edit , Write " yields "Edit" and "Write".
//  3. Anything else is treated as a regular expression (Go RE2). On compile
//     error, returns (false, err).
//
// Evener-native divergence: surrounding whitespace is trimmed from the matcher
// before classification (a convenience), so " Bash " is treated as the exact
// matcher "Bash". Claude treats the matcher as a literal regex and would not trim.
// This is a minor, intentional evener-native nicety.
//
// Evener-native divergence: the widened exact set (hyphen, comma, space) applies
// to every event. Claude's exact set is narrower for FileChanged and StopFailure
// (no comma, space, or hyphen); there is no per-event discriminator here, so a
// hyphenated FileChanged matcher is exact rather than a regex in evener.
//
// Caveat: Claude documents JavaScript regular expressions. Go RE2 is a strict
// subset: lookbehind assertions and backreferences are not supported. If exact
// JS regex parity becomes required, an explicit JS regex engine must be added.
// All known real-world Claude hook matchers use constructs supported by RE2.
func matchTarget(matcher, target string) (bool, error) {
	m := strings.TrimSpace(matcher)

	// Rule 1: empty or wildcard matches everything.
	if m == "" || m == "*" {
		return true, nil
	}

	// Rule 2: exact / list mode when the matcher uses only the exact set.
	if isExactMatcher(m) {
		return slices.ContainsFunc(splitExactMatcher(m), func(alt string) bool {
			return strings.TrimSpace(alt) == target
		}), nil
	}

	// Rule 3: regex mode (Go RE2).
	re, err := regexp.Compile(m)
	if err != nil {
		return false, err
	}
	return re.MatchString(target), nil
}

// isExactMatcher returns true when s contains only ASCII letters, digits,
// underscores, hyphens, pipes, commas, and spaces — the set that triggers
// exact/list matching.
func isExactMatcher(s string) bool {
	for _, c := range s {
		isUpper := 'A' <= c && c <= 'Z'
		isLower := 'a' <= c && c <= 'z'
		isDigit := '0' <= c && c <= '9'
		if !isUpper && !isLower && !isDigit && c != '_' && c != '|' && c != ',' && c != '-' && c != ' ' {
			return false
		}
	}
	return true
}

// splitExactMatcher splits an exact matcher into its alternatives on "|" and ",".
// Surrounding whitespace is not trimmed here; callers trim each alternative so a
// matcher like " Edit , Write " matches "Edit" and "Write".
func splitExactMatcher(m string) []string {
	return strings.FieldsFunc(m, func(r rune) bool { return r == '|' || r == ',' })
}
