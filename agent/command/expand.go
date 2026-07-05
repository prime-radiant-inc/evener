// Package command expands a plugin slash command's markdown template body for
// one invocation. It is self-contained: given a body, a raw argument string,
// and the session's execution environment, it substitutes $ARGUMENTS/$1..$9,
// runs !`cmd` backtick spans, and inlines @file references.
package command

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"primeradiant.com/serf/agent/execenv"
)

// maxInlineBytes bounds how much text a single !`cmd` or @file substitution
// contributes to the expanded body, so one runaway command or large file
// cannot blow up the resulting prompt. Matches the shell tool's default
// output bound (session_tools_shell.go shellToolResultDefaultMaxChars).
const maxInlineBytes = 30_000

// positionalPattern matches $1..$9. The trailing \b excludes $10, $11, ...
// (only single-digit positions 1-9 are supported) since a digit followed by
// another digit is not a word boundary.
var positionalPattern = regexp.MustCompile(`\$([1-9])\b`)

// cmdOrFilePattern matches a !`command` backtick span or an @file reference in
// a single alternation so ReplaceAllStringFunc visits both in one left-to-right
// scan of the ORIGINAL text. That single pass is what keeps the two
// substitutions from contaminating each other: a file's contents are never
// re-scanned for !`cmd` spans (so reading a file can't trigger command
// execution), and a command's stdout is never re-scanned for @file references
// (so running a command can't trigger a further file read).
var cmdOrFilePattern = regexp.MustCompile("!`[^`]*`|@\\S+")

// Expand renders a slash command's markdown body for one invocation:
//   - $ARGUMENTS is replaced with the full, unsplit argument string.
//   - $1..$9 are replaced with the shell-split positional arguments (a
//     position beyond the number of supplied arguments becomes empty).
//   - !`cmd` runs cmd in env (the session's execution environment) and is
//     replaced with its (bounded) stdout.
//   - @file inlines the (bounded) contents of the file at the env
//     WorkingDirectory()-relative path; a missing or unreadable file becomes
//     an inline "[error reading ...]" marker rather than failing the whole
//     expansion.
//
// Substitutions run in that order over progressively-expanded text — so
// $ARGUMENTS/$1.. substitution happens first and its output can itself contain
// !`cmd`/@file syntax (e.g. a template of "@$1" with $1 substituted to a
// path) — except !`cmd` and @file themselves, which are resolved together in
// a single pass (see cmdOrFilePattern) precisely so neither's OUTPUT is
// re-interpreted as the other's syntax.
//
// The only error Expand itself returns is ctx already being done at entry;
// every per-token failure (a failing command, a missing file) degrades to
// inline text instead of aborting the expansion.
func Expand(ctx context.Context, body string, args string, env execenv.ExecutionEnvironment) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	body = substituteArguments(body, args)
	body = cmdOrFilePattern.ReplaceAllStringFunc(body, func(match string) string {
		if strings.HasPrefix(match, "!`") {
			cmd := strings.TrimSuffix(strings.TrimPrefix(match, "!`"), "`")
			return runInlineCommand(ctx, cmd, env)
		}
		return readInlineFile(strings.TrimPrefix(match, "@"), env)
	})
	return body, nil
}

// substituteArguments replaces $ARGUMENTS with the full argument string and
// $1..$9 with the shell-split positional arguments.
func substituteArguments(body, args string) string {
	body = strings.ReplaceAll(body, "$ARGUMENTS", args)
	words := shellSplit(args)
	return positionalPattern.ReplaceAllStringFunc(body, func(match string) string {
		n := int(match[1] - '0') // match is exactly "$" + one digit, e.g. "$1"
		if n <= len(words) {
			return words[n-1]
		}
		return ""
	})
}

// runInlineCommand runs cmd in env's working directory with a 10s timeout and
// returns its (bounded) stdout, trimming trailing newlines the way shell
// command substitution (`cmd`/$(cmd)) does. Any error from ExecCommand — a
// nonzero exit, a timeout, a failure to even start — is intentionally
// ignored: a !`cmd` span substitutes whatever stdout was captured regardless
// of how the command finished, so ordinary nonzero-exit commands (e.g. `grep`
// finding nothing) don't turn into scary inline errors.
func runInlineCommand(ctx context.Context, cmd string, env execenv.ExecutionEnvironment) string {
	result, _ := env.ExecCommand(ctx, cmd, 10_000, env.WorkingDirectory(), nil)
	return boundText(strings.TrimRight(result.Stdout, "\n"))
}

// readInlineFile reads the file at path, resolved relative to env's working
// directory, and returns its (bounded) contents. A read failure (missing
// file, permission error, directory, ...) degrades to an inline marker
// instead of failing the whole expansion.
func readInlineFile(path string, env execenv.ExecutionEnvironment) string {
	full := filepath.Join(env.WorkingDirectory(), path)
	data, err := os.ReadFile(full)
	if err != nil {
		return fmt.Sprintf("[error reading %s: %v]", path, err)
	}
	return boundText(string(data))
}

// boundText truncates s to maxInlineBytes, appending a marker noting the
// original size when truncation occurs.
func boundText(s string) string {
	if len(s) <= maxInlineBytes {
		return s
	}
	return s[:maxInlineBytes] + fmt.Sprintf("\n...[truncated, %d bytes total]", len(s))
}

// shellSplit splits s into words using simplified POSIX shell quoting:
// single quotes preserve their contents literally; double quotes preserve
// their contents but let a backslash escape a following ", \, or $; outside
// quotes a backslash escapes the next character; unquoted whitespace
// separates words. An unterminated quote or trailing backslash is treated as
// literal rather than erroring, so a malformed argument string degrades
// instead of blocking expansion.
func shellSplit(s string) []string {
	var words []string
	var cur strings.Builder
	haveCur := false
	inSingle, inDouble := false, false
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch {
		case inSingle:
			if c == '\'' {
				inSingle = false
			} else {
				cur.WriteRune(c)
			}
		case inDouble:
			if c == '"' {
				inDouble = false
			} else if c == '\\' && i+1 < len(runes) && strings.ContainsRune(`"\$`, runes[i+1]) {
				i++
				cur.WriteRune(runes[i])
			} else {
				cur.WriteRune(c)
			}
		case c == '\'':
			inSingle = true
			haveCur = true
		case c == '"':
			inDouble = true
			haveCur = true
		case c == '\\' && i+1 < len(runes):
			i++
			cur.WriteRune(runes[i])
			haveCur = true
		case c == ' ' || c == '\t' || c == '\n':
			if haveCur {
				words = append(words, cur.String())
				cur.Reset()
				haveCur = false
			}
		default:
			cur.WriteRune(c)
			haveCur = true
		}
	}
	if haveCur {
		words = append(words, cur.String())
	}
	return words
}
