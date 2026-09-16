// Package shellquote renders strings as single words for a POSIX shell.
//
// # Shared rule
//
// A word is left bare only when EVERY byte is on a conservative allow-list of
// bytes that POSIX sh never treats as a metacharacter or a word separator
// (alphanumerics plus _ - . / : , @ % + = ^, and every byte with the high bit
// set, 0x80-0xFF). Anything else — whitespace, quotes, backslashes, the substitution
// and redirection operators, glob and comment syntax, and the ASCII control
// bytes — is wrapped in single quotes. An embedded single quote is emitted with
// the POSIX splice (close the quote, backslash-escape one quote, reopen), which
// is the only byte that needs care inside single quotes. An empty string becomes
// an empty quoted word:
//
//	'\''   the splice for a literal single quote
//	''     an empty string
//
// This is an allow-list rather than a deny-list on purpose: a byte nobody
// thought to deny is quoted rather than trusted.
//
// # POSIX shells only
//
// The quoting above is POSIX quoting, and that is the whole contract: a
// rendered word is safe to give a POSIX shell, or to exec directly as one argv
// element, and nothing else. It is not cmd.exe quoting. ExecCommand runs a
// command string through cmd.exe on Windows, where a single quote is an
// ordinary character rather than a delimiter: Literal("a & calc &") returns
// 'a & calc &', and cmd.exe still runs calc, while the '%' the allow-list
// leaves bare still expands as %VAR%. No single rendering serves both shells,
// so a Windows caller must exec from an argument vector (ExecArgv) rather than
// assemble a command string from these words.
//
// # High bytes and a caret stay bare on purpose
//
// A byte in 0x80-0xFF is not a POSIX metacharacter, so quoting it buys sh no
// safety, and the deny-lists this package replaced left it bare. It stays bare
// to keep the pre-consolidation rendering byte-for-byte: quoting "café" as
// 'café' would change the bytes every existing caller sees, and the only
// command-string consumers left — ShellEscapeArgs' documented POSIX fallback
// and RemoteWord's remote login shell — gain nothing from the quotes. Nothing
// here claims these words are cmd.exe-safe (see # POSIX shells only).
//
// A caret (^, 0x5E) is left bare for the same reason. It is not a POSIX
// metacharacter, and no shell denies it, so the deny-lists this package replaced
// returned it unchanged. Leaving the caret bare keeps that pre-consolidation
// rendering byte-for-byte (say "^HEAD"), and no remaining consumer is a shell
// for which a caret is syntax: the command-string consumers are POSIX paths
// (above), and the platform whose shell does treat the caret specially is
// exactly the one RunGit refuses to build a command line for (see agent/execenv's
// RunGit doc).
//
// # Control bytes are quoted on purpose
//
// The deny-lists also happened to leave ASCII control bytes bare — they denied
// only space, tab, and newline — so DEL (0x7F) in particular reaches this
// allow-list with the same deny-list-to-allow-list drift as the caret. It stays
// quoted. Byte-compatibility does not reach it: no remaining consumer relies on
// a bare control byte, quoting one buys a POSIX shell nothing (it is not a
// metacharacter, but it is also not safer trusted), and a non-printing byte
// appearing literally in a rendered command line is a symptom worth surfacing
// rather than reproducing. This is the allow-list's deliberate tightening, and
// the tests pin it.
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
			c == ',' || c == '@' || c == '%' || c == '+' || c == '=' || c == '^':
		case c == '~' && allowTilde:
		case c >= 0x80:
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
// line a POSIX shell performs word-splitting on. It is the argv-discipline
// helper for callers that assemble a POSIX-shell command string rather than an
// argv; it is not cmd.exe quoting, so a Windows consumer must use an argument
// vector instead (see the package doc).
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
