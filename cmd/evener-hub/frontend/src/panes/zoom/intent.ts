import { asJsonObject, type SessionActivityContext } from "@evener/appwire-client";
import type { SessionPaneParams } from "../session/Session";
import type { TranscriptParams } from "../transcript/Transcript";

export interface DelegateEdgeIntent {
  ownerRef: string;
  childRef: string;
  delegateId: string;
}
export type ConversationReturnDescriptor =
  | { type: "session"; params: SessionPaneParams }
  | { type: "transcript"; params: TranscriptParams };
export interface CascadeReturnOrigin {
  paneId: string;
  type: "session" | "transcript";
  ref: string;
}
export interface SessionZoomParams {
  ref: string;
  source: ConversationReturnDescriptor;
  edges: DelegateEdgeIntent[];
  inspection?: { origin: CascadeReturnOrigin | null };
}
export interface CascadeScope {
  requestedRef: string;
  sessionId?: string;
  title: string;
}
export interface CascadePath {
  scopes: CascadeScope[];
  ancestryKnown: boolean;
}

function object(value: unknown): Record<string, unknown> | null {
  return asJsonObject(value) ?? null;
}
function sessionRef(value: unknown): value is string {
  return typeof value === "string" && value.trim() !== "" && !value.startsWith("job:");
}
function edgeSegment(value: unknown, leaf: string): DelegateEdgeIntent[] {
  if (!Array.isArray(value)) return [];
  const edges: DelegateEdgeIntent[] = [];
  const visited = new Set<string>();
  let previousChild: string | undefined;
  for (const candidate of value) {
    const edge = object(candidate);
    if (
      !edge ||
      !sessionRef(edge.ownerRef) ||
      !sessionRef(edge.childRef) ||
      typeof edge.delegateId !== "string" ||
      edge.delegateId.trim() === ""
    )
      return [];
    if (previousChild === undefined) visited.add(edge.ownerRef);
    else if (edge.ownerRef !== previousChild) return [];
    if (visited.has(edge.childRef)) return [];
    visited.add(edge.childRef);
    edges.push({ ownerRef: edge.ownerRef, childRef: edge.childRef, delegateId: edge.delegateId });
    previousChild = edge.childRef;
  }
  return previousChild === undefined || previousChild === leaf ? edges : [];
}

function inspectionOrigin(value: unknown, sourceRef: string): CascadeReturnOrigin | null {
  const raw = object(object(value)?.origin);
  if (
    !raw ||
    typeof raw.paneId !== "string" ||
    raw.paneId.trim() === "" ||
    (raw.type !== "session" && raw.type !== "transcript") ||
    !sessionRef(raw.ref) ||
    raw.ref !== sourceRef
  )
    return null;
  return { paneId: raw.paneId, type: raw.type, ref: raw.ref };
}

export function parseZoomParams(value: unknown): SessionZoomParams | null {
  const raw = object(value);
  if (!raw || !sessionRef(raw.ref)) return null;
  const source = object(raw.source);
  const params = object(source?.params);
  if (!source || !params || !sessionRef(params.ref)) return null;
  let descriptor: ConversationReturnDescriptor;
  if (source.type === "session") descriptor = { type: "session", params: { ref: params.ref } };
  else if (source.type === "transcript") {
    if (params.parentRef !== undefined && (typeof params.parentRef !== "string" || params.parentRef.trim() === ""))
      return null;
    descriptor = {
      type: "transcript",
      params: { ref: params.ref, ...(params.parentRef === undefined ? {} : { parentRef: params.parentRef }) },
    };
  } else return null;
  const edges = edgeSegment(raw.edges, raw.ref);
  if (!Object.hasOwn(raw, "inspection")) return { ref: raw.ref, source: descriptor, edges };
  return {
    ref: raw.ref,
    source: { type: "transcript", params: descriptor.params },
    edges,
    inspection: { origin: inspectionOrigin(raw.inspection, descriptor.params.ref) },
  };
}

export function deriveCascadePath(params: SessionZoomParams, context: SessionActivityContext | null): CascadePath {
  if (!context?.ancestryKnown) {
    const edges = edgeSegment(params.edges, params.ref);
    const first = edges[0];
    const refs = first ? [first.ownerRef, ...edges.map((edge) => edge.childRef)] : [params.ref];
    return { ancestryKnown: false, scopes: refs.map((requestedRef) => ({ requestedRef, title: requestedRef })) };
  }
  const nodes = [
    ...context.ancestors,
    { ref: context.ref, sessionId: context.sessionId, delegateId: context.delegateId, title: params.ref },
  ];
  const scopes: CascadeScope[] = nodes.map((node, index) => ({
    requestedRef: index === nodes.length - 1 ? params.ref : node.ref,
    sessionId: node.sessionId,
    title: node.title || node.ref,
  }));
  // Only an authoritative delegation can carry a requested alias onto its
  // canonical ancestor. Cached edges never invent or extend the full path.
  let index = nodes.length - 1;
  for (const edge of edgeSegment(params.edges, params.ref).reverse()) {
    const node = nodes[index];
    const parent = nodes[index - 1];
    const parentScope = scopes[index - 1];
    if (!node || !parent || !parentScope) break;
    if (node.delegateId === edge.delegateId) parentScope.requestedRef = edge.ownerRef;
    else if (node.ref !== edge.childRef || parent.ref !== edge.ownerRef) break;
    index -= 1;
  }
  return { ancestryKnown: true, scopes };
}

export function drillZoomIntent(params: SessionZoomParams, edge: DelegateEdgeIntent): SessionZoomParams {
  const refs = deriveCascadePath(params, null).scopes.map((scope) => scope.requestedRef);
  const owner = refs.indexOf(edge.ownerRef);
  const edges = [...(owner < 0 ? [] : params.edges.slice(0, owner)), edge];
  if (edgeSegment(edges, edge.childRef).length !== edges.length) return params;
  return { ...params, ref: edge.childRef, edges };
}

export function popZoomIntent(params: SessionZoomParams, ref: string, path: CascadePath): SessionZoomParams {
  if (!path.scopes.some((scope) => scope.requestedRef === ref) || params.ref === ref) return params;
  const edge = params.edges.findIndex((item) => item.childRef === ref);
  return { ...params, ref, edges: edge < 0 ? [] : params.edges.slice(0, edge + 1) };
}
