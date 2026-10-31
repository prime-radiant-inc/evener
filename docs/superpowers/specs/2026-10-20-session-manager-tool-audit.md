# Manager tools: command-surface audit

**Revised surface: extend eight existing tools and add only `session_control`.** Use manager-only parameters on `delegate` for top-level creation and `job_stop` for existing human stop actions, as Jesse directed. Remove proposed `session_list`, `session_read`, `session_send` and `session_start`.

This updates the audit's original two-new-tool recommendation after Jesse chose manager-only creation and stop parameters. The [manager design](2026-10-20-session-manager-design.md) incorporates this reduction and is approved. Existing product code remains paused and preserved pending review of the replacement implementation plan; this audit describes planned behavior, not an implemented interface.

## What gets smaller

| Need | Proposed surface | What it reuses |
| --- | --- | --- |
| Find sessions | Extend `job_list` with explicit manager-only hub scope. | Hub discovery and filtering. Ordinary calls still list the caller's work. |
| Inspect a session | Extend `job_status` with hub-qualified targets. | Existing session metadata, supported actions, pending-question summary and pending approvals. |
| Read history and full questions | Extend `read_transcript` with hub-qualified references. | Existing bounded hub history reads and their paging. |
| Inspect a worker's activity | Extend `job_list` with an explicit target and session/subtree scope. | Existing delegate, shell-job and watch collections. |
| Send input or answer a question | Extend `delegate_send`, as Jesse directed. | Existing human input handlers for hub targets, existing delegate dispatcher for owned delegates. |
| Watch progress and results | Extend `job_watch`, as already proposed. | Hub subscriptions and existing watch/notification delivery. |
| Choose an available model | Extend `model_list` with a manager-only source-host selector. | The selected host's model catalog and availability information. |
| Create an independent worker | Extend `delegate` with a manager-only top-level mode. | Existing human session creation, preserving ordinary owned delegation. |
| Stop work | Extend `job_stop` with explicit manager-only human stop modes. | Existing interruption, single-delegate stop, shutdown and force-stop owners. Ordinary owned-work cancellation stays unchanged. |
| Use the remaining human controls | Add typed `session_control`. | Existing approval, resume/fork/Clear, settings and other session-specific mutation handlers. |

The eight extensions are `job_list`, `job_status`, `read_transcript`, `delegate_send`, `job_watch`, `model_list`, `delegate` and `job_stop`. Only Manager schemas expose the new parameters, modes and target forms; execution also checks current Manager authority. Ordinary calls retain their existing contracts. This still requires hub routing, bounded result adapters and notification delivery. Existing tools are not already hub-wide.

### Discovery and activity remain separate reads

Hub discovery stays shallow. A session row supplies a source-qualified reference, status and capabilities. Also expose reachable source references from the existing host-list owner, including hosts with no sessions, so models and first-worker creation remain discoverable. This adds no host-administration control. Reading a session's delegates, shell jobs or watches is a separate, explicitly scoped `job_list` call. Reuse the existing activity cursors and generation metadata rather than crawling every session's descendants to assemble a catalog.

The default `job_list` scope stays unchanged. A hub session is neither a shell job nor an owned delegate. Results must identify its kind and actual owner, and must not imply that visibility grants delegate ownership. Preserve session states instead of translating them into misleading shell-job outcomes. Hub pagination must retain the hub's cursor contract instead of pretending it is the current local offset list. [Sources 1–3]

### Status and history have different jobs

`job_status` is the compact current-state read. For a hub target, reuse the existing thread metadata: identity, instance, capabilities, input queue, goal, notes, question summary and pending approvals. Preserve whether the source is live and authoritative or retained. A question preview is not the full set of questions and options.

Pending approvals are currently human-client fields, absent from the worker's transcript. Making them readable by an authenticated manager implements Jesse's delegated-authority decision. Ordinary agents must not gain that read merely because the tool keeps its name.

