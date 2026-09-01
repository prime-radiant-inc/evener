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
				// name is whatever the author put between the braces: exactly
				// the content a misplaced secret would occupy, e.g.
				// "${sk-live-...}" pasted where "${VAR}" belonged. Unlike
				// every other error in this file, it must not be
				// interpolated into the message.
				return errors.New("invalid environment variable name in ${...} reference: must start with a letter or underscore, then only letters, digits, or underscores")
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

// ScanConfigValue reports what a providers.toml value is made of under spec
// §10's grammar: the variables it references, and its literal text with those
// references removed. A caller that must refuse a literal secret standing
// beside a reference reads both halves. Neither the result nor the error
// echoes the value, which may hold one.
func ScanConfigValue(value string) (refs []string, literal string, err error) {
	var lit strings.Builder
	if err := scanEnvRefs(value, func(s string) { lit.WriteString(s) }, func(name string) { refs = append(refs, name) }); err != nil {
		return nil, "", err
	}
	return refs, lit.String(), nil
}

// CheckCredentialHeaderValue holds the secrets boundary both authoring
// surfaces apply to a credential header before it is written (spec §11.2):
// every whitespace-separated token is either a run of $VARIABLE references or
// a bare auth scheme word, and at least one is a reference. That refuses a
// value with no reference at all and a key smuggled beside one
// ("Bearer sk-live-abc$X"), which a bare "contains a $" check accepts.
//
// The rule is deliberately stricter than providers.toml's own grammar, which
// takes any syntactically valid value: a key typed into a form or an argv is
// a key that leaked, so the file may hold shapes neither surface will author.
// No refusal echoes the value, which may hold the secret it refused.
func CheckCredentialHeaderValue(value string) error {
	referenced := false
	for token := range strings.FieldsSeq(value) {
		refs, literal, err := ScanConfigValue(token)
		switch {
		case err != nil:
			return err
		case len(refs) == 0 && isAuthSchemeWord(token):
			// A scheme name carries no secret.
		case len(refs) > 0 && literal == "":
			referenced = true
		default:
			return errors.New("only an auth scheme word may be literal; the value itself must be a $VARIABLE reference, never a literal secret")
		}
	}
	if !referenced {
		return errors.New("the value must reference a $VARIABLE, never a literal secret")
	}
	return nil
}

// isAuthSchemeWord reports whether a literal token is an HTTP auth scheme
// name (Bearer, Basic, Token, ...). Letters only: a token carrying digits,
// dashes, or underscores has the shape of a key, and a key is never literal
// in a credential header.
func isAuthSchemeWord(token string) bool {
	if token == "" {
		return false
	}
	for i := range len(token) {
		c := token[i]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
			return false
		}
	}
	return true
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
