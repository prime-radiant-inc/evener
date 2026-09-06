# Mobile UX philosophy

**Proposed direction · 5 September 2026**

Evener should feel like a place to build, not a remote console squeezed onto a phone. A person should be able to understand what is happening, make a decision, and continue work without decoding the system’s internal vocabulary.

## Ground every feature in Evener

Use the current web UI, server contracts, and Jesse’s explicit requests to establish product behavior. The old mobile UI has no authority over feature scope. Reused client code must be checked against current behavior; its existence does not establish a requirement. Research may shape presentation and native interaction, but must not introduce unsupported actions, states, or model options. Record unresolved differences in the [web parity audit](web-parity-audit.md).

## Make the work the visual center

The session’s content deserves the largest share of attention. Navigation provides orientation; controls appear where the action belongs. Use typography, alignment and spacing before adding containers. A border must communicate a boundary the person cares about. Routine transcript entries do not each need their own rounded rectangle.

## Be compact in appearance, generous in interaction

The current prototype confuses padded containers with comfortable touch. Reduce the amount of visible chrome, repeated labels and empty space between related facts while preserving accessible hit regions. Dense is not tiny: prose remains comfortable to read, metadata stays legible, and large text can expand the layout.

## Reveal complexity without losing information

Summarize consecutive routine activity and let people inspect it in place. Do not collapse a request for approval, an error that blocks progress, or a question awaiting an answer into an anonymous activity count. Expansion must preserve context and expose all underlying content. The summary should explain the work, not merely enumerate events.

## Make state and consequences understandable

Distinguish connecting, working, waiting for the person, disconnected and uncertain delivery. Use natural language. A spinner is not a complete explanation. Make the destination clear before sending, steering, stopping or approving. Switching hubs must not silently redirect a draft or reuse another hub’s session state.

## Preserve continuity

Returning to a session restores the reading position and draft. A streaming response must not pull someone away from earlier content they are reading. Keyboard appearance should reshape the working area smoothly. A reversible action should be easy to undo where the protocol supports it; the UI must not promise reversibility the server cannot provide.

Continuity also means retaining unfinished composition across settings, backgrounding and relaunch, and keeping decisions reachable until resolved. The [Codex/ChatGPT and Claude Code research](agent-mobile-research.md) makes these concrete design obligations: a calm surface still needs trustworthy state and a clear path to act.

## Share the product, respect the platform

One codebase should share domain behavior, hierarchy and recognizable identity. It need not force identical navigation bars, menus, sheets, back gestures or selection behavior onto both operating systems. Use native platform behavior where people already know what to expect. Material guidance and Apple’s materials are inputs, not interchangeable decoration.

## Reserve expression for meaning

Evener can have character through excellent type, precise rhythm, a distinctive accent and responsive interaction. Strong color and motion should point to what matters. A field of bright pills, floating ornaments or glass transcript cards would compete with the work.

## What “better” means

We are not claiming superiority to the reference products. Our opportunity is to combine their clarity with Evener’s difficult cases: many sessions across hubs, long tool activity, code, interrupted connections, and consequential actions. The screen studies and interaction tests must show that these cases remain understandable and pleasant. Attractive static screens alone are insufficient.

## Baseline critique

The checkpointed prototype still gives ordinary messages too much container weight. Repeated status and disclosure rows compete with the conversation. Blue secondary controls attract attention that belongs to the content. The composer and surrounding controls consume substantial space without an established hierarchy. Individual fixes have not produced a coherent typography, spacing or motion system.

The correction is a designed system tested with real content, rather than another round of arbitrary radius and padding edits.