`read_transcript` supplies the history, including full question arguments. Its existing local session, shell-output and artifact formats remain unchanged. A hub history reference needs an explicit projected-history result with its real cursor and snapshot identity. It must not label projected AppWire items as exact `transcript_v2_jsonl`, reinterpret local turn ranges, or claim unsupported raw expansion. Return supported read modes with the reference and reject incompatible arguments. This is the main contract work hidden by the otherwise attractive reuse. [Sources 3–4]

The same tools cover a hub worker's shell jobs: `job_status` uses the existing full job-metadata read, and `read_transcript` uses the existing job-output read. A job reference must retain both its owning session's source-qualified reference and its job ID. Hub job output pages backward with `beforeBytes`, unlike the local tool's forward byte offset. Preserve that distinction, byte ranges, encoding and retention floor; do not promise local search or paging options the hub endpoint lacks. [Source 11]

### Messages and watches preserve their existing distinctions

Owned `dlg_...` messages retain their current `delegate_send` routing, resume, steering and result handling. Hub session messages use the target's existing human input modes and receipts, including supported typed skill or command input. They do not acquire a parent relationship or promise the owned-delegate `earlier_results` behavior. Loading a skill into the manager with `use_skill` does not load it into the worker.

A watched root reports through its existing `communicate` output and the watch bridge. Its report does not become a delegate return packet. Keep snapshots, reconnect handling, generation boundaries and one-shot completion as specified in the design. A notification wakes a session subject to its question gate; answering a question requires user input. [Sources 5–6]

### Shared remote input needs qualification

Remote input forwarding exists, but the baseline controller-side capability mask advertises only shutdown among thread mutations. It masks `Send`, `Queue`, `Steer` and typed input even when the remote daemon advertises them. Plain-text forwarding can still be reached, so this is a discrepancy between forwarding and advertised support, not proof that all remote input fails. [Source 12]

Jesse chose to include qualifying and enabling the shared remote input capabilities needed for the manager's question-and-answer loop. Make that support real in the existing `appsource` and human-input owners, and advertise the same qualified capabilities to human clients and managers. Preserve unsupported actions rather than blanket-enabling mutations or bypassing the mask only for managers. The required remote loop must run; treating its needed capabilities as unsupported and skipping it does not satisfy the design. This prerequisite adds work, not another tool.

### Model discovery belongs in `model_list`

The current tool reads the caller's frozen startup catalog. That cannot establish which models a remote host can run. Give enabled managers an explicit source-host query backed by the existing hub catalog. Keep ordinary calls frozen as today. Selecting a worker's model remains a typed human control, not a mutation hidden in `model_list`. [Source 7]

### `delegate`: explicit top-level creation

Ordinary `delegate` creates an owned conversation with delegate identity, inherited constraints and parent-directed reporting. The current runtime deliberately denies `ask_user` and sandbox escalation to delegates. Preserve that behavior when the new parameter is absent.

The manager-only top-level mode calls the existing hub session-creation handler. It creates an ordinary interactive root with normal questions and approvals, independent lifetime, and Manager off. Return its source-qualified session reference and actual partial outcomes rather than a delegate ID or parent-return promise. Keep mode-specific arguments explicit and reject inapplicable ones. The design's empty-create, watch/read, then-input sequence can be presented in this mode without a new launch protocol. [Sources 5, 8]

The audit originally recommended a separate `session_start` because these creation contracts differ. Jesse chose an explicit parameter instead. The contracts need distinct schemas and results, not distinct tool names.

### `job_stop`: explicit human stop actions

Ordinary `job_stop` retains owned-work cancellation, including recursive owned-delegate subtree stop. Manager-only hub targets require an explicit human stop action. Existing human delegate-stop ends only the selected delegate's current run, leaving its children running. Root interruption, shutdown and force-stop have separate meanings and supported targets.

