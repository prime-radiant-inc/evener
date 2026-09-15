// Package shellquote renders strings as single words for a POSIX shell.
//
// # Shared rule
//
// A word is left bare only when EVERY byte is on a conservative allow-list of
// bytes that POSIX sh never treats as a metacharacter or a word separator
// (alphanumerics plus _ - . / : , @ % + =). Anything else — whitespace, quotes,
// backslashes, the substitution and redirection operators, glob and comment
// syntax, and any byte outside the list such as non-ASCII — is wrapped in
// single quotes. An embedded single quote is emitted with the POSIX splice
// '\” (close the quote, backslash-escape one quote, reopen), which is the only
// byte that needs care inside single quotes. An empty string becomes ”.
//
// This is an allow-list rather than a deny-list on purpose: a byte nobody
// thought to deny is quoted rather than trusted.
//
// # Two entry points, one divergence
//
// Everything the package does is shared except whether a tilde may expand:
//
//   - Literal quotes a tilde like any other unsafe byte, so the result is a
//     literal path or name. Callers assembling an argument vector (a path a
//     program will open, a git argument, a test's file name) want this.
//   - RemoteWord leaves a tilde bare, so a leading "~" expands in the shell
//     that evaluates the word — the documented "~/bin/evener" spelling a
//     remote login shell has always allowed. Callers rendering one word of a
//     remote command line want this.
//
// The two are otherwise identical, so the one case where they disagree is
// explicit at the call site instead of accidental.
package shellquote

import "strings"

// isSafeWord reports whether every byte of s is on the allow-list, and s is
// non-empty. allowTilde admits "~", which is not a metacharacter and expands
// only at the start of a word; without it a tilde is an ordinary unsafe byte.
func isSafeWord(s string, allowTilde bool) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '_' || c == '-' || c == '.' || c == '/' || c == ':' ||
			c == ',' || c == '@' || c == '%' || c == '+' || c == '=':
		case c == '~' && allowTilde:
		default:
			return false
		}
	}
	return true
}

// Literal renders s as a single shell word that is always a literal string: a
// tilde is quoted away like any other unsafe byte, so it never expands. It is
// the entry point for argv discipline — quoting a path or argument so the shell
// passes it through unchanged.
func Literal(s string) string { return quote(s, false) }

// RemoteWord renders s as a single shell word for a remote login shell,
// identical to Literal except that a tilde is left bare so it expands. A word
// that needs quoting for some other reason (a space, a metacharacter) is quoted
// whole, which also quotes the tilde away; only a word that is otherwise safe
// keeps its expandable tilde.
func RemoteWord(s string) string { return quote(s, true) }

func quote(s string, allowTilde bool) string {
	if s == "" {
		return "''"
	}
	if isSafeWord(s, allowTilde) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Args space-joins args, rendering each with Literal, as the single command
// line a shell-based ExecCommand performs word-splitting on. It is the
// argv-discipline helper for callers that assemble a command string rather than
// an argv.
func Args(args ...string) string {
	var b strings.Builder
	for i, a := range args {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(Literal(a))
	}
	return b.String()
}
