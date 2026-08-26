export interface CapturedTranscriptView {
  readonly anchorId?: string;
  readonly anchorOffset: number;
  readonly normalizedOffset: number;
  readonly followingBottom: boolean;
  readonly focusedEntryId?: string;
}

export interface RegisteredTranscriptView {
  id: string;
  /** Optional layout identity used only for deterministic host-remount reuse. */
  layout?: string;
  capture(): CapturedTranscriptView;
  restore(captured: CapturedTranscriptView): void;
  focusDetailTrigger(): void;
  announce(summary: string): void;
}

interface Registration {
  readonly view: RegisteredTranscriptView;
}

interface RemountCapture {
  readonly targetLayout: string;
  readonly captured: CapturedTranscriptView;
}

export interface TranscriptViewTransitionOptions {
  /** Fingerprint of the effective configuration after the publish. */
  readonly fingerprint?: string;
  /** Layout the view is entering; enables deterministic host-remount reuse. */
  readonly targetLayout?: string;
  /** Capture even when the effective fingerprint is unchanged (breakpoints). */
  readonly force?: boolean;
  /** Arm deterministic host-remount reuse for a viewport transition only. */
  readonly prepareRemount?: boolean;
  /** Override the default fingerprint-based announcement decision. */
  readonly announce?: boolean;
}

const registeredViews = new Map<string, Registration>();
const preparedRemounts = new Map<string, RemountCapture>();
const remountCaptures = new Map<string, RemountCapture>();
let hasLastTransitionFingerprint = false;
let lastTransitionFingerprint: string | undefined;

export function registerTranscriptView(view: RegisteredTranscriptView): () => void {
  const id = view.id;
  const registration: Registration = { view };
  registeredViews.set(id, registration);

  const remount = remountCaptures.get(id);
  if (remount) {
    remountCaptures.delete(id);
    if (view.layout === undefined || view.layout === remount.targetLayout) {
      try {
        view.restore(remount.captured);
      } catch {
        // A remounted pane may still be completing its own mount. The regular
        // measurement callback gets another chance to restore its pending view.
      }
    }
  }
  const prepared = preparedRemounts.get(id);
  if (prepared && view.layout !== undefined && view.layout !== prepared.targetLayout) {
    preparedRemounts.delete(id);
  }

  return () => {
    if (registeredViews.get(id) === registration) {
      const prepared = preparedRemounts.get(id);
      if (prepared) {
        preparedRemounts.delete(id);
        remountCaptures.set(id, prepared);
      }
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

/** Arm captured panes for an upcoming viewport host remount. */
export function prepareTranscriptViewRemount(
  captured: ReadonlyMap<string, CapturedTranscriptView>,
  targetLayout: string,
): void {
  for (const [id, capturedView] of captured) {
    preparedRemounts.set(id, { targetLayout, captured: capturedView });
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

/**
 * Runs one effective-view transition in the required order. The publish
 * callback is intentionally synchronous: Zustand state and browser events
 * must not get ahead of the snapshot. A registered view's restore callback
 * records work for its next measurement callback; it does not assume the new
 * rows are measurable yet.
 */
export function transitionTranscriptViews(
  publish: () => void,
  summary: string,
  options?: TranscriptViewTransitionOptions,
): void {
  const fingerprint = options?.fingerprint;
  const fingerprintChanged =
    fingerprint === undefined || !hasLastTransitionFingerprint || fingerprint !== lastTransitionFingerprint;
  const shouldCapture = options?.force === true || fingerprintChanged;
  const shouldAnnounce = options?.announce ?? fingerprintChanged;

  if (options?.targetLayout !== undefined) {
    for (const captures of [preparedRemounts, remountCaptures]) {
      for (const [id, remount] of captures) {
        if (remount.targetLayout !== options.targetLayout) captures.delete(id);
      }
    }
  }

  const captured = shouldCapture ? captureTranscriptViews() : new Map<string, CapturedTranscriptView>();
  if (shouldCapture && options?.prepareRemount && options.targetLayout !== undefined) {
    prepareTranscriptViewRemount(captured, options.targetLayout);
  }
  let published = false;
  try {
    publish();
    published = true;
  } finally {
    if (shouldCapture) restoreTranscriptViews(captured);
  }

  if (published && shouldAnnounce) announceTranscriptViews(summary);
  if (fingerprint !== undefined) {
    hasLastTransitionFingerprint = true;
    lastTransitionFingerprint = fingerprint;
  }
}

export function resetTranscriptViewRegistryForTests(): void {
  registeredViews.clear();
  preparedRemounts.clear();
  remountCaptures.clear();
  hasLastTransitionFingerprint = false;
  lastTransitionFingerprint = undefined;
}
