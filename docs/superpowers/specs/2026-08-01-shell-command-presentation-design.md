# Lossless shell-command presentation

**Status:** Approved for implementation
**Date:** 2026-08-01

## Problem

The shell runner currently puts the command in the transcript row summary and
renders only the command's output in the expanded web body. The TUI renders the
raw command on one line and highlights the output, but not the command. Long
commands therefore become difficult to scan, and neither surface gives shell
syntax enough visual structure to distinguish operators, strings, variables,
and comments.

The command is already present in the tool arguments. This is a presentation
problem, not a protocol or execution problem.

## Decision

Add a lossless presentation layer for shell commands in both the web UI and the
TUI.

The presentation layer will:

- show the command above its output in the expanded web shell body;
- keep collapsed row summaries compact and unchanged in meaning;
- create display-only line breaks after selected shell control operators;
- preserve source newlines and explicit backslash-newline continuations;
- syntax-highlight the command as shell/bash source;
- copy the exact raw command supplied by the tool;
- use the same lexical rules and fixture corpus in TypeScript and Go, without
  introducing a shared runtime dependency between the two clients.

The presentation layer will not rewrite, normalize, quote, escape, trim, or
otherwise canonicalize commands. In particular, it will never add a backslash
to make a display line look shell-continuable.

## Goals

1. Make common command chains readable at a glance in the expanded web and TUI
   views.
2. Make shell syntax visually legible without requiring a heavyweight shell
   formatter or a backend change.
3. Make command copying exact and independent of folding, wrapping, line
   breaks, indentation, or syntax spans.
4. Keep malformed, unfamiliar, and partially streamed command text safe to
   render: no source characters may disappear.
5. Give both clients deterministic, unit-testable formatting and highlighting
   behavior.

## Non-goals

- Do not run `shfmt`, a shell parser, or any formatter that changes source
  spelling.
- Do not change the tool argument wire format, transcript model, shell
  execution, output formatting, or exit-code handling.
- Do not change the collapsed summary contract beyond using the existing raw
  command value.
- Do not attempt to validate whether a command is valid shell.
- Do not split on standalone `&` in this iteration. It is ambiguous around
  redirections (`>&`, `&>`) and is less common in the runner's generated
  commands; leaving it untouched is safer than pretending to parse shell
  grammar.
- Do not add a general-purpose syntax-highlighting framework to the frontend.

## Existing behavior and integration points

### Web UI

`cmd/serf-hub/frontend/src/panes/session/transcript/tools/shellTool.tsx`
currently extracts `command` (falling back to `cmd`), uses it in the summary,
and renders only the output through `CodeBlock` with ANSI support. The expanded
body is therefore the natural place for a command block followed by the
existing output block.

`cmd/serf-hub/frontend/src/widgets/codeblock/index.tsx` owns the framed mono
block, copy control, line splitting, and tail folding. It currently treats
non-ANSI text as plain source and deliberately has no syntax highlighter. The
shell command presentation should reuse that framing, copy, and folding
behavior rather than duplicate it.

### TUI

`cmd/serf-tui/internal/msgrender/tool_bodies.go` already renders shell output
with Chroma's bash lexer. `ShellBody` currently prefixes the unformatted raw
command with `$ ` and then appends highlighted output. The command formatter
will sit immediately before that existing output path, and the formatted
command will use the same Chroma bash highlighting and active-theme fallback.

No server-side changes are required because `ItemModel.argumentsJSON` and
`ToolArgs` already preserve the raw command.

## Lossless presentation contract

The formatter receives one raw string and returns display lines. A display line
contains a source slice plus metadata describing display-only indentation.

The TypeScript shape is:

```ts
export interface ShellCommandLine {
  text: string;
  indent: number;
}

export function formatShellCommand(raw: string): ShellCommandLine[];
```

The Go equivalent is an unexported value used by the TUI renderer:

```go
type shellCommandLine struct {
	text   string
	indent int
}

func formatShellCommand(command string) []shellCommandLine
```

