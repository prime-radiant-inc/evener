import {
  retainedTranscriptReadView,
  type TranscriptReadRole,
  type TranscriptReadView,
} from "../panes/session/transcript/transcriptReadView";
import { parseZoomParams } from "../panes/zoom/intent";
import { conversationPaneLifetime } from "./paneLifetime";
import { refParam } from "./routing";
import {
  cancelCarriedComposerSeat,
  type OpenPaneRecord,
  onPaneRetype,
  type PaneSlot,
  presentedPanes,
  substituteAttentionPane,
  workspaceStore,
} from "./workspace";

export interface AttentionPlacementEntry {
  pane: OpenPaneRecord;
  lifetimeSerial: number | null;
  requestedRef: string | null;
  readViewId: string | null;
  seatId: string;
}

interface Seat {
  id: string;
  slot: Exclude<PaneSlot, "parked">;
  occupant: OpenPaneRecord;
  // Transient host handoff, not a saved grid or a second geometry owner.
  anchor: OpenPaneRecord | null;
}
interface Binding {
  pane: OpenPaneRecord;
  revision: number;
  seat: Seat;
}
const bindings = new WeakMap<OpenPaneRecord, Binding>();
const retypeListeners = new Set<(previous: OpenPaneRecord, replacement: OpenPaneRecord) => void>();
const entries = new WeakMap<
  AttentionPlacementEntry,
  { seat: Seat; revision: number; view: TranscriptReadView | null }
>();
let nextSeat = 0;

function readerIdentity(pane: OpenPaneRecord): { ref: string; role: TranscriptReadRole } | null {
  if (pane.type !== "session" && pane.type !== "sessionZoom" && pane.type !== "transcript") return null;
  const ref = refParam(pane.params);
  if (!ref || ref.startsWith("job:")) return null;
  if (pane.type !== "sessionZoom") return { ref, role: pane.type };
  const params = parseZoomParams(pane.params);
  if (!params) return null;
  return { ref, role: ref === refParam(params.source.params) ? params.source.type : "cascade" };
}

function bindingFor(pane: OpenPaneRecord): Binding {
  let binding = bindings.get(pane);
  if (!binding) {
    binding = {
      pane,
      revision: 0,
      seat: {
        id: `attention-seat-${++nextSeat}`,
        slot: pane.slot === "main" ? "main" : "secondary",
        occupant: pane,
        anchor: null,
      },
    };
    bindings.set(pane, binding);
  }
  return binding;
}

function capture(pane: OpenPaneRecord): AttentionPlacementEntry {
  const binding = bindingFor(pane);
  if (pane.slot !== "parked") binding.seat.slot = pane.slot;
  const reader = readerIdentity(pane);
  const lifetime = reader ? conversationPaneLifetime(pane) : null;
  const view = reader && lifetime ? retainedTranscriptReadView(lifetime, reader.ref, reader.role) : null;
  const entry: AttentionPlacementEntry = {
    // Retype moves this exact lifetime, never resolves by a reusable ID/ref.
    get pane() {
      return binding.pane;
    },
    lifetimeSerial: lifetime?.serial ?? null,
    requestedRef: reader?.ref ?? null,
    readViewId: view?.id ?? null,
    seatId: binding.seat.id,
  };
  entries.set(entry, { seat: binding.seat, revision: binding.revision, view });
  return entry;
}

export function captureAttentionPlacement(): AttentionPlacementEntry | null {
  const state = workspaceStore.getState();
  const pane = presentedPanes(state.panes).find((pane) => pane.id === state.focusedPaneId);
  return pane ? capture(pane) : null;
}

function substitute(target: OpenPaneRecord | string, seat: Seat): OpenPaneRecord {
  const panes = workspaceStore.getState().panes;
  const occupant = seat.occupant;
  const source =
    panes.includes(occupant) && occupant.slot !== "parked" && bindings.get(occupant)?.seat === seat ? occupant : null;
  if (source) {
    seat.slot = source.slot === "main" ? "main" : "secondary";
    // Multiple hops may publish before the host runs. Keep the first actual
    // panel anchor until DockHost consumes it rather than anchoring to a panel
    // that has never existed.
    seat.anchor ??= source;
  }
  return substituteAttentionPane(target, source, seat.slot, (pane) => {
    const binding = bindingFor(pane);
    binding.seat = seat;
    seat.occupant = pane;
  });
}

