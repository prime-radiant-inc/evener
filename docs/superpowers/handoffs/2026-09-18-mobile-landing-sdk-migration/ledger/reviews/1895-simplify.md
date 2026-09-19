# /simplify review — PR #1895 (mobile canonical warning composition + short live-row ids, head e56da8680)

Scope: reuse, simplification, efficiency, altitude only. Correctness is RoboRev's.
Reviewed `git diff -M pr-1894-review..pr-1895-review`: `mobile/src/conversation/project.{ts,test.ts}`,
`mobile/src/state/conversation.{ts,test.ts}`. Line numbers are at this PR's head.

## Must fix before merge

None. (`title` at `conversation.ts:2852` is still load-bearing for `failureItem.title`, so dropping it from the
id leaves nothing dead.)

## Follow-up

### 1. `mobile/src/conversation/project.ts:550` and `mobile/src/state/conversation.ts:2856` — two encodings of "join the non-blank warning parts with ` — `"

```ts
const joined = parts.filter(hasWarningText).join(" — ");                        // project.ts:550
const detail = [folded.text, folded.hint].filter(hasWarningText).join(" — ");   // conversation.ts:2856
```

This PR exists because the projector and the live row disagreed about composition; it fixes the disagreement by
writing the composition out a second time. Both files already import `hasWarningText` (and `conversation.ts`
imports `foldWarningParams`) from the package, so the shared home exists.

- Cost: the separator, the blank-filter and the "compose, never pick with `||`" rule are duplicated across the
  two surfaces whose divergence is the bug being fixed. A change to the separator, or a fourth part (`source`),
  lands in one and not the other, and the failure mode is a silently different-looking row on one surface — not
  a test failure, since each surface asserts only its own string.
- Simpler form: export a three-line `joinWarningParts(...parts: unknown[]): string` from the package next to
  `foldWarningParams` (`parts.filter(hasWarningText).join(" — ")`), and call it at both sites. Note the two
  surfaces are *not* the same rule end to end — the failure row has its own `title` slot (:2852) while the
  projector's `detail.output` does not, which is why the projector falls back to `title` and the live row does
  not — so share the join, not the part selection. ~6 lines, no behaviour change.
- Severity: follow-up (the existing encoding at :2856 is inline, not a named helper).

### 2. `mobile/src/conversation/project.ts:547-551` — the `string | undefined` return and the two-branch array buy nothing at the one call site

```ts
const parts = hasWarningText(item.text) ? [item.text, item.warning.hint] : [item.warning.title, item.warning.hint];
const joined = parts.filter(hasWarningText).join(" — ");
return joined === "" ? undefined : joined;
```

The only caller is `:533`, `warningFallbackText(item) || item.text || item.output`, where `""` and `undefined`
behave identically. And the two array literals differ in exactly one element.

- Cost: a reader has to check whether some caller distinguishes empty-string from absent (none does), and the
  duplicated `item.warning.hint` in both branches hides that the choice is only about the first part.
- Simpler form:
  ```ts
  const message = hasWarningText(item.text) ? item.text : item.warning.title;
  return [message, item.warning.hint].filter(hasWarningText).join(" — ");
  ```
  with the early return at :548 becoming `return "";` and the signature `string`. Three lines to two, one array
  literal instead of two, identical output through the `||` chain at :533.
- Severity: follow-up, low.

### 3. `mobile/src/state/conversation.test.ts:7167` — verbatim copy of the test at `:7143`, differing only in `params`

Both tests build the store the same way, apply one `warning` notification, find the failure row, and assert the
same two things (`id.length < 30`, `id.startsWith("warning:")`). Only the `params` spread differs
(`extra: "x".repeat(500)` vs `title: "T".repeat(2000)`).

- Cost: ~21 duplicated lines, and the pair will drift — a third id property worth asserting gets added to one of
  them.
- Simpler form: `it.each([["a message-less frame", { extra: "x".repeat(500) }], ["an oversized title", { title:
  "T".repeat(2000) }]])("keeps the live row id short for %s", async (_name, extra) => { … })`, keeping both
  comment blocks above it as the two reasons. ~20 lines shorter.
- Severity: follow-up. Test-only.

### 4. `mobile/src/conversation/project.test.ts:1529` — verbatim copy of the test at `:1501`, differing only in `params` and the two expected substrings

Same thread fixture, same `hydrateThread`/`applyNotification`/`projectConversation` sequence, same
`item_warning_live_t1_0` lookup, same two `toContain` assertions.

- Cost: ~28 duplicated lines; the message-less case and the message+hint case are one table with two rows.
- Simpler form: `it.each([[{ title: "Sandbox blocked", hint: "retry later" }, ["Sandbox blocked", "retry
  later"]], [{ message: "disk is nearly full", hint: "retry later" }, ["disk is nearly full", "retry later"]]])`
  over the shared body — the two cases then visibly assert the same contract ("every non-blank part appears"),
  which is the point the PR is making.
- Severity: follow-up. Test-only.

## Verdict

**Follow-up only — 4 findings, 0 must-fix.** Both production changes are net simplifications: dropping the title
from the live row id deletes a concern (`warning:${serial}` cannot bloat, so the ownership keys it feeds cannot
either) rather than adding a bound to manage, and reordering `warningFallbackText(item) || item.text ||
item.output` makes one function own the warning composition instead of splitting it across a `||` chain and a
helper. The findings are all about the copy that came with it: the `" — "` composition is now written out on
both mobile surfaces whose disagreement this PR is fixing (#1, the one worth doing — a three-line shared join
closes the class instead of the instance), the projector's helper keeps an `undefined`/`""` distinction and a
duplicated array element that no caller needs (#2), and both new tests are verbatim copies of their immediate
predecessors where a two-row `it.each` would show the shared contract (#3, #4). Nothing duplicates an existing
named helper and nothing is left dead, so none of it should hold the merge.
