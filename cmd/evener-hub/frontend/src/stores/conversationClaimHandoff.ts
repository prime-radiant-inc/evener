import type { ViewportClass } from "@evener/appwire-client";
import type { TranscriptReadView } from "../panes/session/transcript/transcriptReadView";
import { threadsStore } from "./threads";

interface Reader {
  ref: string;
  view: TranscriptReadView;
}
interface Overlap extends Reader {
  generation: number;
  sessionId: string | undefined;
  unsubscribe: () => void;
}

const readers = new Set<Reader>();
const overlaps = new Map<TranscriptReadView, Overlap>();
let generation = 0;
let destination: ViewportClass | undefined;
let mountedHost: { viewport: ViewportClass; visiblePanes: () => ReadonlySet<string> } | undefined;

/** The mounted host remains the authority, including detached Dockview tabs. */
export function registerConversationHostVisibility(
  viewport: ViewportClass,
  read: () => ReadonlySet<string>,
): () => void {
  const host = { viewport, visiblePanes: read };
  mountedHost = host;
  return () => {
    if (mountedHost === host) mountedHost = undefined;
  };
}

function forget(overlap: Overlap): boolean {
  if (overlaps.get(overlap.view) !== overlap) return false;
  overlaps.delete(overlap.view);
  overlap.unsubscribe();
  return true;
}

function release(overlap: Overlap): void {
  if (forget(overlap)) threadsStore.getState().releaseThread(overlap.ref);
}

function validate(overlap: Overlap): void {
  const sessionId = threadsStore.getState().threads.get(overlap.ref)?.imageSessionId;
  if (
    !overlap.view.alive ||
    overlap.view.requestedRef !== overlap.ref ||
    (overlap.sessionId !== undefined && sessionId !== overlap.sessionId)
  ) {
    release(overlap);
  } else if (sessionId !== undefined) {
    overlap.sessionId = sessionId;
  }
}

/** A started overlap can be adopted without waiting for a reconnect. */
export function hasConversationHandoff(ref: string, view: TranscriptReadView): boolean {
  return view.alive && overlaps.get(view)?.ref === ref;
}

/** Called only after the ordinary foreground claim has acquired ownership. */
export function registerConversationClaim(ref: string, view: TranscriptReadView): () => void {
  const reader = { ref, view };
  readers.add(reader);
  const overlap = overlaps.get(view);
  if (overlap?.ref === ref) release(overlap);
  return () => {
    readers.delete(reader);
  };
}

/** Prepare synchronously, before any subscriber can replace the outgoing host. */
export function prepareConversationHandoff(target: ViewportClass): void {
  generation += 1;
  destination = target;
  const retainedHost = mountedHost?.viewport === target;
  const presented = mountedHost?.visiblePanes();
  // A reversal transfers pending ownership, never releases and reacquires it.
  for (const overlap of overlaps.values()) {
    overlap.generation = generation;
    validate(overlap);
  }
  for (const { ref, view } of readers) {
    if (!view.alive || view.requestedRef !== ref) continue;
    const pending = overlaps.get(view);
    if (retainedHost) {
      // A batched round trip can keep this host and its ordinary readers.
      // Settle against that existing ownership now, without a remount or
      // incidental render, and do not seed another overlap for the same seat.
      if (pending?.ref === ref) release(pending);
      continue;
    }
    if (pending || (presented && !presented.has(view.paneId))) continue;
    const claim = threadsStore.getState().ensureThread(ref);
    const overlap: Overlap = {
      ref,
      view,
      generation,
      sessionId: threadsStore.getState().threads.get(ref)?.imageSessionId,
      unsubscribe: () => {},
    };
    overlaps.set(view, overlap);
    const unsubscribeView = view.subscribe(() => validate(overlap));
    const unsubscribeThread = threadsStore.subscribe(() => validate(overlap));
    overlap.unsubscribe = () => {
      unsubscribeView();
      unsubscribeThread();
    };
    // A rejected ensure owns no hold to release, only its own registration.
    void claim.catch(() => forget(overlap));
  }
}

export function conversationHandoffGeneration(target: ViewportClass): number | undefined {
  return destination === target ? generation : undefined;
}

/** Host policy may reject panes before their lazy contents have resolved. */
export function restrictConversationHandoff(token: number | undefined, paneIds: ReadonlySet<string>): void {
  for (const overlap of overlaps.values()) {
    if (overlap.generation === token && !paneIds.has(overlap.view.paneId)) release(overlap);
  }
}

/** Resolved contents have committed their ordinary claims, including cascade scopes. */
export function completeConversationHandoffPane(token: number | undefined, paneId: string): void {
  for (const overlap of overlaps.values()) {
    if (overlap.generation === token && overlap.view.paneId === paneId) release(overlap);
  }
}

export function cancelConversationHandoff(token: number | undefined): void {
  restrictConversationHandoff(token, new Set());
}
