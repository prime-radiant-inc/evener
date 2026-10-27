# Evener product guide

Evener helps people accomplish work with a coding agent. Its runtime, tools,
hub, and clients form one product: the person should be able to express an
intent, follow the work, steer it, and use the result without managing Evener's
internal machinery.

These are living references. Update the relevant page when behavior or ownership
changes; keep implementation history in Git and pull requests.

- [Product principles](principles.md): the experience changes should produce.
- [Subsystem map](subsystems.md): responsibilities, sources of truth, entry
  points, dependencies, and recovery ownership across the product.
- [Session activity](session-activity.md): scoped delegate, job and watch reads,
  navigation boundaries, pagination and shared recovery ownership.
- [Session pulse meter](pulse-meter.md): the hub's live per-session activity
  read behind the Board meter, quiet time and newest root tool intent.
- [Remote session actions](remote-sessions.md): the effective capability a
  controller advertises for a session on another host, and the recovery
  boundary between the controller and that host.
- [History content](history-content.md): selectable conversation, reasoning, tool,
  intent, system and diagnostics content on reads, pages and subscriptions.
- [Device pairing protocol](../device-pairing.md): self-hosted invitations, device comparison, encrypted credential delivery, QR and acoustic wire formats.
- [Playdate client](playdate-client.md): direct session browsing, chat transcripts,
  voice review, acoustic setup, native enrollment and device-owned draft recovery.
- [Comic Chat](comic-chat.md): literal comic-panel conversations, thought bubbles,
  inline subagent readers and shared composer recovery, selected in Verbosity.
- [Memory](memory.md): host-owned personal/project files, lower-trust indexes,
  scoped tools, gardening, recovery and per-session opt-out.
- [Friction punchlist](friction.md): open, evidence-backed gaps, with unresolved
  proposals, agreed outcomes and deferred work identified separately. Discuss
  unresolved choices individually; an agreed outcome still needs implementation
  and verification before it describes current behavior.
- [Compatibility](#compatibility): which direction of version skew each boundary tolerates.
- [Code architecture](../architecture.md): module and process structure.
- [Web design system](../web-ui/design-system.md): shared visual and interaction
  contracts. [Native development](../../mobile-native/README.md) describes the
  phone client's implementation and qualification.
- [Developing Evener](../developing-evener/README.md): how to build and verify a
  change.

## Compatibility

Users upgrade at different times and skip versions. Daemons outlive the hub
that spawned them ([`appwire/types.go`](../../appwire/types.go) explains why a
hub restart leaves older daemons attached), so version skew is routine rather
than an error state. The direction of the skew decides who adapts:

| Boundary | Must tolerate | May assume |
|---|---|---|
| Hub and its daemons | The hub works with any older daemon. | Daemon code may assume the hub is the same version or newer, and carries no fallback for an older hub. |
| Phone or web client and hub | The client works with an older hub, including the combined update/apply path. | Hub code carries no fallback for an older phone app. |
| Local hub and remote hub over SSH | The local hub works with an older remote hub. | Remote-side code carries no fallback for a newer local hub. |
| On-disk state | Every persisted format stays readable and every migration stays, because users skip versions. | Nothing is dropped on a time window alone. |

Code that serves no producer in any direction is dead code, not compatibility,
and is deleted. New compatibility code names the version direction it serves
and the condition that removes it in its pull request description.

## Keeping the guide useful

The principles express product intent. The map describes the implementation.
The punchlist records gaps between them. Keep those meanings distinct: adding a
case does not silently change a runtime contract, and documenting existing
behavior does not endorse it.

When changing a subsystem, follow the map to its dependent surfaces and update
its row and linked contract in the same change. When discussing a punchlist case,
record the chosen behavior beside the evidence. Once implemented and verified,
put the durable rule in the owning guide and remove the resolved case from the
open punchlist. Git retains its history. Do not renumber surviving case IDs.

Use stable filenames, present-tense explanations, source links, and named
functions or types. Do not turn these pages into release notes, dated audit
diaries, PR progress reports, or duplicate implementation plans. Recheck source
references when touching a case; a historical decision document cannot establish
what the current product does.

Delete an implementation plan in the PR that completes it; Git retains it.
