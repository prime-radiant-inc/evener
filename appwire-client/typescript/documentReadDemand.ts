export type DocumentReadOutcome = "success" | "transient" | "terminal";

export interface DocumentReadAttempt {
  readonly generation: string;
  isCurrent(): boolean;
}

export interface DocumentReadDemand {
  setActive(active: boolean): void;
  refresh(): void;
  replace(): void;
  dispose(): void;
}

const retryMilliseconds = [1000, 2000, 4000, 8000, 15000] as const;
const viewerProcessSeed = `${Date.now().toString(36)}-${Math.floor(Math.random() * Number.MAX_SAFE_INTEGER).toString(36)}`;
let generationCounter = 0;

function nextGeneration(): string {
  generationCounter += 1;
  return `${viewerProcessSeed}-${generationCounter}`;
}

export function createDocumentReadDemand(
  read: (attempt: DocumentReadAttempt) => Promise<DocumentReadOutcome>,
): DocumentReadDemand {
  let active = false;
  let disposed = false;
  let inFlight: Promise<void> | undefined;
  let pendingDemand = false;
  let retryTimeout: ReturnType<typeof setTimeout> | undefined;
  let retryIndex = 0;
  let publicationEpoch = 0;

  const retirePublication = () => {
    publicationEpoch += 1;
  };

  const clearRetry = () => {
    if (retryTimeout === undefined) return;
    clearTimeout(retryTimeout);
    retryTimeout = undefined;
  };

  const startPending = () => {
    if (!active || disposed || inFlight !== undefined || !pendingDemand) return;
    pendingDemand = false;
    retirePublication();
    const attemptEpoch = publicationEpoch;
    const attempt: DocumentReadAttempt = {
      generation: nextGeneration(),
      isCurrent: () => active && !disposed && publicationEpoch === attemptEpoch,
    };

    const operation = Promise.resolve().then(() => read(attempt));
    inFlight = operation.then(
      (outcome) => finish(attemptEpoch, outcome),
      () => finish(attemptEpoch, "transient"),
    );
  };

  const finish = (attemptEpoch: number, outcome: DocumentReadOutcome) => {
    inFlight = undefined;
    if (disposed) return;
    if (pendingDemand) {
      startPending();
      return;
    }
    if (!active || publicationEpoch !== attemptEpoch || outcome !== "transient") return;

    const delay = retryMilliseconds[Math.min(retryIndex, retryMilliseconds.length - 1)];
    retryIndex += 1;
    retryTimeout = setTimeout(() => {
      retryTimeout = undefined;
      if (!active || disposed) return;
      pendingDemand = true;
      startPending();
    }, delay);
  };

  const startNewSeries = () => {
    clearRetry();
    retryIndex = 0;
    retirePublication();
    pendingDemand = active && !disposed;
    startPending();
  };

  return {
    setActive(nextActive) {
      if (disposed || nextActive === active) return;
      active = nextActive;
      if (active) {
        startNewSeries();
        return;
      }
      clearRetry();
      retryIndex = 0;
      pendingDemand = false;
      retirePublication();
    },
    refresh() {
      if (disposed) return;
      startNewSeries();
    },
    replace() {
      if (disposed) return;
      startNewSeries();
    },
    dispose() {
      if (disposed) return;
      disposed = true;
      active = false;
      pendingDemand = false;
      clearRetry();
      retirePublication();
    },
  };
}
