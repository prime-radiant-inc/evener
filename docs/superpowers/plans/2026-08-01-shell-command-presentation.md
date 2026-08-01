# Lossless Shell Command Presentation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (- [ ]) syntax for tracking.

**Goal:** Render shell commands as lossless, syntax-highlighted, operator-broken source in the expanded web and TUI shell views while copying the exact raw command.

**Architecture:** A pure presentation lexer in each client returns source slices plus display-only continuation indentation. The web client adds a small shell tokenizer and reuses CodeBlock through a plain-line renderer callback; the TUI feeds the formatted command into its existing Chroma bash path. No backend or wire changes are involved, and TypeScript and Go use the same semantic fixture cases without sharing runtime code.

**Tech Stack:** React, TypeScript, CSS Modules, Vitest, Testing Library, Go, Chroma, Lipgloss, repository docs/testing.md rules.

## Global Constraints

- The raw command remains authoritative for copy and is never reconstructed from display lines.
- Every non-newline source character must remain present and in order; no whitespace, quoting, escaping, or operator normalization is allowed.
- Never add a backslash to a formatter-created display boundary.
- Split only after &&, ||, |&, |, and ; outside protected lexical regions.
- Preserve existing source newlines and explicit backslash-newline pairs; do not create duplicate blank lines when an existing newline already supplies an operator boundary.
- Do not change the tool argument wire format, transcript model, shell execution, shell output formatting, or exit-code handling.
- Do not add shfmt, a shell parser, or a frontend syntax-highlighting dependency.
- Treat malformed or unsupported source conservatively and preserve the unsplit text rather than dropping it.
- Read docs/testing.md before changing tests; tests must use deterministic real behavior and avoid large rendered-string snapshots.
- Commit each task independently with the repository hooks enabled.

---

## File map

### Web files

- Create cmd/serf-hub/frontend/src/widgets/shellcommand/shellCommand.ts for the pure display formatter and shell tokenization types/functions.
- Create cmd/serf-hub/frontend/src/widgets/shellcommand/shellCommand.test.ts for formatter and tokenizer contracts.
- Create cmd/serf-hub/frontend/src/widgets/shellcommand/index.tsx for the framed shell command widget.
- Create cmd/serf-hub/frontend/src/widgets/shellcommand/shellcommand.module.css for token colors and display-only continuation indentation.
- Modify cmd/serf-hub/frontend/src/widgets/codeblock/index.tsx to accept a plain-line renderer without changing ANSI or copy behavior.
- Modify cmd/serf-hub/frontend/src/widgets/codeblock/codeblock.test.tsx to cover the renderer callback.
- Modify cmd/serf-hub/frontend/src/widgets/index.ts to export ShellCommandBlock.
- Modify cmd/serf-hub/frontend/src/panes/session/transcript/tools/shellTool.tsx to render the command block above output.
- Modify cmd/serf-hub/frontend/src/panes/session/transcript/tools/shellTool.test.tsx to cover command ordering, exact copy, and command-only bodies.

### TUI files

- Create cmd/serf-tui/internal/msgrender/shell_command.go for the pure formatter.
- Create cmd/serf-tui/internal/msgrender/shell_command_test.go for the formatter contract and the parity fixture cases.
- Modify cmd/serf-tui/internal/msgrender/tool_bodies.go to format and highlight the raw command before output.
- Modify cmd/serf-tui/internal/msgrender/tool_bodies_test.go to cover command layout, highlighting, raw whitespace, empty output, and fallback.

## Shared formatter contract

Both implementations must use this exact result shape and boundary policy:

~~~text
line.text  = source text only, with source newlines represented as line boundaries
line.indent = 0 for the first/source-newline line, 2 for a line created after an inline operator
~~~

The formatter matches the longest operator first. When a recognized operator has
source text after it, the synthetic boundary is placed after the operator and
contiguous spaces or tabs immediately following it. Those spaces remain in the
preceding line. If the operator is followed only by horizontal whitespace and
then a source newline, the source newline supplies the boundary. If no source
text follows the operator, no empty line is created.

The scanner tracks:

