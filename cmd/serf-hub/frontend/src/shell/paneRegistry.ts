// paneRegistry maps pane-type ids to lazily-loaded pane components. Each
// pane type registers itself by calling registerPane() once, as a top-level
// side effect of importing its own module (see src/panes/welcome/index.tsx)
// - the registry itself never imports a pane module directly, so adding a
// pane type never means editing this file.
import type { ComponentType, LazyExoticComponent } from "react";

export type PaneTypeId = "session" | "transcript" | "doc" | "spawn" | "settings" | "welcome";

// Context passed to PaneDescriptor.title() alongside a pane's own params -
// e.g. a thread ref -> display name lookup for session/transcript panes.
// Empty for now: the only pane registered in this task (welcome) has a
// constant title that needs no lookup. Record<string, never> (rather than
// an empty `interface {}`, which is equivalent to `unknown` and trips
// @typescript-eslint/no-empty-object-type) types "no fields yet" precisely;
// add fields here as soon as a pane's title() actually needs them (YAGNI).
export type PaneTitleCtx = Record<string, never>;

export interface PaneProps<P = unknown> {
  params: P;
  paneId: string;
  focused: boolean;
}

export interface PaneDescriptor<P = unknown> {
  id: PaneTypeId;
  title(params: P, ctx: PaneTitleCtx): string;
  component: LazyExoticComponent<ComponentType<PaneProps<P>>>;
  singleton?: boolean; // settings/spawn: focus existing instead of second copy
}

// Keyed by PaneTypeId; values are erased to PaneDescriptor<unknown> because
// a single Map can't otherwise hold a different P per entry. registerPane's
// own generic signature is the type-safe boundary - each call site supplies
// its own concrete P, matched against its own `id` and `component` shape.
const registry = new Map<PaneTypeId, PaneDescriptor<unknown>>();

export function registerPane<P>(descriptor: PaneDescriptor<P>): void {
  registry.set(descriptor.id, descriptor as PaneDescriptor<unknown>);
}

export function paneFor(id: PaneTypeId): PaneDescriptor<unknown> {
  const descriptor = registry.get(id);
  if (!descriptor) {
    throw new Error(`paneFor: no pane registered for type "${id}"`);
  }
  return descriptor;
}
