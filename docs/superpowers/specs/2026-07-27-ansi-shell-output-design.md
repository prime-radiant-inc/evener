# ANSI Shell Output Design

**Kata:** mwa9

## Problem

The web transcript renders captured shell output as ordinary `CodeBlock` text.
When a command detects a color-capable output stream, its ANSI Select Graphic
Rendition (SGR) sequences therefore appear as literal fragments such as
`[2m`, `[1m`, and `[32m`. The durable tool result is correct; the presentation
layer is treating terminal styling instructions as printable text.

Shell output must preserve its original bytes for transcript fidelity and
copying while presenting supported ANSI styling to the reader.

## Considered Approaches

1. Parse SGR sequences into structured runs and render those runs as React
   elements. Use the low-level `anser` parser rather than accepting generated
   HTML. This keeps output escaped by React, supports the common ANSI color and
   text-attribute vocabulary, and leaves Serf in control of theme integration.
2. Embed a terminal emulator. This would also implement cursor addressing,
   erasure, and alternate-screen behavior, but it would add a large interactive
   subsystem that conflicts with the transcript's wrapped, selectable,
   foldable log presentation.
3. Implement an ANSI state machine in Serf. This avoids a dependency but
   duplicates a subtle standard whose resets, extended colors, and malformed
   input behavior are easy to get wrong.

Approach 1 is the smallest reliable change for captured command output.

## Design

Add an opt-in ANSI presentation mode to `CodeBlock`. Its `text` prop remains the
single source for line folding and clipboard writes. ANSI mode changes only the
React children rendered inside the existing `<pre><code>` structure.

The renderer parses the complete `text` value into styled logical lines before
`CodeBlock` selects a folded tail. This preserves an SGR state that began on a
hidden earlier line and continues into the visible tail. It emits text and
`<span>` elements, never HTML. Supported presentation includes:

- standard and bright foreground/background colors;
- 256-color and 24-bit color values;
- bold, dim, italic, underline, inverse, and strike-through;
- selective and full resets.

The sixteen named terminal colors map through dedicated light- and dark-theme
tokens so they remain legible on Serf surfaces. Extended palette and truecolor
values use parser-validated color values. Blink is ignored: captured output
must not introduce animation. Unsupported cursor, erase, device-control, and
OSC sequences are consumed rather than displayed or executed. This component
is a styled log renderer, not a terminal emulator.

Enable ANSI mode only in the descriptor shared by `shell`, `exec_command`, and
`run_shell_command`. Other `CodeBlock` consumers and the generic raw-tool
fallback retain their existing plain-text contract.

Long-output behavior remains unchanged. Shell output is first bounded by the
existing character tail helper, and `CodeBlock` continues to fold long output
to its visible tail. ANSI parsing operates on the complete bounded `text` prop
before that line fold, while copy writes that same prop exactly, including its
escape sequences.

## Testing

Use deterministic frontend tests with literal escape bytes and no browser,
provider, or network dependency.

The first RED case renders the screenshot-shaped Vitest output and asserts that
the text contains no printable SGR fragments while dim, bold, green, and reset
runs carry distinct presentation. Additional cases cover multiline state,
selective reset, named/bright colors, 256-color, truecolor, inverse,
unsupported controls, and malformed input.

Integration tests invoke the registered shell descriptor for all three
bash-family tool names and prove ANSI mode is enabled there but not in the
generic raw-tool renderer. Existing `CodeBlock` tests prove folding remains
line-based and add a clipboard assertion that the exact source text, escape
bytes included, is copied.

Mutation proof is required: the screenshot regression must fail against the
plain `CodeBlock` implementation before production code is added.

## Success Criteria

- Bash-family tool results no longer display raw SGR fragments.
- Supported ANSI styling is visible and theme-legible.
- Output text, folding, wrapping, selection, and disclosure layout remain
  unchanged.
- Copy returns the original supplied text with no normalization.
- Unsupported control sequences cannot alter page structure or execute markup.
- Focused transcript/widget tests, frontend lint, typecheck, and production
  build pass.