- single, double, and backtick quote state;
- one-character backslash escapes, including backslash-newline preservation;
- comment state after an unquoted # at a shell-word boundary until source newline;
- balanced ( and { nesting as an opaque region for operator splitting;
- source line boundaries and a continuation indent flag.

At EOF or on malformed quoting/nesting, the current source slice is emitted
unchanged. The pure tests must verify that concatenating all line.text values
equals the raw command with only source newline bytes removed.

## Task 1: Implement the web lossless formatter

**Files:**

- Create: cmd/serf-hub/frontend/src/widgets/shellcommand/shellCommand.ts
- Create: cmd/serf-hub/frontend/src/widgets/shellcommand/shellCommand.test.ts

**Interfaces:**

- Produces ShellCommandLine:

~~~ts
export interface ShellCommandLine {
  text: string;
  indent: number;
}

export function formatShellCommand(raw: string): ShellCommandLine[];
~~~

- Later tasks consume formatShellCommand without needing the scanner's internal state.

- [ ] **Step 1: Read the frontend test rules and write the failing pure formatter tests**

Read docs/testing.md, then add table-driven Vitest cases with exact source lines
and indentation. Use a helper that checks the no-loss invariant:

~~~ts
function sourceWithoutNewlines(lines: readonly ShellCommandLine[]): string {
  return lines.map((line) => line.text).join("");
}

const cases = [
  {
    name: "chains and pipelines",
    raw: "cd /tmp && echo ok; printf '%s\\n' \"$HOME\" | tee out",
    want: [
      { text: "cd /tmp && ", indent: 0 },
      { text: "echo ok; ", indent: 2 },
      { text: "printf '%s\\n' \"$HOME\" | ", indent: 2 },
      { text: "tee out", indent: 2 },
    ],
  },
  {
    name: "longest operators",
    raw: "a || b |& c || d",
    want: [
      { text: "a || ", indent: 0 },
      { text: "b |& ", indent: 2 },
      { text: "c || ", indent: 2 },
      { text: "d", indent: 2 },
    ],
  },
  {
    name: "protected operators",
    raw: "printf '%s' \"a;b && c\" " +
      String.fromCharCode(96) + "echo x;y" + String.fromCharCode(96) +
      " foo\\;bar && done",
    want: [
      {
        text: "printf '%s' \"a;b && c\" " +
          String.fromCharCode(96) + "echo x;y" + String.fromCharCode(96) +
          " foo\\;bar && ",
        indent: 0,
      },
      { text: "done", indent: 2 },
    ],
  },
  {
    name: "comments stop operator scanning",
    raw: "echo hi # && hidden; text\nprintf done",
    want: [
      { text: "echo hi # && hidden; text", indent: 0 },
      { text: "printf done", indent: 0 },
    ],
  },
  {
    name: "nested substitutions stay opaque",
    raw: "echo $(printf 'a;b' && printf c) && echo done",
    want: [
      { text: "echo $(printf 'a;b' && printf c) && ", indent: 0 },
      { text: "echo done", indent: 2 },
    ],
  },
  {
    name: "source continuation is retained",
    raw: "printf \"left\\\\\nright\" && echo done",
    want: [
      { text: "printf \"left\\", indent: 0 },
      { text: "right\" && ", indent: 0 },
      { text: "echo done", indent: 2 },
    ],
  },
  {
    name: "malformed and trailing input stays intact",
    raw: "echo \"unterminated &&",
    want: [{ text: "echo \"unterminated &&", indent: 0 }],
  },
  {
    name: "empty input has one line",
    raw: "",
    want: [{ text: "", indent: 0 }],
  },
  {
    name: "trailing operator has no empty line",
    raw: "echo done &&",
    want: [{ text: "echo done &&", indent: 0 }],
  },
] as const;

test.each(cases)("$name", ({ raw, want }) => {
  const got = formatShellCommand(raw);
  expect(got).toEqual(want);
  expect(sourceWithoutNewlines(got)).toBe(raw.replaceAll("\n", ""));
});

test("a source newline creates a line without synthetic indentation", () => {
  expect(formatShellCommand("one\ntwo")).toEqual([
    { text: "one", indent: 0 },
    { text: "two", indent: 0 },
  ]);
});

test("an operator before an existing newline does not create a blank line", () => {
  expect(formatShellCommand("one &&\ntwo")).toEqual([
    { text: "one &&", indent: 0 },
    { text: "two", indent: 0 },
  ]);
});
~~~

The expected failure is a module-not-found or missing-export error because the
formatter file does not exist yet.

- [ ] **Step 2: Run the formatter tests and verify the failure is real**

Run from cmd/serf-hub/frontend:

~~~bash
npm test -- --run src/widgets/shellcommand/shellCommand.test.ts
~~~

Expected: FAIL before implementation, with the test module unable to import
formatShellCommand.

- [ ] **Step 3: Implement the minimal deterministic scanner**

Implement formatShellCommand as a left-to-right source-slice scanner. Keep
operator text and adjacent horizontal whitespace in the current line, and only
move the line start to a new source index when the boundary has real following
source text:

~~~ts
const SHELL_OPERATORS = ["&&", "||", "|&", "|", ";"] as const;

function operatorAt(raw: string, index: number): string | undefined {
  return SHELL_OPERATORS.find((operator) => raw.startsWith(operator, index));
}

function syntheticBoundaryEnd(raw: string, end: number): number | undefined {
  let next = end;
  while (next < raw.length && (raw[next] === " " || raw[next] === "\t")) next += 1;
  if (next === raw.length || raw[next] === "\n") return undefined;
  if (raw[next] === "\\" && raw[next + 1] === "\n") return undefined;
  return next;
}
~~~

The main loop must handle source newline before ordinary character scanning,
preserve quote state across source lines, clear comment state at each source
newline, and reset one-character escape state after the escaped byte. At
lexical depth zero it should match SHELL_OPERATORS before consuming ordinary
characters. It should append each line with raw.slice(lineStart, end) and never
trim that slice. Track a continuationIndent value that becomes 2 after a
synthetic operator boundary and returns to 0 after a source newline.

Use depth only as a protection mechanism: increment for unquoted ( or {,
decrement for the matching ) or } when depth is positive, and do not split
operators while depth is positive. Treat an unquoted # as a comment only when
the scanner is at a word boundary; consume comment text as ordinary source
until the newline branch handles it. A backslash before an operator must set
escape state so the operator remains in the current slice.

- [ ] **Step 4: Run the pure formatter tests and format the files**

Run:

~~~bash
npm test -- --run src/widgets/shellcommand/shellCommand.test.ts
npx biome check src/widgets/shellcommand/shellCommand.ts src/widgets/shellcommand/shellCommand.test.ts
~~~

Expected: PASS with all boundary, protection, newline, malformed-input, and
no-loss cases green.

- [ ] **Step 5: Commit the formatter**

~~~bash
git add cmd/serf-hub/frontend/src/widgets/shellcommand/shellCommand.ts cmd/serf-hub/frontend/src/widgets/shellcommand/shellCommand.test.ts
git commit -m "feat(web): add lossless shell command formatter"
~~~

## Task 2: Add web shell tokenization, CodeBlock line rendering, and the command widget

**Files:**

- Modify: cmd/serf-hub/frontend/src/widgets/shellcommand/shellCommand.ts
- Modify: cmd/serf-hub/frontend/src/widgets/shellcommand/shellCommand.test.ts
- Create: cmd/serf-hub/frontend/src/widgets/shellcommand/index.tsx
- Create: cmd/serf-hub/frontend/src/widgets/shellcommand/shellcommand.module.css
- Modify: cmd/serf-hub/frontend/src/widgets/codeblock/index.tsx
- Modify: cmd/serf-hub/frontend/src/widgets/codeblock/codeblock.test.tsx
- Modify: cmd/serf-hub/frontend/src/widgets/index.ts

**Interfaces:**

- Consumes formatShellCommand(raw) from Task 1.
- Adds these pure tokenizer types:

~~~ts
export type ShellCommandTokenKind =
  | "plain"
  | "command"
  | "operator"
  | "string"
  | "variable"
  | "flag"
  | "comment";

export interface ShellCommandToken {
  text: string;
  kind: ShellCommandTokenKind;
}

export function tokenizeShellCommand(
  lines: readonly ShellCommandLine[],
): ShellCommandToken[][];
~~~

- Extends CodeBlockProps with:

~~~ts
renderLine?: (line: string, lineNumber: number) => ReactNode;
~~~

lineNumber is the zero-based source line index after folding is accounted for.
The callback applies only to plain non-ANSI text. Existing ANSI rendering
continues to use AnsiLineContent.

- Produces:

~~~ts
export interface ShellCommandBlockProps {
  command: string;
}

export function ShellCommandBlock(props: ShellCommandBlockProps): JSX.Element;
~~~

- [ ] **Step 1: Write failing tokenizer, CodeBlock-hook, and widget tests**

Add pure tokenizer assertions that flatten back to each line's exact source
text and identify representative kinds:

~~~ts
test("tokenizes shell constructs without changing token text", () => {
  const lines = formatShellCommand(
    "printf '%s' \"$HOME\" && echo --name # note",
  );
  const tokens = tokenizeShellCommand(lines);
  expect(tokens.flat().map((token) => token.text).join("")).toBe(
    lines.map((line) => line.text).join(""),
  );
  expect(tokens.flat().map((token) => token.kind)).toEqual(
    expect.arrayContaining(["command", "string", "variable", "operator", "flag", "comment"]),
  );
});
~~~

Add a CodeBlock test that renders a two-line plain block with a callback and
asserts that the callback receives both source lines and their zero-based
indices. Add a ShellCommandBlock test that renders a chain and asserts that the
code text contains the inserted line break, token elements expose
data-shell-token-kind, and the Copy command button writes the exact raw
command. The raw command must include spaces, quotes, and a backslash so the
test would fail if display text were copied.

The tests should import ShellCommandBlock from its widget module while the
export barrel is wired in the implementation step.

- [ ] **Step 2: Run the focused tests and verify the failures**

Run from cmd/serf-hub/frontend:

~~~bash
npm test -- --run src/widgets/shellcommand/shellCommand.test.ts src/widgets/codeblock/codeblock.test.tsx
~~~

Expected: FAIL because the tokenizer, widget, and renderLine prop do not yet
exist.

- [ ] **Step 3: Implement conservative tokenization**

Extend shellCommand.ts with a stateful per-line tokenizer. Preserve every
character by emitting token text from source slices. Use the same quote,
escape, comment, and word-boundary rules as the formatter. Maintain
expectCommand across lines and set it after control operators:

~~~ts
const CONTROL_OPERATORS = new Set(["&&", "||", "|&", "|", ";"]);
const TOKEN_OPERATORS = ["&&", "||", "|&", ">>", "<<", ">&", "&>", "|", ";", ">", "<"];

function token(kind: ShellCommandTokenKind, text: string): ShellCommandToken {
  return { kind, text };
}
~~~

Quoted spans are string; dollar-name, dollar-brace, and dollar-question
expansions are variable; a word beginning with - at a token boundary is flag;
a word that is the first command-like word in a segment is command; control and
redirection operators are operator; comment remainder is comment; all other
source slices are plain. Keep assignment words such as NAME=value plain and
leave command expectation set until the actual command word. If a quote is
unfinished at a line boundary, mark the retained segment as string and carry
quote state to the next line. No tokenization result may drop or decode text.

- [ ] **Step 4: Add the reusable CodeBlock line callback**

Import ReactNode as a type, add the optional prop, and replace only the
plain-string rendering branch:

~~~tsx
const rendered = renderLine?.(line, tailStart + index) ?? line;
return <span>{rendered}</span>;
~~~

Use the same rendered value in the numbered branch and the non-ANSI branch.
Leave ANSI lines on AnsiLineContent, keep the existing line separators,
tail-fold calculations, and copyText behavior unchanged. Document that the
callback is for display-only line decoration and receives source text without
the line separator.

- [ ] **Step 5: Implement ShellCommandBlock and its CSS**

The component computes formatted lines and tokens, joins only line text with
display newlines for CodeBlock, and keeps the raw command in copyText:

~~~tsx
const lines = formatShellCommand(command);
const tokens = tokenizeShellCommand(lines);
const displayText = lines.map((line) => line.text).join("\n");

return (
  <CodeBlock
    text={displayText}
    copyText={command}
    copyLabel="Copy command"
    language="bash"
    renderLine={(line, lineNumber) => {
      const layout = lines[lineNumber];
      const lineTokens = tokens[lineNumber] ?? [{ text: line, kind: "plain" as const }];
      return (
        <span style={{ paddingInlineStart: String(layout?.indent ?? 0) + "ch" }}>
          {lineTokens.map((part, tokenIndex) => (
            <span
              key={tokenIndex}
              className={part.kind === "plain" ? undefined : styles[part.kind]}
              data-shell-token-kind={part.kind}
            >
              {part.text}
            </span>
          ))}
        </span>
      );
    }}
  />
);
~~~

Use CSS Modules and existing terminal theme variables: command and operator
colors may use --ansi-bright-blue and --ansi-yellow, strings --ansi-green,
variables --ansi-cyan, flags --ansi-magenta, and comments --ink-mid with italic
styling. Keep plain text on --ink-hi. Do not use dangerouslySetInnerHTML.
Export the widget from widgets/index.ts.

- [ ] **Step 6: Run the web widget and CodeBlock tests**

Run:

~~~bash
npm test -- --run src/widgets/shellcommand/shellCommand.test.ts src/widgets/codeblock/codeblock.test.tsx
npx biome check src/widgets/shellcommand src/widgets/codeblock/index.tsx src/widgets/codeblock/codeblock.test.tsx src/widgets/index.ts
~~~

Expected: PASS with existing CodeBlock behavior unchanged and new shell
token/copy/indent behavior covered.

- [ ] **Step 7: Commit the reusable web command presentation**

~~~bash
git add cmd/serf-hub/frontend/src/widgets/shellcommand cmd/serf-hub/frontend/src/widgets/codeblock/index.tsx cmd/serf-hub/frontend/src/widgets/codeblock/codeblock.test.tsx cmd/serf-hub/frontend/src/widgets/index.ts
git commit -m "feat(web): render lossless highlighted shell commands"
~~~

## Task 3: Integrate the command widget into the web shell body

**Files:**

- Modify: cmd/serf-hub/frontend/src/panes/session/transcript/tools/shellTool.tsx
- Modify: cmd/serf-hub/frontend/src/panes/session/transcript/tools/shellTool.test.tsx

**Interfaces:**

- Consumes ShellCommandBlock from Task 2.
- Keeps shellCommand(args) as the existing command-then-cmd extraction used by
  summary and body.
- Leaves AnsiTailBuffer, exit-code parsing, failed, detail, and autoExpand
  untouched.

- [ ] **Step 1: Change the web body tests first**

Replace the old output-only body assertion with an ordering assertion:

~~~tsx
test("body renders the raw formatted command above the existing output block", () => {
  const Body = toolRendererFor("shell").body!;
  const raw = "cd /tmp && printf '%s\\n' \"$HOME\"";
  const { container } = render(
    <Body item={withCommand(raw, { output: "ok\n[exit 0]" })} live={false} />,
  );
  const codes = Array.from(container.querySelectorAll("code"));
  expect(codes).toHaveLength(2);
  expect(codes[0].textContent).toContain("cd /tmp &&");
  expect(codes[0].textContent).toContain("printf '%s\\n' \"$HOME\"");
  expect(codes[1].textContent).toContain("ok");
  expect(container.textContent).not.toContain("$ ");
});
~~~

Add a copy test that clicks the button named Copy command and expects the exact
raw string, including the backslash and original spaces. Change the empty
output test to assert that a command-only body contains one command code block.
Add a separate no-command/no-output test to preserve the null-body behavior.

- [ ] **Step 2: Run the shell body tests and verify the changed expectations fail**

Run from cmd/serf-hub/frontend:

~~~bash
npm test -- --run src/panes/session/transcript/tools/shellTool.test.tsx
~~~

Expected: the new command-block assertions fail while the existing output
tests remain diagnostic, proving the integration is not present yet.

- [ ] **Step 3: Render command and output as separate body blocks**

Extract the raw command once in ShellBody, continue updating the output tail
only for output, and return no body only when both command and output are empty:

~~~tsx
const command = shellCommand(parseArgs(item.argumentsJSON));
const output = item.output ?? "";

if (command === "" && output === "") return null;

return (
  <>
    {command !== "" && <ShellCommandBlock command={command} />}
    {output !== "" && (
      <CodeBlock text={body} copyText={tail.copyText} copyLabel="Copy output" ansi />
    )}
  </>
);
~~~

Preserve the current live/settled tail construction exactly, including its
truncation notice and copy bytes. Update stale comments that say the body is
output-only; the row summary still owns collapsed command presentation, while
the expanded body now owns the readable command block.

- [ ] **Step 4: Run web integration and type checks**

Run:

~~~bash
npm test -- --run src/panes/session/transcript/tools/shellTool.test.tsx src/widgets/shellcommand/shellCommand.test.ts src/widgets/codeblock/codeblock.test.tsx
npm run typecheck
npm run lint
~~~

Expected: PASS. The command copy test must receive the original raw argument,
and existing output ANSI/tail tests must remain green.

- [ ] **Step 5: Commit the web shell integration**

~~~bash
git add cmd/serf-hub/frontend/src/panes/session/transcript/tools/shellTool.tsx cmd/serf-hub/frontend/src/panes/session/transcript/tools/shellTool.test.tsx
git commit -m "feat(web): show formatted shell command in tool body"
~~~

## Task 4: Implement the TUI lossless formatter

**Files:**

- Create: cmd/serf-tui/internal/msgrender/shell_command.go
- Create: cmd/serf-tui/internal/msgrender/shell_command_test.go

**Interfaces:**

- Produces the package-private type used by the TUI:

~~~go
type shellCommandLine struct {
    text   string
    indent int
}

func formatShellCommand(command string) []shellCommandLine
~~~

- The Go formatter must produce the same line text and indent values as the
  TypeScript fixtures from Task 1 for the same raw inputs.

- [ ] **Step 1: Write the failing Go formatter tests**

Read docs/testing.md, then add a table-driven test using the same semantic cases
and expected source slices:

~~~go
func TestFormatShellCommandFixtures(t *testing.T) {
    tests := []struct {
        name string
        raw  string
        want []shellCommandLine
    }{
        {
            name: "chains and pipelines",
            raw:  "cd /tmp && echo ok; printf '%s\\n' \"$HOME\" | tee out",
            want: []shellCommandLine{
                {text: "cd /tmp && ", indent: 0},
                {text: "echo ok; ", indent: 2},
                {text: "printf '%s\\n' \"$HOME\" | ", indent: 2},
                {text: "tee out", indent: 2},
            },
        },
        {
            name: "protected and nested operators",
            raw:  "echo \"a;b\" $(printf 'c && d') foo\\;bar && done",
            want: []shellCommandLine{
                {text: "echo \"a;b\" $(printf 'c && d') foo\\;bar && ", indent: 0},
                {text: "done", indent: 2},
            },
        },
        {
            name: "source continuation",
            raw:  "printf \"left\\\nright\" && echo done",
            want: []shellCommandLine{
                {text: "printf \"left\\", indent: 0},
                {text: "right\" && ", indent: 0},
                {text: "echo done", indent: 2},
            },
        },
        {
            name: "comments and malformed input",
            raw:  "echo hi # && hidden; text\nprintf \"unterminated &&",
            want: []shellCommandLine{
                {text: "echo hi # && hidden; text", indent: 0},
                {text: "printf \"unterminated &&", indent: 0},
            },
        },
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got := formatShellCommand(tt.raw)
            if !reflect.DeepEqual(got, tt.want) {
                t.Fatalf("formatShellCommand(%q) = %#v, want %#v", tt.raw, got, tt.want)
            }
            var source strings.Builder
            for _, line := range got {
                source.WriteString(line.text)
            }
            if gotSource := source.String(); gotSource != strings.ReplaceAll(tt.raw, "\n", "") {
                t.Fatalf("formatShellCommand(%q) lost source: %q", tt.raw, gotSource)
            }
        })
    }
}
~~~

