# Activity viewer task summaries and row padding

## Intent

Jesse wants the Tasks section of the activity viewer to omit the redundant
“N of M” summary lines shown above the task groups in the supplied screenshot.
The task rows should begin beneath the activity tabs without those extra lines.
Jesse also wants consistent padding inside activity rows. The shared outer
padding remains unchanged.

## Scope and acceptance

- Remove the navigation-derived “N of M done” line from the web Tasks tab,
  including its appended current-task description.
- Suppress the shared task body's aggregate header in that tab. This includes
  empty, partially completed, completed and cancelled lists, regardless of the
  aggregate label's wording.
- Keep the Tasks tab's done/total badge and accessible label.
- Keep Current and Remaining group headings, group counts, settled-task
  disclosures, row status, timestamps, prompts and updates.
- Keep the existing aggregate header in the standalone Tasks pane and Tasks
  Sheet. Keep their trigger labels unchanged.
- Align Agents, Jobs, Watches and Tasks row padding with the existing task
  disclosure: `var(--space-1)` vertically and `var(--space-2)` horizontally
  (currently 4px and 8px). A watch's nested row must not add a second inset.
- Preserve row navigation, focus, touch targets, disclosures and paging.
  About's field layout and all tabs' shared outer padding stay unchanged.
- Native client rendering, task persistence, protocol values, read ownership,
  subscriptions and refresh triggers are outside this change.

## Implementation boundary

`cmd/evener-hub/frontend/src/shell/activitybar/TasksTab.tsx` composes a
navigation summary and `TasksPanelBody`. Remove the former and pass an explicit
presentation option to suppress the latter's aggregate header. The shared body
continues to render its header for other callers by default. Do not duplicate
the task list or change its store.

`cmd/evener-hub/frontend/src/shell/activitybar/activityRows.tsx` and
`activitybar.module.css` own the resource-row layout. Apply padding once at each
interactive row boundary. Standalone Agents and Jobs rows use the disclosure's
spacing tokens; Watches retain the disclosure's padding and omit the nested
row's extra padding. Keep the common row layout shared.

Update the browser presentation contract in `docs/product/session-activity.md`
and its subsystem reference in `docs/product/subsystems.md`.

## Recovery and preservation

Only presentation changes. Loading, unavailable, empty and failed-read states
stay visible. A failed refresh retains loaded rows and its existing stale-data
notice and retry control. A successful retry returns to the useful task list
without restoring either redundant summary. Disclosure and scroll intent remain
session-scoped. No task or other user data is deleted.

## Behavior evidence

Exercise the real ActivitySidebar and TasksTab with a transport-boundary fixture
and the real thread and task stores:

1. Open Tasks with an authoritative empty list. Confirm its empty state and
   `0/0` tab badge, with no redundant body summaries.
2. Load open, current, completed and cancelled tasks. Confirm the group headings,
   counts and task disclosures remain usable, with no redundant body summaries.
3. Deliver task updates while open. Confirm the tab badge and groups refresh,
   without restoring summaries.
4. Fail a refresh after a useful list loaded. Confirm retained rows and recovery
   controls, then retry successfully and confirm the new rows.
5. Retain existing shared-body and standalone-panel tests as evidence that their
   aggregate headers and triggers still work.

Use a real-browser guard with the production row components and styles at
desktop and phone widths. Measure the glyph's inset from its row boundary for
Agents, Jobs, Watches and Tasks; each must match the existing task disclosure's
8px inset, with 4px vertical padding. Cover clickable and non-clickable Agents
and Jobs rows, and confirm the shared viewport padding is unchanged. Keep the
existing action and disclosure behavior checks.

New assertions must fail against the unmodified renderers before the change.
Run the targeted task-tab and task-panel tests, then the canonical `make test-web`
gate and relevant real-browser geometry checks. Read every invoked test script
first.

## Delivery

Review the frozen specification and immutable implementation diff with two
independent PAR reviewers. Simplify the change, shepherd its PR through CI and
review, verify the squash merge, and clean up owned disposable artifacts.
Deployment is separate and is not requested.

The base is `abd240265411bb1a35f10d1761a56cf0685339ea`. Work is isolated on
`wip/remove-activity-task-summaries`. Preserve `batch-notes.md`,
`evener-fluency.exe` and `notes/experience/isolation-missed-project-config.md`
in Jesse's main checkout; they are unrelated untracked files.