Route each action to its existing owner. A hub delegate reference must never silently select recursive subtree stop. Keep these manager stop actions out of `session_control`; the explicit modes preserve their distinctions within one tool. This supersedes the audit's original grouping of external stop actions under `session_control`. [Sources 2, 8, 10]

## Why one new tool remains

### `session_control`: remaining human controls

No exposed tool resolves another session's sandbox approval, sets its model, resumes or clears it, or changes its human whiteboard or goal. These require an adapter regardless of naming. Keep one fixed, typed action set routed to the existing owners. Account for existing session archive/unarchive, deletion, pin assignment/unpin and seen/unread controls too, wherever supported. Do not duplicate creation, messaging or stop actions, expose arbitrary RPC names, or add project/host administration or a generic remote-tool executor.

Keep the distinctions that the current tools encode:

- `ask_user` asks a question. It does not answer another question or resolve an approval.
- `update_goal` declares the current agent's goal complete or blocked. The human goal control sets or clears the objective.
- `notes_agent_set` writes the agent's own whiteboard. The human whiteboard has a separate owner.
- `compact_context` schedules the caller's compaction at its round boundary and carries a note to that same agent. Human-requested compaction has a different entry point.
- `task_list` mutates the caller's bound task store. The existing human task API is a read. Adding arbitrary remote task mutation would expand this feature.

These distinctions leave genuine human controls for `session_control` after the eight extensions. [Sources 8–9]

### Further reuse that saves no tool

Targeted `notes_read`, read-only `task_list` and `urls_remove` are credible extensions. External compaction could also fit `compact_context` if its target-specific arguments were explicit. These would move small reads or actions out of the status/control adapters while adding manager branches to more tool schemas. I recommend keeping notes in the existing target snapshot, target task details in a bounded status detail read through the existing task-list owner, and human URL removal and compaction in `session_control`.

This is a grouping recommendation, not a capability restriction. Preserve full task detail on demand; an aggregate is insufficient. Target-scoped watch listings remain read-only and do not grant control over another session's watches. [Sources 3, 8–9]

## Every exposed command

The inventory below accounts for all **36** tools exposed to this session. Names follow the live tool definitions, including provider aliases. “Keep” means no additional manager-specific behavior is proposed for that tool; the ordinary tool remains available under its existing rules.