Also cover empty input, a trailing operator, and an operator directly before a
source newline. The failing test should be a compile error because the
formatter file does not exist.

- [ ] **Step 2: Run the focused Go test and verify the failure**

Run from the new worktree:

~~~bash
go test ./cmd/serf-tui/internal/msgrender -run 'TestFormatShellCommand' -count=1
~~~

Expected: FAIL because formatShellCommand and shellCommandLine are undefined.

- [ ] **Step 3: Implement the Go scanner with byte-preserving slices**

Use the same state machine and boundary policy as Task 1. Operators are ASCII,
so byte indices are safe for identifying syntax while command[start:end]
preserves all UTF-8 bytes in ordinary text. Define the operator matcher and
boundary helper explicitly:

~~~go
var shellCommandOperators = []string{"&&", "||", "|&", "|", ";"}

func shellOperatorAt(command string, index int) string {
    for _, operator := range shellCommandOperators {
        if strings.HasPrefix(command[index:], operator) {
            return operator
        }
    }
    return ""
}

func shellSyntheticBoundaryEnd(command string, end int) int {
    next := end
    for next < len(command) && (command[next] == ' ' || command[next] == '\t') {
        next++
    }
    if next == len(command) || command[next] == '\n' {
        return -1
    }
    if command[next] == '\\' && next+1 < len(command) && command[next+1] == '\n' {
        return -1
    }
    return next
}
~~~

