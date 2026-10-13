import type { SessionActivityContext, SessionDelegate } from "@evener/appwire-client";
import { conversationPaneLifetime, type PaneLifetime } from "../../shell/paneLifetime";
import { refParam } from "../../shell/routing";
import { cancelPaneFocus, type OpenPaneRecord, requestPaneFocus, workspaceStore } from "../../shell/workspace";
import { connectionStore } from "../../stores/connection";
import { sessionActivitySnapshot } from "../../stores/sessionActivity";
import { retainedTranscriptReadView } from "../session/transcript/transcriptReadView";
import { associatedCascade, cascadeOrigin, cascadeOriginRef, recordCascadeOrigin } from "./inspectionOrigin";
import {
  type CascadePath,
  type DelegateEdgeIntent,
  deriveCascadePath,
  drillZoomIntent,
  parseZoomParams,
  popZoomIntent,
  type SessionZoomParams,
} from "./intent";
import "./index";
import "../session";
import "../transcript";

const userTransitions = new WeakSet<SessionZoomParams>();

export function hasCascadeUserTransition(params: SessionZoomParams): boolean {
  return userTransitions.has(params);
}
export function clearCascadeUserTransition(params: SessionZoomParams): void {
  userTransitions.delete(params);
}

function contextFor(ref: string): SessionActivityContext | null {
  const client = connectionStore.getState().client;
  return client ? (sessionActivitySnapshot(client, ref, "session")?.context ?? null) : null;
}
function intentFor(pane: OpenPaneRecord): SessionZoomParams | null {
  if (pane.type !== "sessionZoom") return null;
  const parsed = parseZoomParams(pane.params);
  // Legacy Return retains the exact source params. Inspection uses the
  // validated read-only descriptor and leaves its separate source untouched.
  if (!parsed) return null;
  if (parsed.inspection) return parsed;
  return { ...parsed, source: (pane.params as SessionZoomParams).source };
}
function pathFor(params: SessionZoomParams): CascadePath {
  return deriveCascadePath(params, contextFor(params.ref));
}

/** The recorded edge chain that pins an authoritative ancestry to the refs the
 * path should render. deriveCascadePath honours an edge alias only through the
 * child node's delegate identity, so a scope the ancestry alone supplies — one
 * no recorded edge names — is persisted by re-encoding the whole path from the
 * ancestry, not just the recorded segment. Returns null when the ancestry
 * cannot express the chain (a node with no delegate identity), so the caller
 * reports the resume unapplied instead of a false success. */
function ancestryEdges(
  context: SessionActivityContext,
  leafRef: string,
  requested: readonly string[],
): DelegateEdgeIntent[] | null {
  const nodes: { ref: string; delegateId?: string }[] = [
    ...context.ancestors,
    { ref: leafRef, delegateId: context.delegateId },
  ];
  if (nodes.length !== requested.length) return null;
  const edges: DelegateEdgeIntent[] = [];
  for (let index = 1; index < nodes.length; index += 1) {
    const delegateId = nodes[index]?.delegateId;
    const ownerRef = requested[index - 1];
    const childRef = requested[index];
    if (typeof delegateId !== "string" || delegateId.trim() === "" || ownerRef === undefined || childRef === undefined)
      return null;
    edges.push({ ownerRef, childRef, delegateId });
  }
  return edges;
}
function requestedOwner(ref: string, ownerRef: string): boolean {
  return ref === ownerRef || contextFor(ref)?.ref === ownerRef;
}
function pruneViews(lifetime: PaneLifetime, refs: ReadonlySet<string>): void {
  for (const view of [...lifetime.readViews.values()]) {
    if (refs.has(view.requestedRef)) continue;
    if (view.role === "cascade") view.dispose();
    else view.setReadable(false);
  }
  for (const ref of lifetime.resolvedSessions.keys()) {
    if (!refs.has(ref)) lifetime.resolvedSessions.delete(ref);
  }
}
function updateIntent(pane: OpenPaneRecord, params: SessionZoomParams, userTransition = false): boolean {
  if (!workspaceStore.getState().panes.includes(pane)) return false;
  const refs = new Set(pathFor(params).scopes.map((scope) => scope.requestedRef));
  pruneViews(conversationPaneLifetime(pane), refs);
  if (userTransition) userTransitions.add(params);
  return workspaceStore.getState().retypePane(pane, "sessionZoom", params);
}

