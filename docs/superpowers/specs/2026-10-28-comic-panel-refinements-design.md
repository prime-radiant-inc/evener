# Comic reading refinements

## Intent and scope

Make the shipped Comic presentation read like a comic page: bounded panels,
formatted dialogue, technical close-ups and child conversations introduced where
they begin. This changes the hub web presentation on desktop and phones. It does
not change native clients, session execution, composer admission or routing.

Jesse requested compact panels, rendered Markdown and Meanwhile strips at thread
creation. For code and tables, he selected standalone panels without characters.
The dimensions and overflow treatment below are implementation proposals for his
approval.

## Approaches

Keeping formatted blocks inside taller speech balloons preserves their contents
but gives characters implausibly large balloons. Breaking raw text into small
balloons keeps their size but damages Markdown structure. Jesse's chosen treatment
is a technical insert: the code or table becomes the subject of its own panel.
Use that treatment while retaining speech and thought balloons for surrounding
prose.

## Reading treatment

- **Bounded panels.** Ordinary panels have a preferred maximum width of 26rem,
  constrained by available space. Existing paired compositions remain paired when
  they fit; a single panel does not expand into an empty second slot. Align panels
  to the reading flow's left edge, leaving unused space as gutter. Phones use one
  column without shrinking lettering or forcing two panels across.
- **Rendered dialogue.** Render headings, emphasis, lists, quotations, links and
  inline code. Prose retains Comic Sans and the speaker's speech or thought
  balloon. Parse Markdown before composing panels; preserve block structure and
  document-wide references instead of parsing arbitrary raw-text cuts. Measure the
  rendered content at the same bounded width used on screen.
- **Technical inserts.** Each code block or table occupies its own ink-framed
  panel at its original position in the message. Omit the character, scene,
  balloon and tail. Retain unobtrusive source attribution. Code uses the existing
  fixed-width type treatment so indentation and alignment remain legible; table
  lettering uses Comic Sans. No terminal-window controls, invented narration or
  decorative labels.
- **Room for evidence.** Inserts may be wider than dialogue, up to 40rem, but never
  stretch with the viewport. Preserve each complete block and let its height grow.
  Wide content scrolls horizontally within the insert when needed. Do not truncate,
  shrink text to fit, or add a nested vertical reader. Narrow inserts fit the phone.
- **Continuity.** Prose before and after a block stays on the corresponding side
  of that insert. A code-only message produces an insert without an empty character
  panel. The same rules apply inside expanded child conversations.

```mermaid
flowchart LR
    A[Character speaks] --> B[Code close-up, no character]
    B --> C[Table close-up, no character]
    C --> D[Character continues]
```

The diagram describes a message containing prose, code, a table and more prose.
Only content present in the source appears; the renderer invents no transitions.
Retain the existing paper, ink, caption colors and original Microsoft artwork.
Use existing design tokens and widgets, without reaching into widget internals.
A new widget or widget API requires separate approval.

## Meanwhile placement

Insert each strip immediately after the recorded launch event in its parent's
chronology, before later parent messages. Opening it expands the child in that
position; the existing link still opens its ordinary conversation route.
Completion and status updates must not move the strip to where the child finished.

An origin outside the loaded history cannot be placed honestly among the visible
messages. Keep such a strip with the older-history boundary, explicitly unlocated,
until loading reveals its launch. Never infer a launch from a completion notice.
Moving a newly located strip preserves its identity, expansion, child lifetime and
reading position. Reuse canonical activity membership and shared history demand;
no independent polling or speculative full-history download.

## Preservation and recovery

Keep the raw transcript unchanged. Presentation retains source identities and
source-offset anchors even where Markdown syntax has no visible glyph. Preserve
selection and reading location through resize, streaming, font fallback, paging,
reload and Standard/Comic switches. Incomplete streamed Markdown remains readable
and settles without duplicate or lost content. Reuse the shared safe Markdown
rendering and link behavior. Preserve existing tool images and author attribution.

Retain the single virtual reading flow, read-only inline children, source-qualified
remote identities and shared connection/recovery owners. This work does not change
Appearance controls, fonts, authorization, drafts or session execution. No hub
restart or deployment is included.

## Proof

Add failing regressions for each changed behavior, then verify the real renderer:

1. Read prose containing Markdown, code and a table on desktop and a narrow phone.
   Check DOM semantics, source order, absence of character art in inserts, bounded
   panel geometry, readable overflow and preservation of code whitespace.
2. Stream incomplete blocks to completion, then resize and switch presentations.
   Check the source anchor and draft survive, with no duplicated or missing text.
3. Launch a child between two parent messages, expand it, then complete it. Check
   its strip stays at launch. Load multiple history pages, reload and reconnect;
   check late origin discovery relocates the same expanded child without losing
   the reading position. Cover a remote child's qualified identity too.
4. Run affected unit tests, frontend typecheck/lint and real-browser geometry and
   recovery checks. Update `docs/product/comic-chat.md` to the verified behavior.

Deliver a reviewed PR through the repository's normal merge queue. Keep the change
confined to these reading refinements; reopen scope with Jesse if a shared
protocol or ownership change proves necessary.