Append source slices exactly as found, set indent 2 only after a synthetic
operator boundary, and reset it after source newline. Preserve quote state
through source newlines, reset comment state at newline, protect escaped
operators, and keep unmatched nesting opaque to the end.

- [ ] **Step 4: Run Go formatting and formatter tests**

Run:

~~~bash
gofmt -w cmd/serf-tui/internal/msgrender/shell_command.go cmd/serf-tui/internal/msgrender/shell_command_test.go
go test ./cmd/serf-tui/internal/msgrender -run 'TestFormatShellCommand' -count=1
~~~

Expected: PASS, including the source-preservation invariant and the same line
boundaries/indents as the TypeScript fixture corpus.

- [ ] **Step 5: Commit the TUI formatter**

~~~bash
git add cmd/serf-tui/internal/msgrender/shell_command.go cmd/serf-tui/internal/msgrender/shell_command_test.go
git commit -m "feat(tui): add lossless shell command formatter"
~~~

## Task 5: Integrate formatted command highlighting into the TUI shell body

**Files:**

- Modify: cmd/serf-tui/internal/msgrender/tool_bodies.go
- Modify: cmd/serf-tui/internal/msgrender/tool_bodies_test.go

**Interfaces:**

- Consumes formatShellCommand(command) from Task 4.
- Keeps ShellBody(args, output, width) as the public renderer entry point.
- Uses the existing highlightBlock(text, "bash") and its plain-text fallback.
- Does not change shell renderer aliases, target clipping, output highlighting,
  or width arguments.

