# Interruption notices

The current web `transcript/messages/SteeringItem.tsx` uses the typed
`steeringKind` and a collapsible divider labelled Interrupted. Native projection
discarded that field, leaving a generic Notice above the complete reminder.
Preserving the field allows the native renderer to identify the same event
without interpreting its prose.

Only non-warning notices with origin steering and kind interrupted get this
disclosure. Expansion shows the original selectable text, including framing;
the collapsed label does not replace or rewrite it. Warning, notification,
unknown and system-origin notices do not enter this route.

Verification, 6 September 2026: new projection and classification tests failed
before implementation. All 227 native tests and 2,242 shared-client tests pass,
along with native TypeScript and touched-file formatting. Both Release builds
succeeded. On the actual isolated SecondHub session, both native apps showed
collapsed Interrupted rows and expanded the first to its complete original
reminder. iOS was also collapsed again. The existing `/pro` drafts stayed
unchanged; no message or provider request was sent. Both saved screenshots
were visually inspected.

This is one implementation step toward presentation study 02. Header space,
row spacing, other notice families, notification cards, persistent disclosure
state, screen-reader traversal and large-text acceptance remain open. The
screenshots do not establish final visual quality.

## Disclosure continuity

Native transcript rows reuse the current web's UI-independent Zustand
disclosure store. Scope keys encode hub and session together; item keys encode
kind and ID, including recursively rendered details. Explicit choices survive
row and conversation-screen remounts during the app process. This covers
activity, grouped details and interruption disclosures. It does not persist
to disk or implement transcript display-preference baselines yet.

The Android pre-change check reproduced loss of an expanded interruption after
leaving and reopening the session. The final Release apps on both platforms
retain that expanded interruption after the same navigation, with `/pro`
unchanged. Native TypeScript, touched-file Biome and 227 tests pass, as do the
27 existing web disclosure tests, including remount and scope behavior.
Both Release builds succeeded. Cross-hub native manual acceptance, long-list
virtualization and process-restart persistence remain open; scope composition
was checked in source, not established by a cross-hub manual test here.

![iOS interruptions collapsed](assets/interruption-notices/ios-collapsed.png)

![Android interruptions collapsed](assets/interruption-notices/android-collapsed.png)
