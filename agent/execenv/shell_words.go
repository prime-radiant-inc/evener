package execenv

import "strings"

// The shell tool hands ExecCommand one command string, and every such line used
// to pay for a `bash -o pipefail -c` fork before the real program forked. A
// line that is provably a bare word list needs no shell: it runs as argv and
// saves that fork on the round's critical path (#6447).
//
// Everything here errs toward the shell. A guess that is wrong changes what a
// user's command means — a mis-split quote turns one argument into two, an
// unexpanded glob stays literal, an unapplied assignment sets no variable —
// while a needless shell fork only costs time.

// shellWordMetacharacters holds every byte whose unquoted presence lets the
// shell act on a command: operators and subshells (| & ; ( )), redirection and
// brace expansion (< > { }), globbing and pattern matching (* ? [ ]),
// expansion and history (! $ ` ~), and the comment character (#). Quotes and
// backslashes are handled by the splitter rather than listed here.
const shellWordMetacharacters = "|&;<>(){}*?[]!$`~#"

// shellBuiltinOrKeyword names the bash builtins and reserved words, as reported
// by `compgen -b` and `compgen -k`. bash resolves one of these before any PATH
// program of the same name, and several have no binary at all (`exit`,
// `export`, `set`) or behave differently from the binary that shares the name
// (`pwd` prints the logical path, `echo` applies its own backslash rules). A
// command whose program name is one of them stays on the shell.
var shellBuiltinOrKeyword = map[string]bool{
	".": true, ":": true, "[": true, "alias": true, "bg": true, "bind": true,
	"break": true, "builtin": true, "caller": true, "cd": true, "command": true,
	"compgen": true, "complete": true, "compopt": true, "continue": true,
	"declare": true, "dirs": true, "disown": true, "echo": true, "enable": true,
	"eval": true, "exec": true, "exit": true, "export": true, "false": true,
	"fc": true, "fg": true, "getopts": true, "hash": true, "help": true,
	"history": true, "jobs": true, "kill": true, "let": true, "local": true,
	"logout": true, "mapfile": true, "popd": true, "printf": true, "pushd": true,
	"pwd": true, "read": true, "readarray": true, "readonly": true,
	"return": true, "set": true, "shift": true, "shopt": true, "source": true,
	"suspend": true, "test": true, "times": true, "trap": true, "true": true,
	"type": true, "typeset": true, "ulimit": true, "umask": true,
	"unalias": true, "unset": true, "wait": true,

	// Reserved words. Most carry a metacharacter and are refused earlier;
	// these are the plain ones that could otherwise look like a program name.
	"if": true, "then": true, "else": true, "elif": true, "fi": true,
	"case": true, "esac": true, "for": true, "select": true, "while": true,
	"until": true, "do": true, "done": true, "in": true, "function": true,
	"time": true, "coproc": true,
}

// argvFromCommandString reports command as the program name and argument
// vector a POSIX shell would run, with ok=false whenever the shell might still
// act on it: an empty command, a leading assignment, a bash builtin or
// reserved word as the program name, or anything splitShellWords refuses.
//
// The equivalence it relies on: `bash -o pipefail -c 'foo bar'` and an argv of
// foo bar differ only where a shell intervenes before the program runs. A
// non-interactive bash has no aliases and no inherited functions unless $BASH_ENV
// names a file. commandRuntimeForCommand settles that case, and how the program
// is found, before it uses this: it keeps the shell when $BASH_ENV is set, and
// requires the child environment's PATH to resolve a bare name, the same lookup
// bash does.
func argvFromCommandString(command string) (name string, args []string, ok bool) {
	if runtimeGOOS == "windows" {
		// cmd.exe has its own quoting and expansion rules; this classifier
		// describes POSIX shells only.
		return "", nil, false
	}
	words, ok := splitShellWords(command)
	if !ok || len(words) == 0 || words[0] == "" {
		return "", nil, false
	}
	program := words[0]
	if looksLikeAssignmentWord(program) {
		return "", nil, false
	}
	if shellBuiltinOrKeyword[program] {
		// Only a bare name is shadowed this way; a path never appears in the
		// list, so a path is never refused here.
		return "", nil, false
	}
	return program, words[1:], true
}

// looksLikeAssignmentWord reports whether word is a shell assignment
// (`NAME=value`), which sets a variable instead of running a program.
func looksLikeAssignmentWord(word string) bool {
	name, _, ok := strings.Cut(word, "=")
	if !ok || name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c == '_', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// splitShellWords splits command into the words a POSIX shell would pass to the
// program, applying the quote and escape rules it would apply to a bare word
// list, and refusing anything it cannot reproduce byte for byte. It is not a
// shell parser: operators, expansions, globs, comments, line continuations and
// an unmatched quote or trailing backslash all return ok=false.
func splitShellWords(command string) (words []string, ok bool) {
	var word strings.Builder
	haveWord := false
	for i := 0; i < len(command); i++ {
		c := command[i]
		switch {
		case c == ' ' || c == '\t':
			if haveWord {
				words = append(words, word.String())
				word.Reset()
				haveWord = false
			}
		case c == '\n':
			// An unquoted newline ends the command rather than the word, so
			// `git status\ngit log` is two commands and argv cannot say that.
			return nil, false
		case c == '\\':
			// A trailing backslash, or a line continuation, has no argv form:
			// both are the shell still waiting for input.
			if i+1 >= len(command) || command[i+1] == '\n' {
				return nil, false
			}
			i++
			word.WriteByte(command[i])
			haveWord = true
		case c == '\'':
			closed := false
			for i++; i < len(command); i++ {
				if command[i] == '\'' {
					closed = true
					break
				}
				word.WriteByte(command[i])
			}
			if !closed {
				return nil, false
			}
			haveWord = true
		case c == '"':
			next, quoted := appendDoubleQuotedWord(&word, command, i+1)
			if !quoted {
				return nil, false
			}
			i = next - 1
			haveWord = true
		case strings.IndexByte(shellWordMetacharacters, c) >= 0:
			return nil, false
		default:
			word.WriteByte(c)
			haveWord = true
		}
	}
	if haveWord {
		words = append(words, word.String())
	}
	return words, true
}

// appendDoubleQuotedWord writes the contents of the double-quoted word that
// starts at command[start] and returns the index just past its closing quote.
// Inside double quotes only `"` and `\` need the shell; `$` and a backtick
// would expand, so those refuse. A backslash before $, `, " or \ is the
// escape the shell strips, and before anything else it stays a literal
// backslash, as bash leaves it.
func appendDoubleQuotedWord(word *strings.Builder, command string, start int) (next int, ok bool) {
	for i := start; i < len(command); i++ {
		switch c := command[i]; c {
		case '"':
			return i + 1, true
		case '$', '`':
			return 0, false
		case '\\':
			if i+1 >= len(command) {
				return 0, false
			}
			i++
			switch escaped := command[i]; escaped {
			case '$', '`', '"', '\\':
				word.WriteByte(escaped)
			case '\n':
				return 0, false
			default:
				word.WriteByte('\\')
				word.WriteByte(escaped)
			}
		default:
			word.WriteByte(c)
		}
	}
	return 0, false
}
