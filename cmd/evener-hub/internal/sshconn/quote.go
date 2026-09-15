package sshconn

import "strings"

// shellQuote renders s as a single POSIX sh word that is safe to interpolate
// into a remote shell command. Values reaching these builders are either
// registry-configured (host.EvenerPath, supervisor labels) or recovered from the
// host (pids, log paths, a process command line); without quoting, a space
// splits the command and a metacharacter injects extra commands into the remote
// shell. Characters that are never metacharacters and never need word splitting
// are left bare; everything else is wrapped in single quotes, with embedded
// single quotes closed and escaped.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if isShellSafeWord(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// isShellSafeWord reports whether s can be interpolated verbatim: only
// alphanumerics and a conservative set of punctuation that POSIX sh never
// treats as a metacharacter or a word separator.
func isShellSafeWord(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '_' || c == '-' || c == '.' || c == '/' || c == ':' ||
			c == ',' || c == '@' || c == '%' || c == '+' || c == '=':
		default:
			return false
		}
	}
	return true
}
