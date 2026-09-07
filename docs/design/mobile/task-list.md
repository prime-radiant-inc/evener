# Native task list

Reference the current web TasksPanel, taskData, taskGroups and taskTime modules, evener/tasks/list, and evener/task/updated. The panel is read-only. It exposes task descriptions, state, prompt, update notes, type, reasoning effort, dependencies and timestamps; it does not invent task mutation controls.

Open Tasks from the conversation toolbar, beside Session. Keep in-progress and open groups visible, with settled work collapsed. Each compact row expands details; prompts have a separate disclosure. Fetch on opening and current-session invalidation. Preserve loaded rows under an explicit stale warning on transient failure, distinguish unsupported from empty, and discard late results after closing or switching hubs. Reuse current-web parsing, grouping and error classification.

Verify fetch parameters, event identity, coalesced refresh, failure retention, unsupported/empty states and disposal at the transport boundary. Verify actual native list geometry, disclosures and current-hub data with the isolated real Evener runtime. Tasks are one work-management slice; jobs and subagent navigation remain separate required work.

## Evidence and remaining acceptance

The native sheet uses SectionList virtualization, stable task IDs for disclosures, and the current web parser, grouping functions, timestamp formatter and wire error classifiers. Task aggregates hydrate with the conversation and consume identity-checked events; a newer aggregate survives an older hydration. Fetches coalesce invalidations, discard disposed results and retain prior rows on transient failure. Resync and reconnect trigger fresh reads. An open sheet owns its destination independently of temporary conversation hydration, and a replacement client retains the same destination's last loaded rows.

Automated checks pass: 152 native tests, 432 targeted shared projector/service/state tests, native TypeScript and touched-file formatting. Boundary cases cover request scope, identity filtering, coalescing, disposal, unsupported/empty distinctions, terminal versus transient rejection, retry, replacement-connection retention and aggregate hydration races.

Manual iOS 26.5 iPhone 17 Pro and Android API 35 Pixel 7 evidence uses actual task_list tool execution in the isolated Evener daemon with a scripted provider on loopback 58878. Both sheets displayed four open tasks, then updated while open to one in progress, one open, one done and one cancelled. Both expanded the current task's prompt and showed its dependency, timestamps and update. The settled disclosure exposed completed and cancelled work. iOS Done and Android system Back returned to the conversation. Captures: /tmp/evener-tasks-ios.png and /tmp/evener-tasks-android.png. These captures predate the reconnect lifecycle correction below.

A controlled AppWire proxy restart followed by an injected task-list rejection exposed sheet state loss during reconnect. The corrected Release builds on both platforms kept their loaded rows under the explicit failure/stale notice. Removing the injected failure and tapping Try again cleared the warning and loaded the authoritative list. The proxy's failure flag was removed; production hub 9180 was untouched. Both final Release builds passed.

Remaining acceptance includes large lists and long prompts/notes, VoiceOver/TalkBack, large text, cross-hub navigation stress and physical devices. Native unsupported-source presentation has boundary tests but has not been manually exercised against such a source. Jobs, subagent navigation and broader work management remain required work.

## Task instruction disclosures — 7 September

A direct v4 iOS run exposed task-control reminder text and completion JSON inline
in the conversation. Typed task notices now reuse the existing collapsed notice
control. Expanding preserves the complete original text; collapsing leaves a
short task label. The control exposes its expanded state and names the current
show/hide action. Warning and critical notices remain visible, as do goal system
notices, user input and unknown steering kinds. Existing hub/session/item-scoped
disclosure state and activity preferences remain authoritative.

Six routing regressions failed before implementation. All 592 native tests and
TypeScript pass. Luna medium proposed the change and independently reviewed the
critical-notice guard. Updated Release-device disclosure acceptance is pending.
