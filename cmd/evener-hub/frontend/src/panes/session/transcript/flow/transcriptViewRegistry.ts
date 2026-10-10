export interface CapturedTranscriptView {
  readonly anchorId?: string;
  readonly anchorOffset: number;
  readonly normalizedOffset: number;
  readonly followingBottom: boolean;
  readonly focusedEntryId?: string;
  readonly readingPoint?: {
    readonly entryHeight: number;
    readonly viewportHeight: number;
    readonly viewportWidth: number;
  };
  readonly positioningRevision?: number;
}

export interface RegisteredTranscriptView {
  id: string;
  /** Session identity, separate from a reusable pane id. */
  logicalRef?: string;
  /** Optional layout identity used only for deterministic host-remount reuse. */
  layout?: string;
  capture(): CapturedTranscriptView;
  restore(captured: CapturedTranscriptView): void;
  announce(summary: string): void;
}

interface Registration {
  readonly view: RegisteredTranscriptView;
}

interface RemountCapture {
  readonly targetLayout: string;
  readonly captured: CapturedTranscriptView;
  readonly logicalRef?: string;
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
const mountedOwners = new Map<string, { logicalRef: string }>();
const contentReloadCaptures = new Map<string, { logicalRef: string; captured: CapturedTranscriptView }>();
let hasLastTransitionFingerprint = false;
let lastTransitionFingerprint: string | undefined;

/** A placement lifetime, not a thread/subscription claim or scroll history. */
export function retainTranscriptView(id: string, logicalRef: string, keepRemount?: () => boolean): () => void {
  const owner = { logicalRef };
  mountedOwners.set(id, owner);
  const reload = contentReloadCaptures.get(id);
  if (reload && reload.logicalRef !== logicalRef) contentReloadCaptures.delete(id);
  return () => {
    if (mountedOwners.get(id) !== owner) return;
    mountedOwners.delete(id);
    contentReloadCaptures.delete(id);
    const pendingRemount = preparedRemounts.get(id) ?? remountCaptures.get(id);
    if (!pendingRemount || pendingRemount.logicalRef !== logicalRef || keepRemount?.() === false) {
      preparedRemounts.delete(id);
      remountCaptures.delete(id);
    }
  };
}

export function retainedTranscriptCapture(id: string, logicalRef?: string): CapturedTranscriptView | undefined {
  const reload = contentReloadCaptures.get(id);
  if (reload && reload.logicalRef === logicalRef) return reload.captured;
  const remount = remountCaptures.get(id);
  return remount?.logicalRef === logicalRef ? remount?.captured : undefined;
}

export function completeTranscriptViewRestore(id: string, logicalRef?: string): void {
  const registration = registeredViews.get(id);
  const prepared = preparedRemounts.get(id);
  if (prepared && prepared.logicalRef === logicalRef && prepared.targetLayout === registration?.view.layout) {
    preparedRemounts.delete(id);
  }
}

export function registerTranscriptView(view: RegisteredTranscriptView): () => void {
  const id = view.id;
  const registration: Registration = { view };
  registeredViews.set(id, registration);

  const remount = remountCaptures.get(id);
  if (remount) {
    remountCaptures.delete(id);
    if (view.logicalRef === remount.logicalRef && (view.layout === undefined || view.layout === remount.targetLayout)) {
      try {
        view.restore(remount.captured);
      } catch {
        // A remounted pane may still be completing its own mount. The regular
        // measurement callback gets another chance to restore its pending view.
      }
    }
  }
  const reload = contentReloadCaptures.get(id);
  if (reload) {
    contentReloadCaptures.delete(id);
    if (reload.logicalRef === view.logicalRef) view.restore(reload.captured);
  }
  const prepared = preparedRemounts.get(id);
  if (
    prepared &&
    (prepared.logicalRef !== view.logicalRef || (view.layout !== undefined && view.layout !== prepared.targetLayout))
  ) {
    preparedRemounts.delete(id);
  }

  return () => {
    if (registeredViews.get(id) === registration) {
      const prepared = preparedRemounts.get(id);
      if (prepared) {
        preparedRemounts.delete(id);
        remountCaptures.set(id, prepared);
      } else if (view.logicalRef !== undefined && mountedOwners.get(id)?.logicalRef === view.logicalRef) {
        contentReloadCaptures.set(id, { logicalRef: view.logicalRef, captured: view.capture() });
      }
      registeredViews.delete(id);
    }
  };
}

export function captureTranscriptView(id: string): CapturedTranscriptView | undefined {
  try {
    return registeredViews.get(id)?.view.capture();
  } catch {
    // A pane may disappear while its view is being captured.
    return undefined;
  }
}

export function captureTranscriptViews(): ReadonlyMap<string, CapturedTranscriptView> {
  const captured = new Map<string, CapturedTranscriptView>();
  for (const id of [...registeredViews.keys()]) {
    const view = captureTranscriptView(id);
    if (view) captured.set(id, view);
  }
  return captured;
}

export function restoreTranscriptView(id: string, captured: CapturedTranscriptView): void {
  try {
    registeredViews.get(id)?.view.restore(captured);
  } catch {
    // A stale or unmounted pane must not prevent other panes from restoring.
  }
}

export function restoreTranscriptViews(captured: ReadonlyMap<string, CapturedTranscriptView>): void {
  for (const [id, view] of captured) restoreTranscriptView(id, view);
}

/** Arm captured panes for an upcoming viewport host remount. */
export function prepareTranscriptViewRemount(
  captured: ReadonlyMap<string, CapturedTranscriptView>,
  targetLayout: string,
): void {
  for (const [id, capturedView] of captured) {
    preparedRemounts.set(id, {
      targetLayout,
      captured: capturedView,
      logicalRef: registeredViews.get(id)?.view.logicalRef,
    });
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
  mountedOwners.clear();
  contentReloadCaptures.clear();
  hasLastTransitionFingerprint = false;
  lastTransitionFingerprint = undefined;
}
