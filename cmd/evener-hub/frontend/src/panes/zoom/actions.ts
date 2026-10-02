import type { SessionActivityContext, SessionDelegate } from "@evener/appwire-client";
import { conversationPaneLifetime, type PaneLifetime } from "../../shell/paneLifetime";
import { refParam } from "../../shell/routing";
import { type OpenPaneRecord, requestPaneFocus, workspaceStore } from "../../shell/workspace";
import { connectionStore } from "../../stores/connection";
import { sessionActivitySnapshot } from "../../stores/sessionActivity";
import type { SessionPaneParams } from "../session/Session";
import { captureTranscriptView } from "../session/transcript/flow/transcriptViewRegistry";
import { retainedTranscriptReadView } from "../session/transcript/transcriptReadView";
import type { TranscriptParams } from "../transcript/Transcript";
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
  // Keep the exact runtime return descriptor, including the original params
  // object. Saved layouts have already passed the same local validator.
  return parsed ? { ...parsed, source: (pane.params as SessionZoomParams).source } : null;
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
      const lifetime = conversationPaneLifetime(source);
      const sourceView = retainedTranscriptReadView(lifetime, ref, lifetime.sourceType);
      const capture = captureTranscriptView(sourceView.id);
      if (capture) sourceView.setCapture(capture);
      sourceView.setReadable(false);
      const context = contextFor(ref);
      if (context) lifetime.resolvedSessions.set(ref, context.sessionId);
      const params: SessionZoomParams = {
        ref: sub.childRef,
        source:
          source.type === "session"
            ? { type: "session", params: source.params as SessionPaneParams }
            : { type: "transcript", params: source.params as TranscriptParams },
        edges: [{ ...edge, ownerRef: ref }],
      };
      userTransitions.add(params);
      workspace.retypePane(source, "sessionZoom", params);
      return source.id;
    }
  }
  const params: SessionZoomParams = {
    ref: sub.childRef,
    source: { type: "transcript", params: { ref: sub.ownerRef } },
    edges: [edge],
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