- [ ] **Step 1: Write failing TUI body tests**

Add a semantic layout test that removes SGR sequences with the existing
ansiPattern, then checks command-before-output ordering and two-column
continuation alignment:

~~~go
func TestShellBodyFormatsAndHighlightsCommand(t *testing.T) {
    withTestColorProfile(t)
    command := "cd /tmp && echo \"a;b\"; printf '%s\\n' \"$HOME\" | tee out"
    got := ShellBody(ToolArgs{"command": command}, "ok", 60)
    plain := ansiPattern.ReplaceAllString(got, "")
    want := "$ cd /tmp && \n  echo \"a;b\"; \n  printf '%s\\n' \"$HOME\" | \n  tee out\n"
    if !strings.Contains(plain, want) {
        t.Fatalf("ShellBody command layout = %q, want command block %q", plain, want)
    }
    if strings.Index(plain, "tee out") > strings.Index(plain, "ok") {
        t.Fatalf("command must precede output: %q", plain)
    }
    if !strings.Contains(got, "\x1b[") {
        t.Fatalf("formatted command should use Chroma styling: %q", got)
    }
}
~~~

Add tests that command-only input still renders, leading/trailing command
whitespace is retained instead of TrimSpace-normalized, and Chroma failure
falls back to the formatted plain command. The fallback test may temporarily
replace getChromaLexer with a function returning nil and restore it with
t.Cleanup; it must assert visible source, not a full ANSI string.

