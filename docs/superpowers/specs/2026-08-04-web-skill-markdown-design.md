# Web use_skill Markdown rendering design

## Problem

The web transcript renderer displays an expanded `use_skill` result as one plain-text block. Skill output contains Markdown, so headings, lists, and fenced code are not rendered structurally.

## Scope

Change only the expanded body of the `use_skill` tool renderer. Keep the existing summary, Skill/Location metadata, empty-output behavior, and disclosure behavior unchanged.

## Design

In `cmd/serf-hub/frontend/src/panes/session/transcript/tools/useSkillTool.tsx`, pass non-empty `item.output` to the existing shared `Markdown` widget instead of rendering it in a plain `<div>`. This reuses the repository's established Marked-plus-DOMPurify pipeline and avoids duplicating parsing or sanitization logic.

The output remains the complete skill-tool result, including its existing metadata and separator. The tool summary continues to identify the activated skill; only the content shown after disclosure changes from plain text to rendered Markdown.

## Testing

Add a deterministic renderer regression test using representative `use_skill` output containing a heading, list, and fenced code block. Open the tool disclosure and assert the corresponding semantic Markdown elements are present, while preserving coverage that empty output renders no body. Run the focused frontend test and the applicable web test gate.

## Non-goals

- Do not redesign the disclosure or summary.
- Do not create a skill-specific Markdown implementation.
- Do not change backend skill output or metadata formatting.
