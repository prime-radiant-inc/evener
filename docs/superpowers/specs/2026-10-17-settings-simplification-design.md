# Settings simplification design

## Outcome

Make ordinary choices easy to find and keep technical information available on demand. Use the existing settings layout, widgets, and design tokens on desktop and mobile. Preserve all persistence authorities and recovery behavior.

## Project configuration

Move repository-file review and explicit trust from the global In-repo config screen into contextual Project settings, alongside local project overrides. Show the selected host and directory once. Distinguish shared `.evener/launch.toml` from local `.evener/launch.local.toml`. Keep the local form usable if repository inspection fails. Preserve drafts across reconnect and repository trust refresh.

Discover only in the selected working directory, as today. Trust remains an explicit action against the reviewed hash. Never trust on discovery or reuse approval across host/directory changes. Remove In-repo config from navigation and dispatch; the retired route follows the existing unknown-route fallback. Existing project navigation remains the entry point.

## Conversation display

Show one layout editor at a time, initially the current device class, with a selector for Desktop and Mobile. Switching layouts preserves each layout's draft and save state. Keep the preview; place revision and shown/hidden inventories in Technical details. An active browser override exposes Use the shared default, clearing only that layout's local override through the existing store. Do not change shared defaults as part of clearing an override.

## Diagnostics

Put paths, revision numbers, token age, and runtime facts behind Technical details. Hub retains updates and residents as primary content. Storage retains cache management as primary content. Run directory appears only in Storage. Keep diagnostic fields available and errors/recovery actions visible.

## Notifications and input

Show Loud for only while sound or desktop notifications are enabled. Hiding the control preserves its value. Keep permission handling unchanged. Rename OS notification to Desktop notifications and Favicon dot to Browser tab indicator.

Keep Enter-to-send and the character-key accessibility setting immediately visible. Put the full shortcut editor inside Customize shortcuts, with case-insensitive search over action labels and IDs. Closing the editor or filtering rows must not erase persisted bindings or pending edit state. Keep connection/save warnings and retries visible.

Rename Evener launch to Session defaults in settings labels and relevant help/search metadata. Preserve the existing launch-evener route.

## Boundaries and acceptance

No backend changes, parent-directory discovery, new persistence, new widget APIs, or launch-flow redesign. No deployment/restart of the running hub. Tests exercise real stores and components with the existing transport fixtures; copy is not an assertion oracle. Verify host/directory switches, reconnect, independent local/shared state, notification channel transitions, shortcut filtering and keyboard access. Update the web settings guide and affected product ownership references. Finish with simplification, local whole-branch RoboRev convergence, and PR shepherding.
