# /simplify review — PR #1904 (D6 p6c: keybindings screen offline-recovery UI, head 3c6436d6f)

Scope: reuse, simplification, efficiency, altitude only. Correctness is RoboRev's.
Reviewed `git diff -M pr-1902-review..pr-1904-review` (this piece's own diff, stacked on #1902):
`mobile-native/src/{KeybindingPreferencesScreen.tsx,keybindingOfflineRecovery.ts,keybindingOfflineRecovery.test.ts,
nativePreferences.ts,keybindingRecovery.test.ts}`. Line numbers are at this PR's head.

The `nativePreferences.ts` bridge (`:42`, `:69`, `:143`) is the minimal correct thing: the field is declared
once on `PreferenceState<T>`, defaulted in `initialDomain`, and mapped in `keybindingsDomain` next to its
siblings — no new shape, no second projection. The `keybindingRecovery.test.ts:233-236` change from a single
`expect(...).toBe(true)` to a `toMatchObject` of both flags is the right way to strengthen that assertion without
adding a test.

## Must fix before merge

None.

## Follow-up

### 1. `KeybindingPreferencesScreen.tsx:228-230` copies the error literal already at `:132`

`"The change could not be completed. Check current shortcuts and review your changes."` is now written twice in
this file, verbatim: once in `run`'s catch (`:132`) and once in the new offline-discard catch (`:228`).

- Cost: the same user-facing sentence in two places, so the next wording change (or localization pass) silently
  ships two variants of one message.
- Simpler form: hoist a module-level `const CHANGE_FAILED_MESSAGE = "..."` and use it at both sites. Two lines.
- Severity: follow-up (take it in the next push if there is one — it is a mechanical hoist).

### 2. `keybindingOfflineRecovery.ts:45-51` — `offlineStorageErrorMessage` is a function wrapping a constant, and it nearly duplicates the store's own restore-failure message

The body is `flag ? "Could not read the saved shortcut draft on this phone. Check current shortcuts to retry." :
null`, and `keybindingOfflineRecovery.test.ts:53-60` asserts both branches by restating the literal.

- Cost: a cross-module function call, a doc block and a 2-case test for a ternary over a constant. Separately:
  the string is a near-copy of `keybindingsStore.ts:269`'s `DRAFT_RESTORE_FAILED_MESSAGE` ("Could not restore the
  saved shortcut draft. Check current shortcuts to retry."), which the live path already surfaces through
  `domain?.error` — so the user sees one of two 90%-identical sentences for the same failure depending on whether
  a model happens to exist.
- Simpler form: a module-level `const OFFLINE_DRAFT_READ_FAILED_MESSAGE` and
  `preferences.offlineStorageUnavailable ? OFFLINE_DRAFT_READ_FAILED_MESSAGE : null` inline in the
  `ErrorMessage` chain at `:181-187`; drop the function and its test. If the two sentences should be one, the
  cleaner fix is exporting the store's constant from the package root (it already exports
  `GENERIC_ERROR_MESSAGE`/`HUB_UNREACHABLE_MESSAGE` that way) and reusing it — one decision, not two strings.
- Severity: follow-up.

### 3. `keybindingOfflineRecovery.ts:35-41` — `unreadableDraftDiscardDisabled` re-declares the domain shape structurally and re-states the "hub write in flight" triple the screen already computes

The parameter type is an inline `{ loading?: boolean; saving?: boolean; writeUncertain?: boolean } | undefined`
rather than the existing `PreferenceState<ConfirmedKeybindings>`, and the body's
`!!domain?.loading || !!domain?.saving || !!domain?.writeUncertain` is three of the six clauses of `busy` in
`KeybindingPreferencesScreen.tsx:113-119`.

- Cost: an optional-everything structural type means a renamed or mistyped field reads as `undefined` instead of
  failing the typecheck, which is the one guarantee this extraction should buy; and "an in-flight or unresolved
  hub write" is now expressed at two sites, so a fourth flag joining that class gets added to one of them.
- Simpler form: type the parameter as `Pick<PreferenceState<ConfirmedKeybindings>, "loading" | "saving" |
  "writeUncertain"> | undefined` (the type is exported from `nativePreferences.ts`, no cycle: that module does
  not import the screen), and export one `hubWriteInFlight(domain)` from this same module that both `busy` and
  `unreadableDraftDiscardDisabled` use. The screen's `busy` becomes `!available || hubWriteInFlight(domain) ||
  !!domain?.storageUnavailable || !!domain?.confirmed?.loadError`, which also brings that six-clause expression
  under test for the first time.
- Severity: follow-up.

### 4. `keybindingOfflineRecovery.ts:17-23` — `offlineAwareDraftUnreadable` takes three positional booleans, two of which mean nearly the same thing

`offlineAwareDraftUnreadable(connected, domainDraftUnreadable, offlineDraftUnreadable)`: the call site
(`KeybindingPreferencesScreen.tsx:98-102`) is readable, but the tests are not —
`offlineAwareDraftUnreadable(false, false, true)`, `(false, true, false)`, `(true, undefined, true)` — and two
adjacent parameters of type `boolean | undefined`/`boolean` describing the same fact are transposable with no
type error.

- Cost: a tiebreak whose test suite reads as boolean triples; a transposition of the last two arguments would
  pass the typecheck and invert the behaviour.
- Simpler form: one object parameter (`{ connected, domain, offline }`) — the tests then read as the sentences
  their comments already spell out. Or, better, remove the need for the function entirely: see finding #5 of the
  p6b review — the provider publishes this classification twice (its own probe state plus a patch into the
  model's snapshot), and resolving it once at the provider deletes this function and its three tests along with
  the duplication that caused them.
- Severity: follow-up.

## Efficiency

Nothing. This piece adds no reads, no parses and no renders: three pure functions, one JSX block gated on a
boolean, and one field mapped in an existing projection.

## Verdict

**Follow-up only — 4 findings, 0 must-fix.** Nothing here duplicates an existing helper and nothing is dead; the
`nativePreferences.ts` bridge is exactly as small as it should be. The four are all local shape: a user-facing
error literal now written twice in one file (#1, a two-line hoist), a function whose whole body is a ternary over
a constant that is itself a near-copy of the store's own `DRAFT_RESTORE_FAILED_MESSAGE`, so the same failure reads
two different ways depending on whether a model exists (#2), a structurally-typed `{loading?, saving?,
writeUncertain?}` parameter that re-states three of the six clauses of the screen's existing `busy` and gives up
the typecheck that extraction should buy (#3), and a three-positional-boolean tiebreak whose tests read as
`(false, true, false)` (#4). The deeper item is not this PR's to fix: `offlineAwareDraftUnreadable` exists only
because p6b publishes the unreadable classification in two places, and resolving that once at the provider
(p6b finding #5) would delete this function and its three tests outright.