export function enterAgentCascade(sub: SessionDelegate, sourcePaneId?: string): string {
  const workspace = workspaceStore.getState();
  const source = workspace.panes.find((pane) => pane.id === (sourcePaneId ?? workspace.focusedPaneId));
  const edge = { ownerRef: sub.ownerRef, childRef: sub.childRef, delegateId: sub.delegateId };
  if (source) {
    const intent = intentFor(source);
    if (intent) {
      const owner = pathFor(intent).scopes.find((scope) => requestedOwner(scope.requestedRef, sub.ownerRef));
      if (owner) {
        updateIntent(source, drillZoomIntent(intent, { ...edge, ownerRef: owner.requestedRef }), true);
        return source.id;
      }
    }
    const ref = refParam(source.params);
    if (ref && source.type === "session" && requestedOwner(ref, sub.ownerRef)) {
      const existing = associatedCascade(source);
      if (existing) {
        const intent = intentFor(existing);
        if (!intent) throw new Error("Associated cascade has invalid intent");
        updateIntent(existing, drillZoomIntent(intent, { ...edge, ownerRef: ref }), true);
        workspaceStore.getState().focusPane(existing.id);
        return existing.id;
      }
      const params: SessionZoomParams = {
        ref: sub.childRef,
        source: { type: "session", params: { ref } },
        edges: [{ ...edge, ownerRef: ref }],
        inspection: { origin: { paneId: source.id, type: "session", ref } },
      };
      userTransitions.add(params);
      const id = workspace.openPane("sessionZoom", params, { slot: "secondary" });
      const inspector = workspaceStore.getState().panes.find((pane) => pane.id === id);
      if (!inspector) throw new Error("Opened cascade was not committed");
      recordCascadeOrigin(inspector, source);
      return id;
    }
  }
  const params: SessionZoomParams = {
    ref: sub.childRef,
    source: { type: "session", params: { ref: sub.ownerRef } },
    edges: [edge],
    inspection: { origin: null },
  };
  userTransitions.add(params);
  return workspace.openPane("sessionZoom", params, { slot: "secondary" });
}

export function popAgentCascade(paneId: string, ref: string): void {
  const pane = workspaceStore.getState().panes.find((record) => record.id === paneId);
  if (!pane) return;
  const params = intentFor(pane);
  if (!params) return;
  // The origin conversation never renders in the pane, so no surface can pop
  // the cascade to it: selecting it would leave an empty track.
  if (cascadeOriginRef(pane) === ref) return;
  const next = popZoomIntent(params, ref, pathFor(params));
  if (next !== params) updateIntent(pane, next, true);
}
export function returnFromAgentCascade(paneId: string): void {
  const workspace = workspaceStore.getState();
  const pane = workspace.panes.find((record) => record.id === paneId);
  if (!pane) return;
  const params = intentFor(pane);
  if (!params) return;
  if (params.inspection) {
    const origin = cascadeOrigin(pane);
    const originLifetime = origin ? conversationPaneLifetime(origin) : null;
    cancelPaneFocus(pane.id);
    workspace.closePane(pane.id);
    const current = origin ? workspaceStore.getState().panes.find((record) => record.id === origin.id) : undefined;
    if (current && originLifetime?.alive && conversationPaneLifetime(current) === originLifetime) {
      workspaceStore.getState().focusPane(current.id);
      requestPaneFocus(current.id);
    }
    return;
  }
  const lifetime = conversationPaneLifetime(pane);
  pruneViews(lifetime, new Set([lifetime.sourceRef]));
  retainedTranscriptReadView(lifetime, lifetime.sourceRef, lifetime.sourceType).setReadable(true);
  workspace.retypePane(pane, params.source.type, params.source.params);
}
/** Only a committed scope can fence identity. An abandoned result holds an old
 * record and cannot publish intent, even if its pane ID has been reused. */
