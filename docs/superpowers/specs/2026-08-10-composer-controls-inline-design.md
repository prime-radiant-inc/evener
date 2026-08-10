# Inline Composer Controls Design

## Goal

Move the four controls currently shown in the session footer status row into the composer control row beside the paperclip attachment button:

- model selector;
- reasoning-effort selector;
- context-usage meter; and
- session-actions overflow menu.

This is a layout refactor. Existing state, store actions, keyboard behavior, menu actions, and panel-opening behavior remain unchanged.

## Structure

The session chrome will support an embedded presentation for the composer. The embedded presentation will reuse the existing control implementations and render one compact leading cluster for the composer. `Composer` will place the cluster next to its paperclip control through `PromptCard`'s leading controls slot.

The separate footer presentation of those four controls will be removed, preventing duplicate controls and preserving a single DOM location and tab order. Existing details, tasks, and activity panels remain mounted and continue to be opened by the same session-actions menu callbacks.

Cadence and goal semantics are not part of the four requested controls. They remain outside the relocated cluster unless an existing responsive state already hides them; their current behavior is not otherwise changed.

## Layout and responsive behavior

The composer leading row will preserve this order:

1. paperclip attachment button;
2. model selector;
3. reasoning-effort selector;
4. context meter; and
5. session-actions overflow menu.

The implementation will reuse the existing compact trigger styles and container-query compression. It will not use fixed-position or CSS-only visual reordering. Model text may ellipsize at narrow widths, the meter keeps its existing compact representation, and native control hit targets remain usable. The composer action cluster containing Send, Stop, and Steer keeps its existing trailing alignment and behavior.

## Behavior and accessibility

The model and reasoning-effort controls remain native interactive controls with their existing accessible names and change handlers. The session-actions menu retains its `Session actions` accessible label and existing Details/Tasks/Activity, rename, organization, and shutdown actions. The paperclip remains the first attachment action in DOM order.

No network, reducer, persistence, or command-routing behavior changes are required.

## Verification

Focused tests will assert that the four controls render in the composer row and that the footer no longer renders duplicate copies. Existing Composer, SessionChrome, and StatusRow tests will be updated only where their layout contract changes. Relevant desktop and compact layoutguard assertions will be updated to reflect the new row placement.

The frontend verification gates are:

- run `npx biome check --write` on touched files under `src/`;
- run `make test-web`;
- run `make test-web-browser` when Chrome is available.

The implementation must keep all tests deterministic and must not add provider or network dependencies.
