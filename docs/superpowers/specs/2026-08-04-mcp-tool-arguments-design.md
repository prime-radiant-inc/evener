# MCP Tool Arguments Rendering Design

## Goal

Show arguments for unregistered MCP tool calls in the transcript. Render valid JSON in a readable form without changing dedicated tool renderers.

## Architecture

Add a reusable argument-body component under the session transcript tool-rendering code. Assign it to `DEFAULT_DESCRIPTOR` in `toolRenderers.ts`. The default descriptor already handles unregistered tools, including MCP tools, so this change covers new MCP tools without maintaining a name list or changing registered tool descriptors.

The component receives the existing `ToolRenderProps` and reads `item.argumentsJSON`. It renders the arguments before the existing raw output body. The display text is formatted for readability, while the original `argumentsJSON` is passed to the `CodeBlock` copy control as `copyText`, so copying never changes the wire value. `ToolCallItem` continues to own disclosure state, failure handling, and output-image rendering.

## Rendering behavior

- Parse `argumentsJSON` when it contains valid JSON.
- Render valid JSON with two-space indentation in a semantic preformatted code block using the existing code-block presentation.
- Copying arguments returns the original `argumentsJSON` byte-for-byte, including surrounding whitespace and values that cannot be represented exactly by JavaScript numbers.
- If parsing fails, render the original argument string unchanged instead of hiding it or throwing.
- Render no argument block when arguments are absent or empty.
- Keep tool rows collapsed by default. Failed calls retain their existing auto-expansion behavior.
- Preserve existing raw output and error rendering alongside the new argument block.

## Accessibility and errors

The argument block uses readable, copyable text and an accessible label. Long and nested values remain visible without truncating the underlying argument text. Malformed JSON is treated as displayable tool input, not as a renderer failure.

## Tests

Add focused transcript tests covering:

1. Nested valid JSON is pretty-printed.
2. Malformed JSON falls back to the original text.
3. Missing or empty arguments render no argument block.
4. Arguments render with existing output and error content.
5. Clicking `Copy arguments` returns the original argument string rather than the formatted display text.

Run the focused transcript tests and `make test-web` before completion. Run `make test-web-browser` if the host supports the browser gate.