export function reconcileCascadeBinding(expected: OpenPaneRecord, ref: string, sessionId: string): void {
  if (!workspaceStore.getState().panes.includes(expected)) return;
  const params = intentFor(expected);
  if (!params) return;
  const path = pathFor(params);
  const index = path.scopes.findIndex((scope) => scope.requestedRef === ref);
  if (index < 0) return;
  const lifetime = conversationPaneLifetime(expected);
  const previous = lifetime.resolvedSessions.get(ref);
  if (previous && previous !== sessionId) {
    const retired = new Set(path.scopes.slice(index).map((scope) => scope.requestedRef));
    for (const view of [...lifetime.readViews.values()]) if (retired.has(view.requestedRef)) view.dispose();
    for (const key of retired) lifetime.resolvedSessions.delete(key);
    const next = popZoomIntent(params, ref, path);
    updateIntent(expected, { ...next });
  }
  lifetime.resolvedSessions.set(ref, sessionId);
}

/** A store-driven resume returned a new ref for the conversation shown at
 * `ref` in this cascade. Replace the path entry in place — the resumed
 * conversation keeps its position and its drilled descendants, whose parent
 * edges follow the resumed identity — so the column re-renders on the
 * resumed ref with a fresh retained reader and the carried draft. The
 * resume driver has already hydrated the resumed ref before it publishes
 * the identity this answers.
 *
 * The retained lifetime is the frame's identity: a same-commit pair of
 * resumes (whichever applies first retypes the pane record the other
 * captured) still resolves the current record through it, while a wholesale
 * replacement (a closed pane, a reused pane id) cannot inherit the resume.
 * The whole rendered path is re-encoded, so a scope the authoritative
 * ancestry alone supplies — one no recorded edge names, as after popping
 * above the recorded segment — is pinned to the resumed ref too, and the
 * change is reported applied only once the derived path follows it.
 * Returns whether the frame applied the change, so the caller acknowledges
 * the resume only then. A resumed source conversation also moves the source
 * descriptor and the lifetime's retained source ownership, so Return
 * reopens the resumed identity instead of the superseded one. */
export function resumeCascadeScope(lifetime: PaneLifetime, ref: string, resumedRef: string): boolean {
  if (ref === resumedRef) return true;
  if (!lifetime.alive) return false;
  const current = workspaceStore.getState().panes.find((record) => record.id === lifetime.paneId);
  if (!current || conversationPaneLifetime(current) !== lifetime) return false;
  const params = intentFor(current);
  if (!params) return false;
  const context = contextFor(params.ref);
  const path = deriveCascadePath(params, context);
  const index = path.scopes.findIndex((scope) => scope.requestedRef === ref);
  if (index < 0) return false;
  // The scope keeps its position: only the rendered ref it names changes.
  const leaf = index === path.scopes.length - 1;
  const requested = path.scopes.map((scope, scopeIndex) => (scopeIndex === index ? resumedRef : scope.requestedRef));
  // An authoritative ancestry renders scopes no recorded edge names, so pin
  // them by re-encoding the whole path through the ancestry's delegate
  // identities; without a known ancestry, or when the ancestry cannot express
  // the chain, the remapped recorded edges carry every scope they name.
  const remapped = params.edges.map((edge) => ({
    ...edge,
    ownerRef: edge.ownerRef === ref ? resumedRef : edge.ownerRef,
    childRef: edge.childRef === ref ? resumedRef : edge.childRef,
  }));
  const edges = (context?.ancestryKnown ? ancestryEdges(context, params.ref, requested) : null) ?? remapped;
  const sourceResumed = params.source.type === "session" && refParam(params.source.params) === ref;
  const next: SessionZoomParams = {
    ...params,
    ref: leaf ? resumedRef : params.ref,
    // The rendered source conversation keeps rendering through its resumed
    // identity: the descriptor follows, so Return reopens the resumed
    // conversation instead of the superseded one.
    source: sourceResumed ? { ...params.source, params: { ...params.source.params, ref: resumedRef } } : params.source,
    edges,
  };
  // The acknowledgement is truthful only if the rendered path follows. A
  // rebuilt chain the derivation rejects would leave the column on the
  // superseded ref, so report the resume unapplied and let the caller retry
  // rather than clear it against a frame that never moved.
  const after = deriveCascadePath(next, contextFor(next.ref));
  const followed = leaf
    ? after.scopes[after.scopes.length - 1]?.requestedRef === resumedRef
    : after.scopes[index]?.requestedRef === resumedRef;
  if (!followed) return false;
  updateIntent(current, next);
  if (sourceResumed) lifetime.adoptResumedSource(resumedRef);
  return true;
}
