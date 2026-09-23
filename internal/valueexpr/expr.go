// Package valueexpr is the single parser and expander for $ expressions in
// evener's config values. Both surfaces that expand such expressions today —
// providers.toml (llm/registry) and MCP server config (agent/mcpconfig) —
// call this package; there is no second parser.
//
// The grammar:
//
//	$NAME            a reference to the environment variable NAME
//	${NAME}          the same, braced, for names glued to text
//	${NAME:-default} a reference with a default used when NAME is unset or
//	                 empty — POSIX :- semantics; the default is literal text,
//	                 never re-expanded
//	$(command)       a command expression: the command runs through the
//	                 shell and its whitespace-trimmed stdout is the value;
//	                 the interior is opaque to this parser — the shell owns
//	                 its syntax at run time
//	$$               a literal $
//	$ before any other byte is a literal $
//
// A variable that is unset or empty-but-set counts as missing, so an empty
// credential never resolves as a present one. A missing reference without a
// default, and a failed command, both substitute the empty string and are
// reported in Expand's unresolved list; hosts map that to their own
// convention (a warning in the registry, a parse error in MCP config).
//
// Unterminated ${ or $(, an empty command, and an invalid variable name are
// syntax errors. The invalid-name error never echoes the value: between the
// braces may stand a pasted secret.
package valueexpr

import (
	"errors"
	"strings"
)

