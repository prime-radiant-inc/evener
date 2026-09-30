# Product principles

Evener is a capable coding agent. Its product design should help the user achieve
their aims with as little friction as possible. Reliability is measured by the
user's ability to continue their work, not by how conclusively a component can
refuse to operate.

## Let the user work

Keep reading, composing, navigation, and unaffected work available when one
dependency is unhealthy. Carry intent through ordinary reconnects, backgrounding,
process restarts, and temporary resource failures. Preserve drafts, attachments,
queued messages, reader position, and the identity of the work being performed.

Make the natural action sufficient. Scrolling toward older content should obtain
it; opening a session should establish what is needed to use it. A person should
not have to understand cursors, subscriptions, daemon generations, cache state,
or storage internals to finish a normal task.

## Recovery belongs to the product

Recoverable failures need an owner that can observe improvement, retry or repair,
and restore ordinary operation. A retry limit can bound one attempt without
abandoning the user's underlying intent. Backoff, reconnect events, foreground
events, and successful dependency probes are ways to re-enter recovery without
spinning or flooding a service.

A banner, error code, disabled button, or "Retry" action explains a state; it does
not by itself implement recovery. When a repair is already determined by the
user's intent and the available state, Evener should carry it out. Ask for a
decision when there is a meaningful choice the product cannot infer. Explain
that choice in terms of the user's work, retain their input, and continue whatever
can still make progress.

Prefer repairing the affected connection, record, cache, worker, or session over
restarting unrelated work. Derived data should be rebuildable from its authority.
The component that owns durable intent should also make clear who resumes it
after interruption.

## Preserve work while restoring capability

Automatic recovery must serve the original intent. Replaying an acknowledged
message, dropping an unacknowledged draft, discarding working files, or selecting
a different destination without the user's choice does not meet that standard.
Reconcile uncertain results with the authoritative state and retain operation
identity across attempts wherever the protocol supports it.

Distinguish a broken view or cache from missing authoritative data. Explain what
is known, keep usable content accessible, and isolate the smallest affected
operation while resolving the uncertainty. A permanent refusal needs more
justification than the temporary condition that first triggered it.

## Make restrictions earn their cost

"Fail closed," "fully locked down," and "defensive" are implementation labels,
not product goals. For a proposed restriction, identify the user intent or
concrete guarantee it serves, what useful work it prevents, and how the user gets
back to work when its triggering condition changes.

Honor boundaries the user actually chose. Make capability limitations accurate,
discoverable, and consistent between tools and clients. Missing cached metadata
is not evidence that a capability is permanently unavailable. Review automatic
restrictions, arbitrary limits, and approval demands against the task the user
is trying to complete.

The [punchlist](friction.md) records specific policy choices and their decisions;
these principles do not resolve an undecided choice by implication.

## Communicate in proportion to the interruption

Ordinary recovery should be quiet when it does not affect the task. When waiting
matters, show a concise status that says what Evener is doing and keep relevant
controls usable. A recovered dependency should clear its failure state. Successful
self-repair should not look like a new failure requiring attention.

When input really is needed, explain the consequence and the smallest useful
decision. Give the user an action in the place they are working, with their work
intact. Avoid asking them to reproduce internal setup steps, search another screen
to discover whether an action happened, or repeatedly make the same decision.

## Verify the whole experience

Trace a failure from its trigger through the owner, transport, state store, and
client. Verify the recovery transition as well as the initial refusal. A useful
contract is that the user's task resumes after a transient dependency recovers,
with no duplicate side effects or lost work.

Use deterministic faults at real component boundaries for plumbing tests.
Exercise representative content and navigation when the experience depends on
layout, scrolling, focus, accessibility, or mobile lifecycle. Keep source tracing,
passing tests, and observed user journeys distinct; none substitutes for evidence
it does not provide.
