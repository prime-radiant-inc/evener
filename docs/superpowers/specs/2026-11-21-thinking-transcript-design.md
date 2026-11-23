# Thinking transcript rows

**Date:** 2026-11-21
**Status:** Approved

## Intent and why

Thinking is intent-level content. Chat should contain the conversation without live or historical reasoning rows. At Intent and above, a reader should be able to unfold thinking reliably, and compact thinking rows should visually belong beside the transcript's intent rows.

Jesse reported two problems in the web transcript: thinking and thought rows feel visually unbalanced, and some thinking rows cannot be clicked to disclose their contents. He also asked for an explicit review of padding and type treatments.

## Current behavior and root cause

The shared projector sends a live reasoning item to a compact `thinking` entry even at Chat. The web renderer's Chat-specific `ContentFreeThinkBlock` deliberately renders a Loader with no `Disclosure`, so that view cannot open. Intent, Tools, and Activity render compact disclosures; Full renders the richer reasoning presentation.

The compact live label already uses `Loader`'s `--font-size-ui` treatment and rail seating. The compact settled label inherits the larger body font and has no leading thought icon. The block and disclosure summary also contribute separate vertical padding, so the collapsed row needs measurement rather than another arbitrary inset.

## Goals

- Hide live, settled, and redacted reasoning rows at Chat on web and native.
- Keep genuine turn-failure presentation independent of reasoning visibility.
- Keep live and settled thinking disclosable at Intent and above.
- Align compact thinking summaries with intent-row UI typography, rail seating, chevron geometry, and deliberate vertical padding.
- Preserve the quieter italic Markdown body when a compact row is expanded.
- Leave Full's richer live and settled reasoning presentation unchanged.
- Update the evergreen product and design documentation with the behavior.

## Non-goals and anti-goals

- Do not fold thinking into tool-action groups.
- Do not add a widget, widget prop, detail level, or persisted preference.
- Do not change provider reasoning capture, replay, or history retention.
- Do not create a new visual vocabulary or add decorative motion.
- Do not tighten padding at the expense of the existing coarse-pointer tap target.

## Approaches

### Selected: remove Chat reasoning and align compact rows with existing intent grammar

Apply Chat visibility in the shared projector and native's Chat-specific filter. Remove the web's now-unreachable plain Chat placeholder. Reuse the existing Loader, Disclosure, ToolIcon, rail, and type/spacing tokens for compact rows.

This directly implements the Intent boundary and removes the known non-opening path. It also keeps web and native behavior aligned without introducing new preference state.

### Make Chat's placeholder disclosable

This would fix the click symptom but contradict the approved rule that thinking begins at Intent. Rejected.

### Use Full's reasoning presentation at every level

This would make live and settled rows more similar to each other, but gives compact levels too much visual weight and does not align them with intent rows. Rejected.

### Fold thinking into the action-group disclosure

This would change grouping and hide current activity behind another click. It is larger than the reported problem requires. Rejected.

## Design details

### Visibility

`decisionFor` treats Chat as hiding all reasoning when the Reasoning content flag is off, before live or critical reasoning classification. Terminal-turn fallback must not resurrect a hidden reasoning item. Named Custom configurations retain their existing content-driven behavior.

Native Chat intentionally projects through Intent so subagent rows survive. Its `justTheConversation` filter therefore drops every reasoning-family activity and thought-specific failure row, not only settled thoughts. Other failure rows remain.

### Compact presentation

Live compact rows keep the existing pulsing rail Loader and its disclosure. Settled compact rows gain the thought glyph in the same rail column. Both labels use the sans UI size and compact title/UI line height already used by neighboring intent chrome. The summary remains quiet chrome; the reasoning body remains italic, quieter Markdown at reading size.

Audit the combined block, summary, body, and expanded-content spacing at desktop and phone widths. Use existing spacing tokens and keep one intentional summary-line inset rather than stacking accidental padding. Keep the shared Disclosure widget API unchanged.

### Verification

Red-first tests cover Chat's live, settled, failed, and terminal-fallback reasoning cases, plus native's Intent-projected Chat filter. Existing web tests already prove that compact live and settled summaries open and close; retain those tests and add structural/type/spacing assertions for the settled row.

Run focused shared, web, and native tests, then typecheck and Biome. Inspect actual browser renderings in both themes at desktop and phone widths, collapsed and expanded. Measure computed label type and row padding and check the chevron remains visible and clickable. Run the browser layout guards that cover the touched rail geometry.

## Size

A small change, expected to remain well below the default couple-hundred-production-line budget. Most edits are tests and evergreen documentation.
