# Web session Overview: adversarial spec review

## Verdict

The spec is ready for Jesse's approval before planning. Two independent read-only
reviews examined draft commit `4c37fc2607`, then reviewed the Activity retirement
delta after Jesse chose “Retire it without migration.” The revised spec
incorporates their scope, accessibility, geometry, restoration, and test
corrections. No product code changed. No tests, dependency installs, or provider
requests ran in this phase.

## Review lanes

- **Product, UX, accessibility, and scope:** delegate `overview-spec-ux`.
  Evidence: session `local:034YkPmqP4f0paPuPvZF37`.
- **Session binding, demand, retention, and testing:** delegate
  `overview-spec-state`. Evidence: session `local:034YkQIuzu2gM3IoP7VXkr`.

The UX reviewer requested revisions. The state reviewer considered the design
ready for planning with recorded risks. Those judgments cover different
boundaries: the shared read architecture is sound, while moving mobile Details
from a Sheet into the sidebar needs explicit accessibility and readability
qualification.

Both delta reviews support retiring the Activity workspace pane through existing
unknown-pane restoration. They identified wording conflicts with independent
Overview preferences and route precedence. Source checks confirmed both; the
spec now preserves those contracts.

## Findings and dispositions

| Finding | Evidence | Disposition |
| --- | --- | --- |
| Mobile Details currently has trapped focus; the shared sidebar does not specify entry or containment | `src/widgets/dialog/OverlayPanel.tsx`, `src/shell/activitybar/ActivitySidebar.tsx`, `src/shell/mobile/StackHost.tsx` | Require named phone surface, opening focus, sequential containment, and visible return focus through the existing focus primitive. Desktop remains nonmodal. Keep global accessibility work out of scope. |
| Renaming the recursive Activity pane to Overview gives differently scoped surfaces the same name | `src/panes/sessionPanels/SessionPanelPane.tsx`, `src/panes/session/chrome/ActivityPanel.tsx` | Jesse chose to retire the workspace pane without migration. Keep the distinct recursive Sheet and discovery owner where used. Overview names the session-scoped category sidebar and its entry points only. |
| Long identity values can be silently clipped even when the page has no overflow | `src/widgets/inspectorcard/index.tsx`, `src/widgets/inspectorcard/inspectorcard.module.css`, `src/panes/session/chrome/DetailsPanel.tsx` | Require full readable/selectable identifiers, paths, and branches at sidebar and phone widths. Measure before choosing a local presentation fix. |
| Five equal-width categories can truncate labels or crowded counts with XL text | `src/widgets/segmentedcontrol/`, `src/shell/activitybar/activitybar.module.css` | Include XL text, multi-digit counts, visible selection/focus, and touch floors in browser qualification. |
| `/status` currently advertises a toggling Details action | `src/shell/palette/commands.ts` | Change affected discovery copy to “Show session details in Overview.” Keep details/info search terms. |
| About has no footer chip for the existing return-focus fallback; phone drawer openers can become hidden | `src/shell/activitybar/activitySidebarStore.ts`, `src/shell/sessionMenu/SessionMenu.tsx` | Require a visible opener or session-qualified session-actions fallback, preserving originating-pane identity and existing activity-chip fallback. Use real menus in proof. |
| Shellguard equates category count with footer-chip count | `src/dev/shellguard-entry.tsx:82,789–795` | Assert four footer identities independently of the five categories in the existing gated harness. Adding a chipless category must not stall readiness. |
| Summary/transcript subscription counts can hide a missing About model holder | `src/stores/sessionActivity.ts`, `src/stores/useThreadModel.ts`, `src/stores/threads.ts` | Prove rich model hydration and model-holder release separately; deliver notifications after About closes. Check collection release with later invalidation/reconnect, not only immediate read counts. |
| Direct command invocation and placeholder Details panes do not establish composer or saved-pane behavior | `src/panes/session/chrome/DetailsPanel.test.tsx` | Require the real composer submission path and real registered/restored Details pane. Preserve accounting tests. |
| The earlier spec promised standalone Details and Activity URLs that do not exist | `src/shell/routing.ts:122–127` | Correct the spec. Saved Details panes remain registered; retiring Activity requires no route migration. |
| An absolute prohibition on opening Overview during restore conflicts with independently saved sidebar intent | `src/shell/activitybar/activitySidebarStore.ts:172–198,223–225` | Retirement must not create, transfer, or alter Overview intent. Restore independent closed and open-on-About choices normally and test both. |
| An unconditional Welcome fallback for Activity-only layouts would override a valid route | `src/shell/DockHost.tsx:464–495` | Preserve route precedence. Test Activity-only restoration both without a valid routed primary and with a valid session route. |
| Retiring the saved main or focused Activity pane must preserve useful remaining panes | `src/shell/workspace.ts:432–456`, `src/shell/DockHost.test.tsx:1176–1221` | Use existing unknown-pane removal and surviving-focus selection. Test actual rendering and activation for mixed, Activity-main, and Activity-focused layouts; add no Activity-specific restore shim. |
| Pane-type removal touches mobile state retention and existing registration tests | `src/shell/mobile/StackHost.tsx:379`, `src/stores/panelStoreEviction.ts:17–29,41–63`, `src/shell/paneRestore.test.ts:89–143` | Remove retired production branches and obsolete expectations. Keep the saved Activity fixture as an omission test and preserve session, Tasks, Details, doc, transcript, and retained Sheet eviction guarantees. |

Frontend paths in this table are relative to `cmd/evener-hub/frontend/`.

## Verified constraints and limits

Both reviews support reusing the shared sidebar, existing details body, public
session refs, thread holders, and activity read owners. No new metadata cache,
retry owner, route migration, backend producer change, or native/TUI work is
needed. The parent's source checks confirmed the Sheet focus primitive,
inspector overflow rule, shellguard cardinality assumption, independent sidebar
intent restoration, unknown-pane removal, and route precedence. Retiring the
workspace pane deliberately removes its combined subtree view and saved
placement. It deletes no resource records and does not broaden Overview scope.

The reviewers did not run UI tests or browsers. Clipping and focus behavior are
source-grounded risks, not measured failures. Implementation must reproduce
those scenarios and provide passing evidence before claiming them fixed.