The contract is:

- `raw` is retained unchanged for copying and is never reconstructed from
  display lines;
- every non-newline source character appears in exactly one returned line's
  `text`, in the original order;
- a source newline becomes a display line boundary and is not duplicated in a
  line's `text`;
- a formatter-created boundary is display-only and is allowed to add a line
  break and visual indentation, but not source characters;
- `indent` is metadata, not source whitespace. The web UI expresses it with
  CSS padding; the TUI expresses it with a display prefix before highlighting
  or line rendering;
- only lines created after a recognized inline operator receive the standard
  continuation indentation (`2` display columns). Existing source newlines,
  including backslash-newline continuations, retain their source whitespace
  without additional indentation;
- an empty raw command still produces one empty line, so the command block can
  be rendered consistently;
- if the scanner encounters malformed quoting, an unterminated comment, or an
  unsupported construct, it must preserve all text and may return the
  unsplit remainder as a raw line. Rendering must fail safe, never fail closed.

For example, the raw command:

```text
cd /tmp && echo "a;b"; printf '%s\n' "$HOME" | tee out
```

is displayed conceptually as:

```text
cd /tmp &&
[display indent] echo "a;b";
[display indent] printf '%s\n' "$HOME" |
[display indent] tee out
```

`[display indent]` represents two visual columns supplied by the renderer; it
is not source whitespace. The source space after each operator remains at the
start of the following source slice, even though it is not shown separately in
the conceptual rendering. The line breaks and continuation indents above are
not part of the copied command. The semicolon inside the double-quoted string
and the `\n` inside the single-quoted string do not create boundaries.

## Formatting rules

The formatter is a small presentation lexer, not a shell grammar. It scans the
raw string from left to right while tracking enough lexical state to avoid
breaking text that merely resembles an operator.

### Recognized boundaries

At lexical depth zero, outside quotes, comments, and escapes, split *after*:

- `&&`
- `||`
- `|&`
- `|`
- `;`

The longest operator is matched first, so `||` is one operator and not two
single pipes. The operator remains at the end of the preceding display line;
the following source text begins the next display line. Existing whitespace
around the operator remains in the source slice on the same side where it
appeared. No trimming is performed.

If an operator is immediately followed by an existing source newline, the
existing newline supplies the display boundary and the formatter does not add a
second empty line.

### Lexical protection

- Single-quoted, double-quoted, and backtick-quoted text is opaque to boundary
  detection. Operators inside it remain on the same display line.
- A backslash protects the following character for boundary detection. A
  backslash-newline pair is preserved exactly and remains an ordinary source
  continuation, with the newline represented by the existing display boundary.
- An unquoted `#` at the beginning of a shell word starts a comment that runs
  through the existing newline. Operators in that comment are not boundaries.
  A `#` embedded in a word such as `foo#bar` is ordinary text.
- Command substitutions and other explicitly enclosed regions are opaque for
  this presentation pass. Operators inside `$(`…`)`, backticks, or balanced
  grouping delimiters do not split the outer command. The scanner tracks
  balanced nesting only to protect boundaries; it does not validate shell
  grammar.
- Existing newlines are always preserved as boundaries. The scanner resets
  comment state at an existing newline and keeps quote/escape state so a
  multiline quoted string remains protected.

### Failure behavior

The formatter must be total over arbitrary strings. If it cannot confidently
classify a character sequence, it leaves that sequence in the current line.
The final concatenation of line text, with source newlines omitted as line
separators, must equal the raw command with only source newline characters
removed. This is the invariant that catches accidental loss or normalization.

## Web rendering

Add a small shell-command widget under
`cmd/serf-hub/frontend/src/widgets/shellcommand/`. It will:

1. call `formatShellCommand(raw)`;
2. tokenize each source line without changing token text;
3. render through the existing `CodeBlock` frame, copy button, and fold
   behavior;