// isEnvNameStart and isEnvNameByte spell the grammar a reference name may
// take: a letter or underscore first, then letters, digits, or underscores.
// ValidEnvName is the one authority over the whole set.
func isEnvNameStart(c byte) bool {
	return c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

func isEnvNameByte(c byte) bool {
	return isEnvNameStart(c) || (c >= '0' && c <= '9')
}

// sink receives a value's pieces while scan walks it: every literal run, every
// reference (with its default when one stands in the file), and every command
// expression.
type sink struct {
	lit func(string)
	ref func(name, def string, hasDef bool)
	cmd func(command string)
}

// scan walks value with the grammar above and feeds the pieces to s. It never
// evaluates anything and never echoes the value in an error, which may hold a
// secret.
func scan(value string, s sink) error {
scanLoop:
	for i := 0; i < len(value); {
		c := value[i]
		if c != '$' {
			j := i
			for j < len(value) && value[j] != '$' {
				j++
			}
			s.lit(value[i:j])
			i = j
			continue
		}
		if i+1 >= len(value) {
			s.lit("$")
			i++
			continue
		}
		next := value[i+1]
		switch {
		case next == '$':
			s.lit("$")
			i += 2
		case next == '{':
			end := strings.IndexByte(value[i+2:], '}')
			if end < 0 {
				return errors.New("unterminated ${ in value")
			}
			expr := value[i+2 : i+2+end]
			i += 2 + end + 1
			name, def, hasDef := strings.Cut(expr, ":-")
			if !ValidEnvName(name) {
				// name is whatever the author put between the braces: exactly
				// the content a misplaced secret would occupy, so it must not
				// be interpolated into the message.
				return errors.New("invalid environment variable name in ${...} reference: must start with a letter or underscore, then only letters, digits, or underscores")
			}
			s.ref(name, def, hasDef)
		case next == '(':
			// The expression ends at the first ) that brings the paren depth
			// back to zero. Quotes are not parsed: a literal ) inside a quoted
			// command argument is a documented limitation — restructure the
			// command or wrap it in a helper script.
			depth := 1
			j := i + 2
			for j < len(value) {
				switch value[j] {
				case '(':
					depth++
				case ')':
					depth--
					if depth == 0 {
						command := value[i+2 : j]
						if command == "" {
							return errors.New("empty $( command in value")
						}
						s.cmd(command)
						i = j + 1
						continue scanLoop
					}
				}
				j++
			}
			// Falling out of the loop means the value ran out with the depth
			// still open.
			return errors.New("unterminated $( in value")
		case isEnvNameStart(next):
			j := i + 1
			for j < len(value) && isEnvNameByte(value[j]) {
				j++
			}
			s.ref(value[i+1:j], "", false)
			i = j
		default:
			s.lit("$")
			i++
		}
	}
	return nil
}

// Ref is one reference as it stands in the file: the variable name plus the
// default text when the author wrote one. The default is not literal material
// — it substitutes for the reference, so it never appears in Scan.Literal.
type Ref struct {
	Name       string
	Default    string
	HasDefault bool
}

// Inventory reports a value's pieces without expanding it: the references,
// the command expressions, and the literal text with both removed. The
// secrets boundary and the credential-ownership predicates build on it, so no
// expansion and no I/O happens here.
type Inventory struct {
	Refs     []Ref
	Commands []string
	Literal  string
}

// Scan walks value and reports its pieces, or a syntax error.
func Scan(value string) (Inventory, error) {
	// A value with no $ is one literal run and nothing else, and most
	// config values are exactly that.
	if strings.IndexByte(value, '$') < 0 {
		return Inventory{Literal: value}, nil
	}
	var out Inventory
	var lit strings.Builder
	err := scan(value, sink{
		lit: func(s string) { lit.WriteString(s) },
		ref: func(name, def string, hasDef bool) {
			out.Refs = append(out.Refs, Ref{Name: name, Default: def, HasDefault: hasDef})
		},
		cmd: func(command string) { out.Commands = append(out.Commands, command) },
	})
	out.Literal = lit.String()
	return out, err
}

// PieceKind names one piece of a scanned value.
type PieceKind int8

const (
	PieceLit     PieceKind = iota // a literal run
	PieceRef                      // a $NAME / ${NAME...} reference
	PieceCommand                  // a $(command) expression
)

// Piece is one piece of a value in file order: a literal run, a reference, or
// a command expression. The authoring boundary's placement rules read order,
// so they consume pieces where Scan flattens it away.
type Piece struct {
	Kind    PieceKind
	Lit     string // PieceLit
	Ref     Ref    // PieceRef
	Command string // PieceCommand
}

// Pieces splits value into its ordered pieces, or reports the syntax error.
// It is the same single scan as Scan, reported in order instead of
// categorized.
func Pieces(value string) ([]Piece, error) {
	var out []Piece
	err := scan(value, sink{
		lit: func(s string) { out = append(out, Piece{Kind: PieceLit, Lit: s}) },
		ref: func(name, def string, hasDef bool) {
			out = append(out, Piece{Kind: PieceRef, Ref: Ref{Name: name, Default: def, HasDefault: hasDef}})
		},
		cmd: func(command string) { out = append(out, Piece{Kind: PieceCommand, Command: command}) },
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ValidEnvName reports whether name is one a ${NAME} reference could spell.
// The env-ref grammar's single authority; hosts that validate a name-shaped
// field (the registry's api_key_env) call this instead of restating the rule.
func ValidEnvName(name string) bool {
	if name == "" || !isEnvNameStart(name[0]) {
		return false
	}
	for i := 1; i < len(name); i++ {
		if !isEnvNameByte(name[i]) {
			return false
		}
	}
	return true
}

// Unresolved names why a piece of the value produced nothing: a missing or
// empty environment variable (Name set) or a failed command (Command set with
// Err). It carries the failed command so tests and hosts can be precise; no
// host should echo it, and CommandError never carries the command's output.
type Unresolved struct {
	Name    string
	Command string
	Err     error
}

// Expand substitutes every reference and command expression in value through
// lookup and the command evaluator. A missing or empty-but-set variable
// without a default, and a failed command, substitute the empty string and
// are reported in the unresolved list. A syntax error returns the error and
// an empty value; callers that validated the value at load time can treat it
// as unreachable. Commands run only after the whole value has scanned
// clean: the pieces are collected first, so a malformed tail fails the
// expansion before any side effect.
func Expand(value string, lookup func(string) (string, bool)) (string, []Unresolved, error) {
	// Same fast path as Scan: a value with no $ expands to itself.
	if strings.IndexByte(value, '$') < 0 {
		return value, nil, nil
	}
	pieces, err := Pieces(value)
	if err != nil {
		return "", nil, err
	}
	var b strings.Builder
	var unresolved []Unresolved
	for _, p := range pieces {
		switch p.Kind {
		case PieceLit:
			b.WriteString(p.Lit)
		case PieceRef:
			if v, ok := lookup(p.Ref.Name); ok && v != "" {
				b.WriteString(v)
				continue
			}
			if p.Ref.HasDefault {
				b.WriteString(p.Ref.Default)
				continue
			}
			unresolved = append(unresolved, Unresolved{Name: p.Ref.Name})
		case PieceCommand:
			res, err := evaluate(p.Command)
			if err != nil {
				unresolved = append(unresolved, Unresolved{Command: p.Command, Err: err})
				continue
			}
			b.WriteString(res.Value)
		}
	}
	return b.String(), unresolved, nil
}
