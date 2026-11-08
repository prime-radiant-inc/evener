# Global compact serif and Duo composer density

## Intent

Jesse selected the Compact type direction and explicitly chose **smaller serif prose**, not a global sans transcript. The existing session screen should keep its content, controls, states, and navigation while using space more deliberately. The Duo composer and its status footer need substantially less vertical chrome.

## Scope

- Reduce the web UI's shared prose size from 18px to 16px and introduce a dedicated 1.45 prose-leading token at the default font scale.
- Apply that prose-leading token to reading surfaces, including the shared Markdown widget and its live/settled transcript parity paths. Do not change `--line-height-body`, which also controls editors, dialogs, banners, and other UI.
- Keep Source Serif 4 as `--font-prose`; retain the user's existing font-size and density preferences.
- On the browser-rendered session screen at the Duo cover viewport, 466 × 678 points, compact the existing session composer, SessionChrome, and working-location/activity footer. The supplied screenshot is this web UI, identified by its existing composer and StatusBar controls. This pass does not change the separate native app.
- Keep Agents, Jobs, Watches, Tasks, their existing `mobileCount` values, and their existing activity-sidebar actions in the app. Full fractions remain in accessible names/tooltips/Overview; labels may yield to compact icon/count presentation when space is constrained.
- Preserve the current phone footer omission where already implemented. Where the working-location/activity footer exists, keep its one compact row and reduce its framing and path treatment while retaining the existing chip overflow and 44px targets.

## Non-goals

- No new briefing, answer, queue, summary, or navigation mode.
- No change to Source Serif, UI sans, code monospace, or their assigned roles.
- No reduction of the editable text size below 16px on phones.
- No removal of existing composer controls, status counters, or activity actions.
- No modification of the OS-owned Duo side rail. Reviewed Apple ActivityKit and WidgetKit documentation provides Live Activities and widgets on other surfaces, not customization of this system strip; no public API or existing repository bridge was found. This is a documented-absence conclusion, not a claim about private APIs.
- No native-app rewrite or new Duo hardware API. The native Board is a separate full session list, not the pictured OS strip or this web session screen.

## Decisions and constraints

- Use shared type tokens rather than adding one-off transcript font sizes.
- Duo-specific layout rules live with the existing composer/status CSS. This is a responsive 466px cover-width treatment, not reliable device detection; browser CSS cannot distinguish a Duo from another viewport of the same size. Use viewport/media and component/container queries as appropriate without changing ordinary desktop docked-pane behavior.
- Keep 44px primary touch targets and existing keyboard/safe-area clearance. Compact spacing must not place controls on top of transcript content or each other.
- Preserve the existing composer behavior: Stop for an empty live draft, Send for a sendable draft, Steer placement, question and approval docks, pending/queued messages, attachments, drafts, focus, and session menus.
- The activity counters remain owned by `StatusBar` and `deriveScope`; this is presentation work only. No activity count or navigation semantics change.
- This pass does not add liquid-glass overlays. It changes the type ramp and the arrangement and density of the existing surfaces only. A separate overlay treatment would need its own contrast and scroll-clearance design.

## Acceptance and test plan

1. The default prose token computes to 16px with 1.45 line-height, and Source Serif remains the prose family. Existing font-size and density overrides continue to scale it.
2. Existing token and frontend tests are updated only for the intentional metric change; a browser measurement confirms the new prose metric at desktop and phone widths.
3. At 466 × 678, measure the baseline and resulting total footer height for the empty/live, filled, attachment, and question/approval states. The compact empty/live dock must lose at least 48px of vertical chrome while keeping every original control and its target. Record exact baseline and target heights in the fixture; do not use an unmeasured "materially less" assertion.
4. Agents, Jobs, Watches, and Tasks retain their existing `mobileCount` values and open the same Overview category. The activity read remains the same shared source. All four links retain at least 44px targets and the path/branch must not force horizontal overflow.
5. Empty/live and filled composer states, attachment staging, question/approval docks, focus, and keyboard-open safe-area clearance retain their existing behavior and usable targets. Test these states explicitly rather than relying on the existing desktop-only fixed-control harness.
6. Add a real 466 × 678 mobile/touch browser fixture that loads the full composer, SessionChrome, StatusBar, and RepoLocation styles, with baseline and resulting heights, hit-target, overflow, and transcript-clearance assertions. Run `npm run layoutguard`, targeted frontend tests, `make test-web`, and `make test-web-browser`; inspect the Duo viewport before/after.
7. Update `docs/web-ui/design-system.md`, the relevant product subsystem entry, and Duo/front-screen project memory to describe the chosen compact-serif rule and actual app-owned rail limitation.

## Open implementation choice

The exact compact pixel values will be chosen against the existing component geometry rather than copied from the static mockup. The goal is a compact dock, not tiny type or unreachable controls. No new widget API is planned; if the existing widgets cannot express the arrangement without violating their contracts, stop and raise that before extending one.
