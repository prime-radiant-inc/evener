# Inline Slash Skill Mentions — Design

Date: 2026-08-27
Status: Ready for implementation

## Goal

Expose every slash-addressable skill available to the focused Evener session in the web composer's inline slash menu. Match skill names fuzzily while the user types a slash token, including inside ordinary prose such as `Use /simplify on this`.

Support both forms:

- **Inline mention:** selecting `/simplify` only completes the text. The submitted sentence remains unchanged, and the model loads the named skill through `use_skill` (or reads its `SKILL.md` when that tool is unavailable).
- **Standalone invocation:** a message consisting of `/simplify` or `/simplify <context>` is handled by the existing server-side slash-input path and expands the skill body directly before the model turn.

Unknown slash tokens remain ordinary user text.

## Existing data flow

The session discovers bundled, project, configured `skills_dirs`, and plugin skills into `Session.skills`. Live thread snapshots already carry the metadata through `EvenerThread.Diagnostics.Skills`, but the frontend reducer drops that field. The web composer currently merges only built-in commands and the global `CommandDescriptor` catalog. The existing `use_skill` executor and prompt catalog remain the authoritative model-mediated skill-loading path.

Plugin skills are keyed internally as `plugin:skill`. `DetailedStatus` currently emits `SkillMeta.Name` while iterating the map, which loses that namespace. The projection must copy each `SkillMeta`, set only the projected `Name` to the map key, and leave `s.skills` unchanged. AppWire emits only metadata: `{name: catalogKey, description}`.

## Design

### Session metadata

Reuse `EvenerThread.Diagnostics.Skills`; do not add skills to global `evener/command/list` and do not add a second global catalog. Add an optional `skills` field to the frontend `ThreadModel` and hydrate it from `thread.evener.diagnostics?.skills`.

A live thread read uses the daemon's diagnostics, which are the authoritative initialized-session catalog. On local `thread/read` only, when live diagnostics are absent, reconstruct the same catalog as session startup: embedded skills, project/`SkillsDirs`, then plugin skills under `plugin:skill`; sort by canonical key and emit only name/description. Do not add this work to thread-list, navigation, transcript-list, or turn-page sweeps. Prefer one shared metadata-only discovery helper. Reconstructed metadata is a fallback for a cold session and may be stale after filesystem edits; a resumed session rediscovering skills is authoritative.

The metadata contains no skill bodies or filesystem paths in the browser.

### Slash-name grammar

A slash-addressable catalog name is case-sensitive and is at most 128 UTF-8 bytes. It consists of one bare name, or one plugin-qualified name with exactly one colon:

```text
[A-Za-z0-9_][A-Za-z0-9_-]*(?::[A-Za-z0-9_][A-Za-z0-9_-]*)?
```

Skill discovery rejects names outside this grammar, as it already skips malformed skill files. This keeps every exposed skill representable by the composer token parser and the standalone invocation parser. Autocomplete comparison is case-insensitive, but the inserted and resolved invocation preserves the canonical catalog spelling. Skill resolution remains exact first, then a unique unqualified plugin suffix.

### Inline completion

Extend `SlashMenuItem.kind` with `skill`. Merge the focused thread's skills into the existing composer menu with:

- key `skill:<catalog-name>`;
- invocation `/<catalog-name>`;
- label equal to the canonical catalog name;
- description as the hint;
- exact namespaced names for plugin skills.

The parser continues to recognize a slash token only after the start of the message or whitespace, and only while the caret trails that token. Replace the completion filter's prefix-only test with this exact ranking contract:

1. case-fold the label and query;
2. an empty query returns every row in merge order;
3. a non-empty query must be a subsequence of the label;
4. rank exact match, match beginning/contiguousness, matched span/earliest start, then original merge index;
5. do not alphabetically reorder final ties.

The parser remains unchanged. Splicing adds a trailing space only when the preserved suffix does not already begin with whitespace. Thus completing `Use /sim` produces `Use /simplify `, while completing the token in `Use /sim on this` produces `Use /simplify on this` with no doubled space.

Command and skill rows may coexist when names collide. A selected skill row inserts its canonical catalog name. A server-resolved plugin/evener-wide command wins over a skill for standalone server input; web/TUI built-ins remain client-side and do not enter the server resolver. Qualified skill names remain unambiguous.

### Model behavior for inline mentions

Extend the skills prompt section only when `use_skill` is actually callable in the final tool registry. State that a token matching a canonical skill name with the same start/whitespace boundaries as the composer is a skill request, including inline prose. The model must call `use_skill` with the exact catalog name before acting. If `use_skill` is unavailable, it must read the corresponding `SKILL.md` path as the existing fallback prompt requires.

The guidance must not activate paths, URLs, code spans, unknown names, or literal/negated mentions. The prompt must not advertise `use_skill` when provider or tool restrictions have removed it.

### Standalone invocation

Extend `Session.expandSlashCommand` after the existing server command resolution:

1. Resolve an exact server catalog key first. If that fails, resolve the unqualified plugin suffix only when exactly one candidate exists; multiple suffix matches remain unresolved and literal.
2. Load the skill body before emitting `EventSkillActivated`.
3. Emit the event with the canonical catalog key.
4. Return the body as model input. For trailing context, return `body + "\n\nUser context:\n" + strings.TrimSpace(context)`; perform no directive or argument expansion in either body or context.

An actual server-resolved plugin/evener-wide command wins before this skill path. Web/TUI built-ins are intercepted by those clients and remain unchanged. A skill load failure emits a warning, emits no activation event, and falls back to the literal input. Existing command expansion, warnings, and unknown-input fallthrough remain unchanged.

Inline text such as `Use /simplify on this` does not enter this server expansion path because it does not begin with a slash command; the model-mediated prompt behavior handles it.

## Testing

Add focused tests for:

- `DetailedStatus` preserving a plugin skill's `plugin:skill` catalog key without mutating session state;
- slash-name validation, case-preserving resolution, maximum length, and qualified-name parsing;
- cold local `thread/read` fallback using embedded, project/`SkillsDirs`, and plugin skills, without discovery on list/navigation/turn-page sweeps;
- reducer hydration of `ThreadModel.skills`;
- fuzzy completion, including `/smp` matching `simplify`, ranking, empty queries, canonical qualified labels, and deterministic ties;
- whitespace-safe inline completion preserving `Use /simplify on this`;
- prompt guidance only when `use_skill` is callable, with inline-boundary and negative-mention cases;
- standalone `/skill` and `/skill context` expansion, exact and unique-qualified resolution, command precedence, activation event ordering, no event on load failure, unknown/multiple-suffix fallthrough, and literal context handling.

Run the focused Go tests, the touched frontend tests, `npx biome check --write` on touched `src/` files, then the repository's frontend gate.
