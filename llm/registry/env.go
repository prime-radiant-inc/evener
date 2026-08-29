package registry

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func isEnvNameByte(c byte) bool {
	return c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
}

// scanEnvRefs walks value with today's providers.toml rules (spec §10):
// "$NAME" and "${NAME}" reference a variable, "$$" is a literal "$", a "$"
// not followed by a name character is literal. It calls ref for every
// reference and lit for every literal run, and returns a syntax error for an
// unterminated "${" or an invalid name. It never echoes the value (it may
// hold a secret).
func scanEnvRefs(value string, lit func(string), ref func(string)) error {
	for i := 0; i < len(value); {
		c := value[i]
		if c != '$' {
			j := i
			for j < len(value) && value[j] != '$' {
				j++
			}
			lit(value[i:j])
			i = j
			continue
		}
		if i+1 >= len(value) {
			lit("$")
			i++
			continue
		}
		next := value[i+1]
		switch {
		case next == '$':
			lit("$")
			i += 2
		case next == '{':
			end := strings.IndexByte(value[i+2:], '}')
			if end < 0 {
				return errors.New("unterminated ${ in value")
			}
			name := value[i+2 : i+2+end]
			if !envNameRe.MatchString(name) {
				return fmt.Errorf("invalid environment variable name %q", name)
			}
			ref(name)
			i += 2 + end + 1
		case isEnvNameByte(next) && (next < '0' || next > '9'):
			j := i + 1
			for j < len(value) && isEnvNameByte(value[j]) {
				j++
			}
			ref(value[i+1 : j])
			i = j
		default:
			lit("$")
			i++
		}
	}
	return nil
}

// checkEnvRefs validates the $ENV syntax of a config value at load time;
// what names the field in the error.
func checkEnvRefs(value, what string) error {
	if !strings.Contains(value, "$") {
		return nil
	}
	if err := scanEnvRefs(value, func(string) {}, func(string) {}); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	return nil
}

// expandEnv substitutes $ENV references through lookup and returns the
// expanded value plus the names that did not resolve (each substituted by
// the empty string). Values validated by checkEnvRefs never fail here.
func expandEnv(value string, lookup func(string) (string, bool)) (string, []string) {
	if !strings.Contains(value, "$") {
		return value, nil
	}
	var b strings.Builder
	var missing []string
	_ = scanEnvRefs(value, func(s string) { b.WriteString(s) }, func(name string) {
		v, ok := lookup(name)
		if !ok || v == "" {
			missing = append(missing, name)
			return
		}
		b.WriteString(v)
	})
	return b.String(), missing
}