- [ ] **Step 2: Run the focused TUI body tests and verify the failures**

Run:

~~~bash
go test ./cmd/serf-tui/internal/msgrender -run 'TestShellBody' -count=1
~~~

Expected: the new layout/highlighting assertions fail because the current body
trims and renders the command as one unformatted line.

- [ ] **Step 3: Add a display-only command renderer and preserve raw args**

Replace the current trimmed one-line command path with a helper that joins
formatted source lines after applying only metadata indentation:

~~~go
func renderShellCommand(command string) string {
    formatted := formatShellCommand(command)
    sourceLines := make([]string, 0, len(formatted))
    for _, line := range formatted {
        sourceLines = append(sourceLines, strings.Repeat(" ", line.indent)+line.text)
    }
    source := strings.Join(sourceLines, "\n")
    highlighted := highlightBlock(source, "bash")
    if highlighted == "" {
        highlighted = source
    }
    prompt := lipgloss.NewStyle().Foreground(tuitheme.ActiveTheme().TextMuted).Render("$ ")
    return prompt + highlighted
}

func ShellBody(args ToolArgs, output string, width int) string {
    var lines []string
    if command := args.Str("command"); command != "" {
        lines = append(lines, renderShellCommand(command))
    }
    if output != "" {
        highlighted := highlightBlock(output, "bash")
        if highlighted != "" {
            lines = append(lines, highlighted)
        } else {
            lines = append(lines, output)
        }
    }
    return strings.Join(lines, "\n")
}
~~~

