# Web session Overview: adversarial spec review

## Verdict

The spec is ready for Jesse's review before planning. Two independent read-only
reviews examined draft commit `4c37fc2607`. The revised spec incorporates their
scope, accessibility, geometry, and test corrections. No product code, tests,
dependencies, or provider requests ran in this phase.

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

## Findings and dispositions

| Finding | Evidence | Disposition |
| --- | --- | --- |
| Mobile Details currently has trapped focus; the shared sidebar does not specify entry or containment | `src/widgets/dialog/OverlayPanel.tsx`, `src/shell/activitybar/ActivitySidebar.tsx`, `src/shell/mobile/StackHost.tsx` | Require named phone surface, opening focus, sequential containment, and visible return focus through the existing focus primitive. Desktop remains nonmodal. Keep global accessibility work out of scope. |
| Renaming the unchanged recursive Activity pane to Overview gives two different surfaces the same name | `src/panes/sessionPanels/SessionPanelPane.tsx`, `src/panes/session/chrome/ActivityPanel.tsx` | Keep legacy Activity titles and triggers unchanged. Overview names the combined category sidebar and its entry points only. |
| Long identity values can be silently clipped even when the page has no overflow | `src/widgets/inspectorcard/index.tsx`, `src/widgets/inspectorcard/inspectorcard.module.css`, `src/panes/session/chrome/DetailsPanel.tsx` | Require full readable/selectable identifiers, paths, and branches at sidebar and phone widths. Measure before choosing a local presentation fix. |
| Five equal-width categories can truncate labels or crowded counts with XL text | `src/widgets/segmentedcontrol/`, `src/shell/activitybar/activitybar.module.css` | Include XL text, multi-digit counts, visible selection/focus, and touch floors in browser qualification. |
| `/status` currently advertises a toggling Details action | `src/shell/palette/commands.ts` | Change affected discovery copy to “Show session details in Overview.” Keep details/info search terms. |
| About has no footer chip for the existing return-focus fallback; phone drawer openers can become hidden | `src/shell/activitybar/activitySidebarStore.ts`, `src/shell/sessionMenu/SessionMenu.tsx` | Require a visible opener or session-qualified session-actions fallback, preserving originating-pane identity and existing activity-chip fallback. Use real menus in proof. |
| Shellguard equates category count with footer-chip count | `src/dev/shellguard-entry.tsx:82,789–795` | Assert four footer identities independently of the five categories in the existing gated harness. Adding a chipless category must not stall readiness. |
| Summary/transcript subscription counts can hide a missing About model holder | `src/stores/sessionActivity.ts`, `src/stores/useThreadModel.ts`, `src/stores/threads.ts` | Prove rich model hydration and model-holder release separately; deliver notifications after About closes. Check collection release with later invalidation/reconnect, not only immediate read counts. |
| Direct command invocation and placeholder Details panes do not establish composer or saved-pane behavior | `src/panes/session/chrome/DetailsPanel.test.tsx` | Require the real composer submission path and real registered/restored Details pane. Preserve accounting tests. |

Frontend paths in this table are relative to `cmd/evener-hub/frontend/`.

## Verified constraints and limits

Both reviews support reusing the shared sidebar, existing details body, public
session refs, thread holders, and activity read owners. No new metadata cache,
retry owner, route migration, backend producer change, or native/TUI work is
needed. The parent's source checks confirmed the Sheet focus primitive,
inspector overflow rule, and shellguard cardinality assumption.

The reviewers did not run UI tests or browsers. Clipping and focus behavior are
source-grounded risks, not measured failures. Implementation must reproduce
those scenarios and provide passing evidence before claiming them fixed.
