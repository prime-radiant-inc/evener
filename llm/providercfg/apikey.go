package providercfg

import (
	"fmt"
	"os"
	"strings"
)

// ResolveAPIKey expands environment-variable references in a providers.toml
// api_key value: "$NAME" and "${NAME}" substitute the named variable, "$$"
// escapes a literal "$". A referenced variable that is unset or empty is an
// error — a silently-empty key would surface later as an opaque provider 401.
// Values without "$" pass through unchanged. Callers resolve at the point of
// use (adapter construction, live /models probes), not at Load, so one
// instance's missing variable never blocks unrelated instances.
func ResolveAPIKey(raw string) (string, error) {
	return resolveEnvValue(raw, "api_key")
}

// ResolveHeaderValue expands the same environment-variable references as
// ResolveAPIKey in a providers.toml [instances.X.headers] value. Errors name
// the header key and the missing variable so a misconfigured gateway header is
// diagnosable. A header value may legitimately hold a secret via $ENV — that is
// the recommended form, because unlike api_key the raw header value is written
// back verbatim by Marshal.
func ResolveHeaderValue(name, raw string) (string, error) {
	return resolveEnvValue(raw, fmt.Sprintf("header %q", name))
}

// resolveEnvValue is the shared $ENV/${ENV}/$$ expander behind ResolveAPIKey
// and ResolveHeaderValue. what names the field in error messages (e.g.
// "api_key" or `header "X-Foo"`).
func resolveEnvValue(raw, what string) (string, error) {
	if !strings.Contains(raw, "$") {
		return raw, nil
	}
	var b strings.Builder
	for i := 0; i < len(raw); {
		c := raw[i]
		if c != '$' {
			b.WriteByte(c)
			i++
			continue
		}
		// c == '$'
		if i+1 >= len(raw) {
			// Trailing lone '$' is literal.
			b.WriteByte('$')
			i++
			continue
		}
		next := raw[i+1]
		switch {
		case next == '$':
			b.WriteByte('$')
			i += 2
		case next == '{':
			end := strings.IndexByte(raw[i+2:], '}')
			if end < 0 {
				return "", fmt.Errorf("%s: unterminated ${ in %q", what, raw)
			}
			name := raw[i+2 : i+2+end]
			if !validEnvName(name) {
				return "", fmt.Errorf("%s: invalid environment variable name %q", what, name)
			}
			v := os.Getenv(name)
			if v == "" {
				return "", fmt.Errorf("%s references environment variable %s, which is unset or empty", what, name)
			}
			b.WriteString(v)
			i += 2 + end + 1
		case isEnvNameStart(next):
			j := i + 1
			for j < len(raw) && isEnvNameByte(raw[j]) {
				j++
			}
			name := raw[i+1 : j]
			v := os.Getenv(name)
			if v == "" {
				return "", fmt.Errorf("%s references environment variable %s, which is unset or empty", what, name)
			}
			b.WriteString(v)
			i = j
		default:
			// '$' followed by a non-name character is literal.
			b.WriteByte('$')
			i++
		}
	}
	return b.String(), nil
}

func isEnvNameStart(c byte) bool {
	return c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

func isEnvNameByte(c byte) bool {
	return isEnvNameStart(c) || (c >= '0' && c <= '9')
}

func validEnvName(s string) bool {
	if s == "" || !isEnvNameStart(s[0]) {
		return false
	}
	for i := 1; i < len(s); i++ {
		if !isEnvNameByte(s[i]) {
			return false
		}
	}
	return true
}
