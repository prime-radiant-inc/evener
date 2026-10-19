# Web settings organization

## Intent

Make settings predictable by organizing around the person's task rather than historical implementation buckets. Jesse chose the full reorganization after reviewing a mobile-first interactive prototype. Phones are a first-class surface, not a compressed desktop sidebar.

The work includes Preferences, a unified Plugins destination, and consolidation of General into System pages. It preserves the existing design language and the controls' real behavior. The prototype illustrates arrangement with example data; its simplified controls and placeholder Agent/System pages are not the production specification.

## Navigation

Retain shallow desktop navigation and the existing phone list/detail model.

| Group | Label | Existing section ID |
| --- | --- | --- |
| Preferences | Appearance | `theme` |
| Preferences | Conversation display | `transcript` |
| Preferences | Input & shortcuts | `keybindings` |
| Preferences | Notifications | `notifications` |
| Agent setup | Providers & credentials | `credentials` |
| Agent setup | Agents | `agents` |
| Agent setup | AGENTS.md | `agents-md` |
| Agent setup | Evener launch | `launch-evener` |
| Agent setup | In-repo config | `inrepo` |
| Extensions | Plugins | `plugins-manager` |
| Extensions | Skill directories | `skills` |
| Extensions | MCP servers | `mcp` |
| System | Hosts | `hosts` |
| System | Devices | `mobile` |
| System | Hub | `hub` |
| System | Storage | `storage` |
| System | About | `about` |

Retaining still-used IDs is a builder choice to avoid unnecessary URL churn. Agent setup pages and project-specific settings retain their existing behavior. The contextual `project` destination remains valid, absent from global navigation, and preserves `cwd` and host context.

### Retired links and entry behavior

Jesse explicitly chose to retire `general`, `display`, and the standalone directory section `plugins`. No forwarding aliases to Hub, Input or Sources are added. Old bookmarks and remembered retired section IDs fall back to Appearance. Unknown section IDs receive the same useful fallback. Do not let a remembered valid page override an explicit retired URL.

On desktop, bare `/settings` reopens a valid remembered page, otherwise Appearance. On phones, bare `/settings` remains the section list. Explicit retired/unknown URLs open Appearance, even on phones. Canonicalize explicit invalid URLs to `/settings/theme` with history replacement, preserving host context; do not add a Back-history trap. Do not remember the contextual project page.

## Preferences

- **Appearance:** reuse the existing color theme, text size, phone density and transcript width controls and storage keys. Retain their behavior and defaults.
- **Conversation display:** reuse the complete existing desktop/mobile hub-default cards, draft handling and browser-override explanation. Do not replace them with the prototype's simplified checkboxes.
- **Input & shortcuts:** bring the existing Enter-sends control and full existing keyboard configuration together. Preserve shortcut editing, conflict checks, resets, single-character preference and the existing warning about Shift-Enter steering. Enter-sends and the single-character toggle are browser-local; custom keybinding overrides sync to the connected hub. Label these subgroups separately, never the whole page as browser-local. This corrects the simplified prototype's scope caption.
- **Notifications:** retain all current controls, permission handling and defaults.

Use consistent visible scope descriptions: browser-local preferences apply here; conversation defaults belong to the connected hub and paired devices, with local overrides distinct. This is presentation, not a new sync model.

## Plugins

One Plugins navigation entry owns **Installed**, **Browse**, and **Sources**, using the existing segmented workspace and detail sheets.

- Installed retains plugin status, enablement, upgrade, auto-upgrade and removal behavior.
- Browse retains marketplace discovery, filtering and installation.
- Sources contains separate titled areas for Marketplaces and Local plugin directories. Reuse the current marketplace management and the existing directory editor. Keep directory validation and save behavior.
- A single existing host picker scopes the whole plugin destination. Directory edits and plugin operations must use that exact host, including after switching away and back or reconnecting.
- Directory controls must remain usable when marketplace/plugin inventory loading fails. Moving the previously independent directory editor into this workspace must not make its availability depend on unrelated reads. Keep segment navigation reachable and scope errors to affected content.
- State explicitly that installation makes a plugin available and default enablement affects new sessions. A launch's custom selection and existing sessions' frozen selections remain unchanged. No launcher redesign is included.

Keep current segment state ownership; URL-addressable subsegments and field-level deep links are not new requirements. The manager still initially opens Installed. Preserve current confirmation and marketplace rename/removal protections.

## System and General inventory

Remove the duplicate General page only after every value has a surviving owner:

| Current General field | Owner after reorganization |
| --- | --- |
| Hub address | Hub, existing listen-address row |
| Bearer token mask and age | Hub, still never expose the token |
| Run directory | Hub, existing row; existing Storage reference may remain |
| State directory | Storage, existing row |
| Past index path and size | Storage, existing row |
| Spawn timeout | Hub, existing row |
| Past results per page | Storage, alongside the optional past index |
| Hub config location | Storage, existing row; Hub help can link/reference it |
| Hub version and commit | About, existing identity |

Keep conditional absence of past-index information and all existing loading/error/retry behaviors. Hub updates and resident controls, Storage cache controls and About credits remain intact. Devices is the existing Mobile app setup page under a clearer label; all phone/QR/acoustic setup remains intact. Do not add new backend fields or expose credentials.

## Search

Extend the existing navigation filter to match control/topic vocabulary, not just page labels, and render a no-results state. Minimum discoverability cases: font/text size and density → Appearance; Enter/send/composer and shortcuts → Input & shortcuts; sound/favicon → Notifications; transcript/cost/tokens → Conversation display; marketplace/plugin directory → Plugins; skill directory → Skill directories; cache → Storage; pairing/mobile → Devices.

Builder choice: a small explicit per-section keyword inventory alongside the section metadata, case-insensitive and trimmed. Results remain destination rows; this change does not introduce a global index, field highlighting, or control-level jump routing. Keep navigation ordering, keyboard accessibility, host-carry behavior, and retained filter/scroll through phone Back.

## Constraints and non-goals

- Preserve all preference keys, stored values, defaults, backend methods, host ownership and project overrides.
- Use existing widgets from their barrel and existing design tokens. No new widget or widget prop without Jesse's approval.
- Preserve useful content and recovery when reorganizing. Do not trade independent availability for a shared all-or-nothing loader.
- No new dependencies, backend schema, migration, compatibility alias, or automatic mutation of real user settings.
- No new visual identity. Use the editorial instrument system in both themes.
- No broad notification, localStorage, plugin-launch or unrelated recovery redesign. Fix behavioral regressions caused by the consolidation at their source.
- Mockup files and its server remain separate from shipped source. Do not commit server state, credentials or logs.

## Verification

Use red-before-green behavior tests. Assert routes, selected IDs, store values, outgoing host-scoped requests, rendered field values supplied by fixtures, disabled/error states and recovery; never assert production wording as an oracle. Test retired and unknown routes, preserved project routes, remembered destinations, phone Back/Forward, Enter-sends persistence, hub-default/local-override separation, remote plugin-directory saves, and independent directory availability through inventory failure/recovery.

Run targeted Vitest tests while building, plus frontend typecheck and the canonical frontend gate. Run browser guards on this Chrome-capable host and inspect real phone and desktop settings in both themes. A source review or the standalone prototype does not establish final production geometry or live behavior.

## Remaining decisions

The organization and retired-link policy are settled. The implementation method and final plan review remain with Jesse. Builder choices above can be adjusted during plan review without changing scope or storage authority.