export function presentAttentionTarget(targetRef: string): AttentionPlacementEntry | null {
  const state = workspaceStore.getState();
  const matches = state.panes.filter((pane) => readerIdentity(pane)?.ref === targetRef);
  const presented = matches.find((pane) => pane.slot !== "parked");
  if (presented) {
    // Focusing the pane that already presents the conversation is the
    // placement decision: a pending carry dies with it.
    cancelCarriedComposerSeat(targetRef);
    state.focusPane(presented.id);
    return capture(presented);
  }
  const origin = captureAttentionPlacement();
  if (!origin) {
    state.replacePrimary("session", { ref: targetRef });
    return captureAttentionPlacement();
  }
  const seat = bindingFor(origin.pane).seat;
  return capture(substitute(matches[0] ?? targetRef, seat));
}

export function attentionPlacementIsValid(entry: AttentionPlacementEntry): boolean {
  const state = workspaceStore.getState();
  const saved = entries.get(entry);
  const pane = entry.pane;
  const binding = bindings.get(pane);
  if (!saved || !binding || !state.panes.includes(pane) || saved.revision !== binding.revision) return false;
  const reader = readerIdentity(pane);
  if (entry.lifetimeSerial !== null) {
    const lifetime = conversationPaneLifetime(pane);
    const view = reader ? lifetime.readViews.get(JSON.stringify([reader.ref, reader.role])) : undefined;
    if (
      !lifetime.alive ||
      lifetime.serial !== entry.lifetimeSerial ||
      reader?.ref !== entry.requestedRef ||
      !view?.alive ||
      view !== saved.view ||
      view.id !== entry.readViewId
    )
      return false;
  } else if (reader !== null) return false;
  return true;
}

export function restoreAttentionPlacement(entry: AttentionPlacementEntry): boolean {
  if (!attentionPlacementIsValid(entry)) return false;
  const state = workspaceStore.getState();
  const pane = entry.pane;
  const saved = entries.get(entry);
  if (!saved) return false;
  if (pane.slot !== "parked") state.focusPane(pane.id);
  else substitute(pane, saved.seat);
  return true;
}

export function releaseUnreferencedAttentionPanes(retained: ReadonlySet<OpenPaneRecord>): void {
  const state = workspaceStore.getState();
  const panes = state.panes.filter((pane) => pane.slot !== "parked" || retained.has(pane));
  if (panes.length !== state.panes.length) workspaceStore.setState({ panes });
}

/** Called after an in-place retype has moved a pane's lifetime to its replacement. */
export function onAttentionPaneRetype(listener: (previous: OpenPaneRecord, replacement: OpenPaneRecord) => void): void {
  retypeListeners.add(listener);
}

// A drill inside a cascade keeps the same pane and lifetime, so an entry that
// owns that pane still describes the reader - it just reads a different leaf.
// Re-capturing keeps the entry valid instead of letting its route fall back to
// ordinary placement, which would reopen the pre-drill reader beside it.
export function refreshAttentionPlacement(entry: AttentionPlacementEntry): boolean {
  const binding = bindings.get(entry.pane);
  if (!binding) return false;
  const reader = readerIdentity(binding.pane);
  const lifetime = reader ? conversationPaneLifetime(binding.pane) : null;
  const view = reader && lifetime ? retainedTranscriptReadView(lifetime, reader.ref, reader.role) : null;
  entry.lifetimeSerial = lifetime?.serial ?? null;
  entry.requestedRef = reader?.ref ?? null;
  entry.readViewId = view?.id ?? null;
  entries.set(entry, { seat: binding.seat, revision: binding.revision, view });
  return true;
}

// The host uses only this one-seat transition, then clears it. It inserts the
// destination before removing the source so a sole-panel group stays alive.
export function attentionPanelAnchor(pane: OpenPaneRecord): OpenPaneRecord | null {
  return bindings.get(pane)?.seat.anchor ?? null;
}
export function attentionPanelApplied(pane: OpenPaneRecord): void {
  const seat = bindings.get(pane)?.seat;
  if (seat) seat.anchor = null;
}

onPaneRetype((previous, replacement) => {
  const binding = bindings.get(previous);
  if (!binding) return;
  if (JSON.stringify(readerIdentity(previous)) !== JSON.stringify(readerIdentity(replacement))) binding.revision += 1;
  binding.pane = replacement;
  if (binding.seat.occupant === previous) binding.seat.occupant = replacement;
  if (binding.seat.anchor === previous) binding.seat.anchor = replacement;
  bindings.delete(previous);
  bindings.set(replacement, binding);
  for (const listener of retypeListeners) listener(previous, replacement);
});
