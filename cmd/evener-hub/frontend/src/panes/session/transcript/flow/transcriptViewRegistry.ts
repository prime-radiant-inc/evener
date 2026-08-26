export interface CapturedTranscriptView {
  readonly anchorId?: string;
  readonly anchorOffset: number;
  readonly normalizedOffset: number;
  readonly followingBottom: boolean;
  readonly focusedEntryId?: string;
}

export interface RegisteredTranscriptView {
  id: string;
  capture(): CapturedTranscriptView;
  restore(captured: CapturedTranscriptView): void;
  focusDetailTrigger(): void;
  announce(summary: string): void;
}

interface Registration {
  readonly view: RegisteredTranscriptView;
}

const registeredViews = new Map<string, Registration>();

export function registerTranscriptView(view: RegisteredTranscriptView): () => void {
  const id = view.id;
  const registration: Registration = { view };
  registeredViews.set(id, registration);

  return () => {
    if (registeredViews.get(id) === registration) {
      registeredViews.delete(id);
    }
  };
}

export function captureTranscriptViews(): ReadonlyMap<string, CapturedTranscriptView> {
  const captured = new Map<string, CapturedTranscriptView>();
  const currentViews = [...registeredViews.entries()];

  for (const [id, registration] of currentViews) {
    try {
      captured.set(id, registration.view.capture());
    } catch {
      // A pane may disappear while a transition is capturing its view. Keep
      // the other panes' snapshots available to the transition.
    }
  }

  return captured;
}

export function restoreTranscriptViews(captured: ReadonlyMap<string, CapturedTranscriptView>): void {
  for (const [id, capturedView] of captured) {
    const registration = registeredViews.get(id);
    if (!registration) continue;

    try {
      registration.view.restore(capturedView);
    } catch {
      // A stale or unmounted pane must not prevent other panes from restoring.
    }
  }
}

export function announceTranscriptViews(summary: string): void {
  const currentViews = [...registeredViews.values()];

  for (const registration of currentViews) {
    try {
      registration.view.announce(summary);
    } catch {
      // Announcements are best-effort and isolated per pane.
    }
  }
}

export function resetTranscriptViewRegistryForTests(): void {
  registeredViews.clear();
}