4. pass `copyText={raw}` and `copyLabel="Copy command"`;
5. apply visual indentation from `ShellCommandLine.indent` to the line wrapper;
6. apply CSS token classes for shell syntax.

The smallest reusable `CodeBlock` extension is a `renderLine` callback for
plain (non-ANSI) blocks:

```ts
renderLine?: (line: string, lineNumber: number) => React.ReactNode;
```

The callback is used for both numbered and unnumbered plain lines. It does not
change ANSI handling, existing output behavior, or copy behavior. The shell
widget uses the callback to render token spans and display-only indentation.

The web tokenizer preserves each line's text exactly while assigning these
conservative classes where recognition is unambiguous:

- `command` for the first command-like word in a command segment;
- `operator` for shell control and redirection operators;
- `string` for quoted spans;
- `variable` for parameter and environment expansions;
- `flag` for option-like words beginning with `-`;
- `comment` for comment text;
- `plain` for everything else.

It is acceptable for a token to remain `plain` when the lexer is uncertain.
The highlighter must never re-emit escaped text, decode entities, or use
`dangerouslySetInnerHTML`; React text nodes are the safety boundary.

`ShellBody` will render the command block whenever a command is present,
followed by the existing output block when output is present. A missing command
and missing output still result in no body, matching the current empty-output
behavior. The output's ANSI tail buffer, copy semantics, and truncation notice
remain unchanged.

## TUI rendering

Add a pure formatter in
`cmd/serf-tui/internal/msgrender/shell_command.go` and use it from
`ShellBody`.

The renderer will:

1. format the raw `command` into source lines and indentation metadata;
2. apply the display-only continuation prefix to those lines;
3. pass the formatted command text to the existing Chroma bash highlighter;
4. fall back to the unstyled formatted text if Chroma cannot highlight it;
5. prefix the first line with the existing muted `$ ` prompt and keep
   continuation lines visually aligned beneath the command text;
6. append the existing highlighted output after the command block.

The command must render even when output is empty. The existing output text,
width behavior, and renderer aliases remain unchanged except for the addition of
the formatted command block.

## Testing contract

Read `docs/testing.md` before changing tests. Tests must exercise the pure
formatting/tokenization behavior and the public rendering seams, not snapshot
large generated HTML or ANSI strings.

The TypeScript formatter tests must cover:

- `&&`, `||`, `|&`, `|`, and `;` boundaries;
- operators inside single quotes, double quotes, backticks, comments, and
  escaped text;
- nested command substitutions or balanced regions;
- existing newlines and explicit backslash-newline continuations;
- empty input, trailing operators, repeated operators, and malformed quotes;
- the no-loss invariant over a corpus of representative commands.

The TypeScript widget tests must cover:

- command text appears above output in an expanded shell body;
- command and output remain separate blocks;
- the command copy control writes the exact raw command, including whitespace,
  quotes, operators, and backslashes;
- token classes are present for representative shell constructs;
- long formatted commands still use `CodeBlock` folding without changing copy
  text.

The Go formatter tests must use the same semantic fixture corpus and assert the
same line boundaries, source text, and indentation metadata. The TUI renderer
tests must cover command highlighting, the `$ ` prompt, continuation alignment,
empty output, and plain-text fallback when Chroma is unavailable.

Run focused tests while implementing, then the relevant frontend test suite,
`gofmt`/Go tests, and the repository's normal validation commands before
claiming completion. Report missing dependencies or baseline failures as
limitations rather than treating them as passes.

## Acceptance criteria

- An expanded web shell tool shows a readable, syntax-highlighted command above
  its output.
- The TUI shows the same command structure with bash highlighting and aligned
  continuation lines.
- `;`, `&&`, `||`, `|&`, and `|` split only outside protected lexical regions.
- No command character is normalized, removed, or newly escaped.
- Copying the command returns the exact raw argument string.
- Existing shell output rendering and exit-state behavior remain intact.
- The web and TUI test suites contain deterministic regression coverage for the
  formatter and both renderers.
