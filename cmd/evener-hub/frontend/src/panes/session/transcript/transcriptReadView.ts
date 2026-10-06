import type { PaneLifetime } from "../../../shell/paneLifetime";
import { type CapturedTranscriptView, captureTranscriptView } from "./flow/transcriptViewRegistry";

export type TranscriptReadRole = "session" | "transcript" | "cascade";
export interface TranscriptReadView {
  readonly id: string;
  readonly paneId: string;
  readonly requestedRef: string;
  readonly role: TranscriptReadRole;
  readonly alive: boolean;
  readonly readable: boolean;
  readonly positioningRevision: number;
  supersedePositioning(): void;
  syncPositioningMovement(beforeOffset: number): void;
  subscribe(listener: (beforeOffset?: number) => void): () => void;
  getCapture(): CapturedTranscriptView | undefined;
  setCapture(value: CapturedTranscriptView): void;
  setReadable(value: boolean): void;
  dispose(): void;
}

export function retainedTranscriptReadView(
  lifetime: PaneLifetime,
  ref: string,
  role: TranscriptReadRole,
): TranscriptReadView {
  const key = JSON.stringify([ref, role]);
  const existing = lifetime.readViews.get(key);
  if (existing?.alive) return existing;
  let alive = lifetime.alive;
  let readable = false;
  let capture: CapturedTranscriptView | undefined;
  let positioningRevision = 0;
  const listeners = new Set<(beforeOffset?: number) => void>();
  const publish = (beforeOffset?: number) => {
    for (const listener of listeners) listener(beforeOffset);
  };
  const view: TranscriptReadView = {
    id: JSON.stringify(["read", lifetime.paneId, lifetime.serial, ref, role]),
    paneId: lifetime.paneId,
    requestedRef: ref,
    role,
    get alive() {
      return alive;
    },
    get readable() {
      return alive && readable;
    },
    get positioningRevision() {
      return positioningRevision;
    },
    supersedePositioning() {
      if (!alive) return;
      positioningRevision += 1;
      capture = undefined;
      publish();
    },
    syncPositioningMovement(beforeOffset) {
      if (!alive || !Number.isFinite(beforeOffset)) return;
      publish(beforeOffset);
    },
    subscribe(listener) {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
    getCapture: () => capture,
    setCapture: (value) => {
      if (alive && (value.positioningRevision === undefined || value.positioningRevision === positioningRevision))
        capture = value;
    },
    setReadable(value) {
      if (!alive || readable === value) return;
      if (!value) capture = captureTranscriptView(view.id) ?? capture;
      readable = value;
      publish();
    },
    dispose() {
      if (!alive) return;
      alive = false;
      readable = false;
      positioningRevision += 1;
      capture = undefined;
      publish();
      listeners.clear();
      if (lifetime.readViews.get(key) === view) lifetime.readViews.delete(key);
    },
  };
  if (alive) lifetime.readViews.set(key, view);
  return view;
}