| Existing command | Disposition |
| --- | --- |
| `job_list` | Extend for explicit hub discovery and target-scoped activity. Replace `session_list`. |
| `job_status` | Extend for current hub session state, bounded task detail and full job-metadata reads. Absorb the state portion of `session_read`. |
| `read_transcript` | Extend for bounded hub history and job output with honest format and paging distinctions. Absorb the history portion of `session_read`. |
| `delegate_send` | Extend for hub input, preserving owned-delegate semantics. Already directed by Jesse. |
| `job_watch` | Extend for hub session observation through the existing notification machinery. |
| `model_list` | Extend for the selected host's catalog. Keep selection in `session_control`. |
| `delegate` | Extend with a manager-only top-level creation parameter. Preserve ordinary owned creation and reporting. |
| `job_stop` | Extend with explicit manager-only human stop actions. Preserve ordinary owned-work cancellation and recursive delegate stop. |
| `communicate` | Keep current human/parent reporting and end-turn behavior. Observe root reports through watches. |
| `ask_user` | Keep question creation. Answer through `delegate_send`, approve through `session_control`. |
| `find_session_transcripts` | Keep archival search. Its local bucket scan cannot establish live hub availability. A future remote archive-search adapter could reuse it if needed; it is not a second session catalog. |
| `compact_context` | Keep self-compaction. Use the existing human compaction owner for another session. |
| `task_list` | Keep the caller's task store. Read target task state through existing hub reads; do not add remote task mutation. |
| `update_goal` | Keep terminal goal reporting. Human objective changes belong in `session_control`. |
| `notes_agent_set` | Keep agent whiteboard ownership. Human instructions use the existing human-note mutation. |
| `notes_read` | Keep the local shared-notes snapshot. Target metadata already carries its notes and URLs. |
| `urls_add` | Keep local link creation. Do not introduce a remote URL-add API solely for symmetry. |
| `urls_remove` | Keep local link removal. The existing human target-URL removal can remain a typed session control. |
| `manage_worktree` | Keep repository/worktree management. It neither creates an interactive root nor routes through a remote hub. |
| `doctor_evener` | Keep read-only forensics. Do not make a state-file diagnostic reader the live manager API. |
| `use_skill` | Keep loading guidance into the caller. It is not a remote session-control mechanism. |
| `read_file` | Keep file reads. Do not infer live target state from private storage files. |
| `write_file` | Keep file writes. Do not bypass session owners by writing their state. |
| `edit_file` | Keep exact file edits. Same boundary as `write_file`. |
| `apply_patch` | Keep source/file patches. Same boundary as `write_file`. |
| `list_dir` | Keep directory listings. Filesystem layout is not hub discovery. |
| `find_files` | Keep file-path search. Same boundary as `list_dir`. |
| `grep_files` | Keep content search. It supplies no current session control state. |
| `exec_command` | Keep shell execution. A CLI or script wrapper would hide the adapter and credential handling, not eliminate them. |
| `web_fetch` | Keep URL reads. It is not an authenticated session-operation client. |
| `plugin_superpowers_chrome_chrome__use_browser` | Keep browser work. Driving human controls in a browser is a workaround, not the native manager interface. |
| `memory_read` | Keep the bound personal/project memory read. Memory is not live session state. |
| `memory_search` | Keep memory search. Same boundary as `memory_read`. |
| `memory_write` | Keep durable memory notes. Do not add a manager assignment or approval store. |
| `memory_edit` | Keep memory edits. Same boundary as `memory_write`. |
| `memory_delete` | Keep memory deletion. Same boundary as `memory_write`. |

The unrelated file, memory, browser and web tools were checked against their exposed contracts. The plausible manager-reuse candidates were also traced to their source owners. This audit does not claim a line-by-line review of every unrelated tool implementation.

## Constraints the reduction keeps

- Everything reachable through the hub stays in scope, including remote sessions and other managers.
- Manager authority remains human-controlled, checked on each hub admission, and persistent until Jesse turns it off.
- Added parameters and `session_control` are exposed only in Manager schemas. Hiding them does not replace execution-time checks, including after revocation.
- The design adds proposed conditional Manager system-prompt guidance, aligned with the current tools and authority. Exact arguments remain in tool descriptions.
- Ordinary delegation, recursive owned-delegate stop and other default tool behavior remain unchanged. Explicit hub modes use the corresponding existing human owner.
- Each target uses the shared human capability boundary. Required remote input support is qualified and enabled for humans and managers together. Readable delegates do not gain unsupported direct input or configuration.
- Existing attribution, input retry identity, approval conflicts and partial launch outcomes remain authoritative.
- Reads preserve source identity, paging, availability and generation information. Reading another owner's state does not acknowledge its pending delegate report.
- Existing notification and watch recovery promises remain required. This audit adds no offline monitoring service.
- No new project grants, ownership assignments, operation journal, approval store or launch/Stop recovery framework.

## Source evidence and limits

Source citations refer to pre-manager commit `34d4ea239ec6c192838b544d6c4dd4e7f70c04a1`, not the paused framework in the working tree. Inspect a cited file with `git show 34d4ea239ec6c192838b544d6c4dd4e7f70c04a1:<path>`.

