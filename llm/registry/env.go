package registry

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/net/http/httpguts"

	"primeradiant.com/evener/internal/valueexpr"
)

var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// checkEnvRefs validates the $-expression syntax of a config value at load
// time — references, defaults, and command expressions — naming the field in
// the error. It never echoes the value, which may hold a secret.
func checkEnvRefs(value, what string) error {
	if !strings.Contains(value, "$") {
		return nil
	}
	if _, err := valueexpr.Scan(value); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	return nil
}

// expandEnv substitutes a value's $ expressions — references, defaults, and
// command expressions, whose minted results come from the shared evaluator
// cache — through lookup. Missing entries are bare variable names; a failed
// command expression is an already-worded phrase (command failures always
// contain a space; a validated name never can), so a consumer can tell the
// two apart without a second type.
//
// A syntax error returns an empty value with nothing missing: every field
// that reaches here was validated at load time, so this is unreachable in
// practice and half an expansion is worse than none.
func expandEnv(value string, lookup func(string) (string, bool)) (string, []string) {
	expanded, unresolved, err := valueexpr.Expand(value, lookup)
	if err != nil {
		return "", nil
	}
	if len(unresolved) == 0 {
		return expanded, nil
	}
	missing := make([]string, 0, len(unresolved))
	for _, u := range unresolved {
		if u.Command != "" {
			missing = append(missing, "command expression failed: "+u.Err.Error())
		} else {
			missing = append(missing, u.Name)
		}
	}
	return expanded, missing
}

// missingReason words one "no credential" or "unresolved variable" report from
// expandEnv's missing entries: a bare name was unset, a phrase (a failed
// command expression) is already worded.
func missingReason(missing []string) string {
	parts := make([]string, 0, len(missing))
	for _, m := range missing {
		if strings.ContainsAny(m, " \t") {
			parts = append(parts, m)
		} else {
			parts = append(parts, m+" unset")
		}
	}
	return strings.Join(parts, ", ")
}

// ScanConfigValue reports what a providers.toml value is made of: the
// variables it references, and its literal text with those references (and any
// command expressions) removed. A caller that must refuse a literal secret
// standing beside a reference reads both halves. Neither the result nor the
// error echoes the value, which may hold one.
func ScanConfigValue(value string) (refs []string, literal string, err error) {
	scan, err := valueexpr.Scan(value)
	if err != nil {
		return nil, "", err
	}
	for _, ref := range scan.Refs {
		refs = append(refs, ref.Name)
	}
	return refs, scan.Literal, nil
}

// CheckCredentialHeaderValue holds the secrets boundary both authoring
// surfaces apply to a credential header before it is written (spec §11.2):
// every whitespace-separated token is a run of $VARIABLE references, at least
// one of them, ahead of which at most ONE literal token may stand — the auth
// scheme, by convention, so any scheme name works, custom ones included. That
// refuses a value with no reference at all and a key smuggled beside one,
// whether it is glued to the reference ("Bearer sk-live-abc$X"), standing
// behind it, or made of letters alone so that it reads as a second scheme
// word ("Bearer supersecret $KEY") — all of which a bare "contains a $" check
// accepts.
//
// The same boundary applies inside a reference's default text and to command
// expressions: only an auth scheme word may stand as a default (anything else
// is a literal secret in the file), and command expressions are authored in
// providers.toml, not through an authoring surface, so a value carrying one is
// refused here rather than half-authored.
//
// The rule is deliberately stricter than providers.toml's own grammar, which
// takes any syntactically valid value: a key typed into a form or an argv is
// a key that leaked, so the file may hold shapes neither surface will author.
// No refusal echoes the value, which may hold the secret it refused.
func CheckCredentialHeaderValue(value string) error {
	// The whole value goes through the scanner first: a command expression
	// carries spaces, so the token walk below would otherwise shred it into
	// nonsense tokens, and its refusal belongs to the whole value anyway.
	whole, err := valueexpr.Scan(value)
	if err != nil {
		return err
	}
	if len(whole.Commands) > 0 {
		return errors.New("command expressions are authored in providers.toml, not through this surface; the value itself must be a $VARIABLE reference, never a literal secret")
	}
	for _, ref := range whole.Refs {
		if ref.HasDefault && ref.Default != "" && !isAuthSchemeWord(ref.Default) {
			return errors.New("only an auth scheme word may stand as a reference's default; the value itself must be a $VARIABLE reference, never a literal secret")
		}
	}
	referenced, scheme := false, false
	for token := range strings.FieldsSeq(value) {
		scan, err := valueexpr.Scan(token)
		if err != nil {
			return err
		}
		switch {
		case len(scan.Refs) == 0 && isAuthSchemeWord(token) && !scheme && !referenced:
			// A scheme name carries no secret; a second literal word, or one
			// standing behind the reference, is not a scheme name.
			scheme = true
		case len(scan.Refs) > 0 && scan.Literal == "":
			referenced = true
		default:
			return errors.New("only an auth scheme word may be literal, ahead of the reference; the value itself must be a $VARIABLE reference, never a literal secret")
		}
	}
	if !referenced {
		return errors.New("the value must reference a $VARIABLE, never a literal secret")
	}
	return nil
}

// CheckCredentialHeaderName holds the same boundary for the NAME half of a
// credential header both authoring surfaces parse: it must be an HTTP header
// field name (RFC 7230's token). A name outside that grammar is one no server
// would read, and a CR or LF inside it would forge a second header. The
// refusal does not echo the name: a form field can hold anything the user
// pasted into it.
func CheckCredentialHeaderName(name string) error {
	if !httpguts.ValidHeaderFieldName(name) {
		return errors.New("credential header name must be an HTTP header token (letters, digits, and !#$%&'*+-.^_`|~)")
	}
	return nil
}

// CheckAPIKeyEnvName holds the same boundary for api_key_env, which names an
// environment variable rather than holding a key: the name must be one a
// "${NAME}" reference could spell (spec §10's grammar). The loader takes any
// string the TOML grammar spells, so a key pasted where its variable's name
// belonged loads fine — a category error, but one a real file can hold, and
// neither authoring surface may write it nor any client receive it. The
// refusal does not echo the name: it may be that key.
func CheckAPIKeyEnvName(name string) error {
	if !envNameRe.MatchString(name) {
		return errors.New("api_key_env names an environment variable: a letter or underscore, then only letters, digits, or underscores")
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
