# Issue #4245: preserve literal notification entities

## Approved scope and contract

Jesse's final 2026-10-23 ruling accepts historical replay breakage and authorizes implementation after spec PAR. No migration, legacy fallback, heuristic classification, or stored-frame rewrite is permitted.

For newly produced text bodies, normalize invalid UTF-8 as today, escape `&` to `&amp;` first, then `<` to `&lt;`. Decode exactly `&lt;` and `&amp;` in one pass, matching `&lt;` before `&amp;`. Thus wire `&amp;lt;` decodes to the literal text `&lt;`, never to `<`. Quotes, `>`, apostrophes, and the literal strings `&quot;`, `&gt;`, `&#39;` are not body entities.

Keep the full attribute encoder and decoder, including quote and greater-than decoding. Split attribute decoding from the exported body decoder rather than narrowing attribute consumers. Preserve current trimming, frame parsing, title selection, wrapper protection and UTF-8 normalization.

## Producer and consumer inventory

- `agent/job_notify.go`: `escapeNotificationBody` serves job excerpts, watch-send bodies, no-job watch reasons, and `withNotificationNote` notes. Change this central encoder.
- `agent/job_watch.go`: `watchNotificationFromWatch` pre-escapes ampersands in notes today. Remove that step so the central encoder runs exactly once.
- `agent/delegate_delivery.go`: update messages use `escapeNotificationBody`. Update its codec comment. Terminal reports use `json.Marshal` and remain JSON, without notification entity decoding.
- `agent/delegate_runtime.go`: the quiet watchdog uses `html.EscapeString` on a fixed duration/timestamp sentence. Its character domain contains no quotes, greater-than signs or apostrophes, so it needs no producer change.
- `appwire-client/typescript/steeringNotifications.ts`: body decoding covers delegate updates, watchdog prose, legacy delegate excerpt/fallback, watch prose, job prose/excerpts and historical observer callback excerpts. Narrow `decodeNotificationEntities` for those paths. Attribute decoding covers delegate names/descriptions, failure reasons, watch titles/reasons and job intent/description, and must retain the full decoder.
- `appwire-client/typescript/index.ts` exports the existing decoder and encoder. Preserve those exports; `escapeNotificationEntities` remains the full attribute-style encoder.
- Browser `SteeringItem.tsx` and `NotificationCard.tsx`, and native `mobile-native/src/projectedRows.ts`, consume the shared parser's decoded fields without a second body decode. No UI-specific change is required.

## Accepted historical breakage

Pre-`6adc500868` frames are expected to render `&quot;` and `&gt;` literally, and their entity-escaped JSON-envelope extraction fails under the narrow body decoder. This is the accepted trade, not a compatibility promise.

Before commit `6adc500868a00e53ccd3724958ee080052d28175`, whole bodies used the full attribute-style encoder. Its sender text `>` and the later encoder's sender text `&gt;` both produce identical wire bytes `&gt;`. Transcript format 2 entries carry no notification-codec marker. A creation-time optional build header cannot classify entries exactly across resumed/development sessions. No migration or legacy fallback can resolve that collision without guessing. Do not rewrite any persisted transcript, event, job record, cache or overlay.

## Tests and corpus

Use red-first tests for producer bytes and the real Go producer → projected wire fixture → shared TypeScript parser journey. Cover `&`, `<`, `&lt;`, `&amp;`, `&amp;lt;`, quotes, `>` and apostrophe in both job excerpts and delegate updates. Include watch notes to catch double escaping. Assert the narrow decoder leaves `&quot;`, `&gt;` and `&#39;` literal and decodes each input match once. Assert full-escaped attributes still render correctly. Pin the accepted old-frame JSON extraction failure explicitly.

Extend `agent/steering_notification_wire_fixture_test.go` using real producers and `apptranscript.ProjectTurn`. Regenerate and commit `agent/testdata/notificationwire/steering.json` with `make fuzz-goldens`. Keep the typed reader `appwire-client/typescript/testing/notificationWireFixtures.ts` and shared parser corpus assertions green. Expected sender text must be independently asserted rather than inferred from the codec under test.

## Non-goals and recovery

No migration, stored-frame rewrite, codec version field, backward-compatibility shim, unrelated renderer change, notification framing redesign, or change to terminal packet JSON. Reverting the code change is the only recovery action; no persisted data is modified by this work. Scope is the producer escape/caller adjustment, shared decoder, relevant producer/notificationwire tests and corpus, and this spec. The final review amendment also authorizes updating only the matching body-contract paragraph in `docs/job-control.md` so the owning guide stays evergreen.

## Verification and delivery

Run targeted Go tests for producer escaping, affected delegate/watch behavior and the notificationwire fixture; shared TypeScript steering notification tests (including corpus and attributes); relevant appwire API package checks; `make fuzz-goldens`; and `make build`. Read command implementations and record actual outcomes. Do not run the full repository suite. Reuse a lockfile-identical shared dependency install, never `npm ci` through a symlink. Run Biome only from `cmd/evener-hub/frontend`.

Before delivery: two independent competing spec PAR reviewers, implementation red/green, two implementation PAR reviewers, simplify, commit and roborev branch review, push PR, shepherd current-head checks and reviews, then admin squash merge only with required checks green and findings settled. Report exact commands/results, issue/PR/merge SHA, accepted breakage, all touched producers/consumers and anything unverified.
