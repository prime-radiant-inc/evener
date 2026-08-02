# Expanded Shell Tool Calls Use Pretty-Printed Commands

## Goal

When a transcript expands a `shell`, `exec_command`, or `run_shell_command` tool call, show the existing pretty-printed command only. Do not show the `bash` language label in the command block's top-right corner.

## Design

Keep `ShellCommandBlock` as the single renderer for shell commands. It already formats and tokenizes commands and preserves the original command for copying. Remove its `language="bash"` decoration so `CodeBlock` renders the formatted command without a language label. Keep the collapsed row summary (`Ran …`) unchanged; it remains useful context before expansion and is not the duplicate command block targeted by this change.

## Testing

Extend the shell command block or shell tool renderer tests to verify that formatted multiline commands remain rendered and that no `bash` language label appears. Preserve existing copy behavior and formatting assertions. Run the focused frontend tests for the shell command and shell tool renderers.

## Scope

This change affects presentation only. It does not change command execution, parsing, formatting rules, copy payloads, output rendering, failure handling, or disclosure behavior.