Do not call TrimSpace on the command. The raw command is a display source,
and the copy contract is represented by keeping it intact through formatting.
The prompt is the only added non-source text; it is muted and appears only on
the first command line. Apply the same Chroma/fallback behavior to the entire
formatted command as the existing output path.

- [ ] **Step 4: Run focused and package-wide TUI verification**

Run:

~~~bash
gofmt -w cmd/serf-tui/internal/msgrender/tool_bodies.go cmd/serf-tui/internal/msgrender/tool_bodies_test.go
go test ./cmd/serf-tui/internal/msgrender -run 'Test(ShellBody|FormatShellCommand)' -count=1
go test ./cmd/serf-tui/internal/msgrender -count=1
~~~

Expected: PASS. Existing output-highlighting tests must remain green, command
text must be visible when output is empty, and the fallback test must retain
all formatted source text.

- [ ] **Step 5: Commit the TUI shell integration**

~~~bash
git add cmd/serf-tui/internal/msgrender/tool_bodies.go cmd/serf-tui/internal/msgrender/tool_bodies_test.go
git commit -m "feat(tui): show formatted highlighted shell commands"
~~~

## Final verification

After every task commit has passed its task reviewer, run the complete relevant
checks from the new worktree. Frontend commands run from
cmd/serf-hub/frontend; Go commands run from the worktree root:

~~~bash
# frontend
npm test -- --run src/panes/session/transcript/tools/shellTool.test.tsx src/widgets/shellcommand/shellCommand.test.ts src/widgets/codeblock/codeblock.test.tsx
npm run typecheck
npm run lint
npm run build

# TUI
go test ./cmd/serf-tui/internal/msgrender -count=1
go test ./cmd/serf-tui/... -count=1

# repository hygiene
git diff --check
git status --short
~~~

Review the final diff for these specific regressions before reporting success:

- no backend, wire, execution, output, or exit-code files changed;
- no TrimSpace, shell formatter, or added escaping was introduced on the
  canonical command path;
- command copy uses the raw command, not display text or folded visible lines;
- existing CodeBlock ANSI behavior and output copy behavior remain unchanged;
- the web command block precedes output and the TUI command block precedes
  output;
- both formatter tests preserve source characters and use the same semantic
  fixture expectations;
- no unrelated worktree files were modified.
