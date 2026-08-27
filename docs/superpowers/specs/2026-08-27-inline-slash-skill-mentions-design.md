# Inline Slash Skill Mentions — Design

Date: 2026-08-27
Status: Ready for review

## Goal

Expose every skill available to the focused Evener session in the web composer's inline slash menu. Match skill names fuzzily while the user types a slash token, including inside ordinary prose such as `Use /simplify on this`.

Support both forms:

- **Inline mention:** selecting `/simplify` only completes the text. The submitted sentence remains unchanged, and the model loads the named skill through `use_skill`.
- **Standalone invocation:** a message consisting of `/simplify` or `/simplify <context>` is handled by the existing server-side slash-input path and expands the skill body directly before the model turn.

Unknown slash tokens remain ordinary user text.

## Existing data flow

The session already discovers bundled, project, configured `skills_dirs`, and plugin skills into `Session.skills`. Live thread snapshots already carry the metadata through `EvenerThread.Diagnostics.Skills`, but the frontend reducer drops that field. The web composer currently merges only built-in commands and the global `CommandDescriptor` catalog. The existing `use_skill` executor and prompt catalog remain the authoritative skill-loading path.

Plugin skills are keyed internally as `plugin:skill`. `DetailedStatus` currently emits `SkillMeta.Name` while iterating the map, which loses that namespace; the wire metadata must use the map key so completion and standalone resolution agree.

## Design

### Session metadata

Reuse `EvenerThread.Diagnostics.Skills`; do not add a global `evener/command/list` entry or a second global catalog. Add an optional `skills` field to the frontend `ThreadModel` and hydrate it from `thread.evener.diagnostics?.skills`.

For cold local thread reads, populate the same metadata-only diagnostics field from the persisted session's discovery inputs (working directory, `SkillsDirs`, and `PluginDirs`) only on the single-thread read path, not navigation/list sweeps. This keeps completion session-scoped without scanning every historical session.

The metadata contains only catalog names and descriptions. It never exposes skill bodies or filesystem paths to the browser.

### Inline completion

Extend `SlashMenuItem.kind` with `skill`. Merge the focused thread's skills into the existing composer menu with:

- key `skill:<catalog-name>`;
- invocation `/<catalog-name>`;
- label equal to the catalog name;
- description as the hint;
- exact namespaced names for plugin skills.

Replace the completion filter's prefix-only test with deterministic fuzzy subsequence scoring. Rank exact/contiguous/earlier matches first, retain deterministic merge order for ties, and exclude non-subsequences. The existing slash-token parser and splice logic remain unchanged, so a completion in `Use /sim` becomes `Use /simplify ` and later text can be appended normally.

Command and skill rows may coexist when names collide. The selected skill row still inserts its catalog name; standalone server resolution gives an actual command precedence over a skill, while a qualified skill name remains unambiguous.

### Model behavior for inline mentions

Extend the skills prompt section to state that a matching `/name` token in user prose is a skill request. Profiles with `use_skill` must call it with the exact catalog name before acting; profiles without that tool must read the corresponding `SKILL.md` path as they already do. This makes the inline example model-mediated without rewriting the user's sentence.

### Standalone invocation

Extend `Session.expandSlashCommand` after command resolution:

1. Resolve a skill by exact catalog name, then the existing unqualified plugin-suffix fallback.
2. Load the skill body.
3. Emit the existing skill-activation event.
4. Return the body as the model input. If trailing context is supplied, append it as user context without performing skill-body directive expansion or argument substitution.

Existing command precedence, command expansion, warning behavior, and unknown-input fallthrough remain unchanged. A skill load failure warns and falls back to the literal input, matching command expansion failure behavior.

## Testing

Add focused tests for:

- `DetailedStatus` preserving a plugin skill's `plugin:skill` catalog key;
- cold thread-read skill metadata, without adding discovery to list sweeps;
- reducer hydration of `ThreadModel.skills`;
- fuzzy completion, including `/smp` matching `simplify`, ranking, empty queries, and deterministic ties;
- inline completion preserving `Use /simplify on this` as text;
- prompt guidance for slash mentions;
- standalone `/skill` and `/skill context` expansion, activation event, qualified plugin names, command-over-skill collision, unknown skill fallthrough, and load failures.

Run the focused Go tests, the touched frontend tests, `npx biome check --write` on touched `src/` files, then the repository's frontend gate.
