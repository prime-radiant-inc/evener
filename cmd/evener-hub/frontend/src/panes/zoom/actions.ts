import type { SessionActivityContext, SessionDelegate } from "@evener/appwire-client";
import { conversationPaneLifetime, type PaneLifetime } from "../../shell/paneLifetime";
import { refParam } from "../../shell/routing";
import { cancelPaneFocus, type OpenPaneRecord, requestPaneFocus, workspaceStore } from "../../shell/workspace";
import { connectionStore } from "../../stores/connection";
import { sessionActivitySnapshot } from "../../stores/sessionActivity";
import { retainedTranscriptReadView } from "../session/transcript/transcriptReadView";
import type { TranscriptParams } from "../transcript/Transcript";
import { associatedCascade, cascadeOrigin, recordCascadeOrigin } from "./inspectionOrigin";
import {
  type CascadePath,
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
    if (
      ref &&
      (source.type === "session" || (source.type === "transcript" && !ref.startsWith("job:"))) &&
      requestedOwner(ref, sub.ownerRef)
    ) {
      const existing = associatedCascade(source);
      if (existing) {
        const intent = intentFor(existing);
        if (!intent) throw new Error("Associated cascade has invalid intent");
        updateIntent(existing, drillZoomIntent(intent, { ...edge, ownerRef: ref }), true);
        workspaceStore.getState().focusPane(existing.id);
        return existing.id;
      }
      const sourceParams = source.params as TranscriptParams;
      const params: SessionZoomParams = {
        ref: sub.childRef,
        source: {
          type: "transcript",
          params: {
            ref,
            ...(source.type === "transcript" && sourceParams.parentRef !== undefined
              ? { parentRef: sourceParams.parentRef }
              : {}),
          },
        },
        edges: [{ ...edge, ownerRef: ref }],
        inspection: { origin: { paneId: source.id, type: source.type, ref } },
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
    source: { type: "transcript", params: { ref: sub.ownerRef } },
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
export function openCascadeConversation(paneId: string, ref: string): void {
  const workspace = workspaceStore.getState();
  const pane = workspace.panes.find((record) => record.id === paneId);
  if (!pane) return;
  const params = intentFor(pane);
  if (!params) return;
  const origin = params.inspection ? cascadeOrigin(pane) : null;
  if (origin?.type === "session" && refParam(origin.params) === ref) {
    returnFromAgentCascade(paneId);
    return;
  }
  if (params.source.type === "session" && params.source.params.ref === ref) {
    returnFromAgentCascade(paneId);
    workspace.focusPane(paneId);
    requestPaneFocus(paneId);
    return;
  }
  const existing = workspace.panes.find((record) => record.type === "session" && refParam(record.params) === ref);
  const id = existing?.id ?? workspace.openPane("session", { ref }, { slot: "secondary" });
  workspace.focusPane(id);
  requestPaneFocus(id);
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
