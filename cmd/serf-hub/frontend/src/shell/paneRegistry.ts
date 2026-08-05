// paneRegistry maps pane-type ids to lazily-loaded pane components. Each
// pane type registers itself by calling registerPane() once, as a top-level
// side effect of importing its own module (see src/panes/welcome/index.tsx)
// - the registry itself never imports a pane module directly, so adding a
// pane type never means editing this file.
import type { ComponentType, LazyExoticComponent } from "react";

export type PaneTypeId =
  | "session"
  | "transcript"
  | "doc"
  | "sessionTasks"
  | "sessionActivity"
  | "sessionDetails"
  | "spawn"
  | "settings"
  | "welcome";

// Context passed to PaneDescriptor.title() alongside a pane's own params -
// e.g. a thread ref -> display name lookup for session/transcript panes.
// threadName is the first (and so far only) field: DockHost calls title()
// with a ctx backed by the threads store, so a session pane's tab title
// tracks the live ThreadModel.name (falling back to the ref itself when the
// thread hasn't hydrated a name yet, or isn't tracked at all) instead of a
// static ref string. Optional - a pane whose title doesn't need a lookup
// (welcome's is constant) can be called with `{}`, as the existing welcome
// pane test already does - add more fields here, optional or not, only when
// another pane's title() needs a different kind of lookup (YAGNI).
export interface PaneTitleCtx {
  threadName?(ref: string): string | undefined;
}

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
