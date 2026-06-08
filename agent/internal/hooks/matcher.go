package hooks

import (
	"regexp"
	"strings"
)

// matchTarget reports whether matcher matches target using Claude-compatible
// matcher semantics (claude-compatible-subset tier).
//
// Rules, in priority order:
//  1. Trimmed matcher "" or "*" matches everything.
//  2. Matcher containing only [A-Za-z0-9_|] characters is treated as exact
//     string matching or a pipe-separated exact-alternative list. "Bash" matches
//     only "Bash", not "BashOutput". "Edit|Write" matches "Edit" or "Write".
//  3. Anything else is treated as a regular expression (Go RE2). On compile
//     error, returns (false, err).
//
// Serf-native divergence: surrounding whitespace is trimmed from the matcher
// before classification (a convenience), so " Bash " is treated as the exact
// matcher "Bash". Claude treats the matcher as a literal regex and would not trim.
// This is a minor, intentional serf-native nicety.
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

	// Rule 2: exact / pipe-list mode when matcher is only [A-Za-z0-9_|].
	if isExactMatcher(m) {
		for _, segment := range strings.Split(m, "|") {
			if segment == target {
				return true, nil
			}
		}
		return false, nil
	}

	// Rule 3: regex mode (Go RE2).
	re, err := regexp.Compile(m)
	if err != nil {
		return false, err
	}
	return re.MatchString(target), nil
}

// isExactMatcher returns true when s contains only ASCII letters, digits,
// underscores, and pipes — the set that triggers exact/pipe-list matching.
func isExactMatcher(s string) bool {
	for _, c := range s {
		isUpper := 'A' <= c && c <= 'Z'
		isLower := 'a' <= c && c <= 'z'
		isDigit := '0' <= c && c <= '9'
		if !isUpper && !isLower && !isDigit && c != '_' && c != '|' {
			return false
		}
	}
	return true
}