1. **Tool contracts:** `agent/internal/tool/definitions.go`, particularly delegate creation at 228–301, messaging at 365 onward, watches at 408 onward, job reads at 477–530, stop at 532 onward, transcript search at 919 onward and transcript reads at 1141 onward. The session's exposed definitions also cover generated memory wrappers, compaction and the browser plugin.
2. **Local job ownership:** `agent/session_tools_jobs.go:699–746,800–903,1177–1277`. Status/list dispatch use the caller's jobs and delegate visibility. Delegate stop invokes `StopSubtreeAndDrive` at 1253.
3. **Hub state and activity:** `appwire/types.go:1046–1093,1152–1186,2157–2203,4975–4980`; `appwire/session_activity.go:23–37,47–67,97–109,178–197`. Metadata carries capabilities and pending state; host discovery supplies references independently of session rows. Activity collections have explicit scope, independent cursors and source availability.
4. **History contracts:** `agent/session_tools_transcript.go:337–388,788–829`; `appwire/types.go:2157–2203,2341–2378`. Local history and exact raw expansion differ from projected hub history. `agent/session_tools_find.go:195–212,482–530` searches local state buckets.
5. **Messaging and reporting:** `agent/session_tools.go:117–169`; `agent/session_tools_communicate.go:129–149,175–215`; `appwire/types.go:1545–1555`. Owned sends use the delegate runtime. Communicate records the current session's message and uses its existing parent callback where applicable. Human input capabilities include typed skills and commands.
6. **Notifications and question gating:** `agent/session_queue.go:223–242`; `agent/session_lifecycle.go:1138–1163`; `agent/job_watch.go:3444–3474`. Existing routes and causal metadata need the hub bridge described in the design.
7. **Model discovery:** `agent/session_tools_model_list.go:13–37`; `appwire/protocol.go:251`. The existing tool pages the caller's startup snapshot, while the hub model method includes launch diagnostics.
8. **Interactive-root and human controls:** `agent/session_tools_ask.go:50–75`; `agent/session_escalation.go:136–147`; `appwire/protocol.go:171–192,218–228,284–286`; `cmd/evener-hub/app_threadlifecycle.go:69–75`. Delegates lack `ask_user` and sandbox escalation; the existing human lifecycle supports remote start and distinct target controls.
9. **Self-owned tools:** `agent/session_tools_goal.go:13–40`; `agent/session_tools_notes.go:14–85`; `agent/session_tools_compact.go:13–92`; `agent/session_tools_task.go:483–545`. These bind to the caller's state and are not arbitrary target setters. The human full-task read is `appwire/types.go:2837–2845`; human versus agent note/URL capabilities are explicit at `:1533–1539`.
10. **Human single-delegate stop:** `cmd/evener/serve.go:1468–1470`; `agent/delegate_user_stop.go:24–49,90–108`. The callback cancels the selected live run, not its descendant tree.
11. **Hub job reads:** `appwire/types.go:3010–3048`; `appwire/protocol.go:194–195`. The existing human job APIs distinguish owner-qualified metadata from byte-paged output.
12. **Remote capability prerequisite:** `cmd/evener-hub/internal/appsource/remote_hub_refs.go:89–145,343–350` defines and applies the shutdown-only mask. `remote_hub_mutations.go:127–139` in the same directory forwards `StartTurn`. `cmd/evener-hub/app_relay.go:2117–2129` validates typed skill/command capabilities, with no such check for plain text. These paths explain the design conflict; they do not qualify the remote loop at runtime.

An independent read-only scout supported the original two-new-tool recommendation before Jesse chose the `delegate` and `job_stop` extensions. That review established the semantic distinctions above; it did not review this later grouping. Its shell failed before execution because the sandbox profile exceeded the platform limit. It used exact baseline files exported by the controller; the controller checked the material findings against those sources. The scout did not run tests or modify files.

A subsequent parallel adversarial review of the eight-extension surface found the remote-capability conflict. Jesse resolved the scope choice by including shared remote input support. The master design now names that prerequisite and separately proposes the missing Manager prompt guidance.

No runtime tests ran for this audit. The recommendations require implementation and the real local/remote acceptance loops in the design. Fewer tool names are not evidence that those adapters already work. Session archive/delete, pin/unpin and seen-state were inventoried from the method catalog, not qualified through every implementation handler.
