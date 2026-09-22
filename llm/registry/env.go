package registry

import (
	"errors"
	"fmt"
	"strings"

	"golang.org/x/net/http/httpguts"

	"primeradiant.com/evener/internal/valueexpr"
)

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
// cache — through lookup. It returns the unresolved pieces as valueexpr
// reports them: a missing variable's name, or a failed command with its
// error.
//
// A syntax error returns an empty value with nothing missing: every field
// that reaches here was validated at load time, so this is unreachable in
// practice and half an expansion is worse than none.
func expandEnv(value string, lookup func(string) (string, bool)) (string, []valueexpr.Unresolved) {
	expanded, unresolved, err := valueexpr.Expand(value, lookup)
	if err != nil {
		return "", nil
	}
	return expanded, unresolved
}

// commandFailurePhrase words one failed command expression for a report; the
// evaluator's error text already carries the exit status or timeout reason.
func commandFailurePhrase(u valueexpr.Unresolved) string {
	return "command expression failed: " + u.Err.Error()
}

// missingReason words one "no credential" or "unresolved variable" report
// from expandEnv's unresolved pieces: a variable was unset, or a command
// expression failed.
func missingReason(unresolved []valueexpr.Unresolved) string {
	parts := make([]string, 0, len(unresolved))
	for _, u := range unresolved {
		if u.Command != "" {
			parts = append(parts, commandFailurePhrase(u))
		} else {
			parts = append(parts, u.Name+" unset")
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
// surfaces apply to a credential header before it is written (spec §11.2).
// The value's credential material is $VARIABLE references and $(command)
// expressions — a command is authored config, not a secret, so both surfaces
// (the hub's forms and `evener providers add`) accept it. Everything else is
// held to the boundary: at most ONE literal word, an HTTP auth scheme by
// convention (any scheme name works, custom ones included), and it must stand
// AHEAD of the credential material. A reference's default is literal text
// standing in the file, so only an auth scheme word may fill one — anything
// else is a key at rest.
//
// The placement rules read order, so the boundary walks the scanner's ordered
// pieces: a literal run behind credential material (a key smuggled behind a
// reference), a second literal word (an alphabetic key beside one, which a
// bare "contains a $" check accepts), and a non-scheme literal (a key glued
// to a reference, "Bearer sk-live-abc$X") are all refused.
//
// The rule is deliberately stricter than providers.toml's own grammar, which
// takes any syntactically valid value: a key typed into a form or an argv is
// a key that leaked, so the file may hold shapes neither surface will author.
// No refusal echoes the value, which may hold the secret it refused.
func CheckCredentialHeaderValue(value string) error {
	pieces, err := valueexpr.Pieces(value)
	if err != nil {
		return err
	}
	seenMaterial, schemeWord := false, false
	for _, p := range pieces {
		switch p.Kind {
		case valueexpr.PieceLit:
			for token := range strings.FieldsSeq(p.Lit) {
				if seenMaterial || schemeWord || !isAuthSchemeWord(token) {
					return errors.New("only an auth scheme word may be literal, ahead of the reference; the value itself must be a $VARIABLE reference, never a literal secret")
				}
				schemeWord = true
			}
		case valueexpr.PieceRef:
			if p.Ref.HasDefault && p.Ref.Default != "" && !isAuthSchemeWord(p.Ref.Default) {
				return errors.New("only an auth scheme word may stand as a reference's default; the value itself must be a $VARIABLE reference, never a literal secret")
			}
			seenMaterial = true
		case valueexpr.PieceCommand:
			seenMaterial = true
		}
	}
	if !seenMaterial {
		return errors.New("the value must reference a $VARIABLE or run a $(command), never a literal secret")
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
	if !valueexpr.ValidEnvName(name) {
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
