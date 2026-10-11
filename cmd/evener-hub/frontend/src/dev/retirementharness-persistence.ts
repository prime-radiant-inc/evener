import { deferred } from "@evener/appwire-client/testing/deferred";

export interface RestartMutationSnapshot {
  clientMutationId: string;
  method: string;
  state: string;
  composerText: string;
}

export interface RestartSubmissionSnapshot {
  outbox: RestartMutationSnapshot[];
  recovery: RestartMutationSnapshot[];
}

interface RestartSubmissionObserverOptions {
  read(): Promise<RestartSubmissionSnapshot>;
  subscribe(listener: () => void): () => void;
  timeoutMs: number;
}

export interface RestartSubmissionObserver {
  pending: Promise<RestartSubmissionSnapshot>;
  settled: Promise<RestartSubmissionSnapshot>;
}

export function observeRestartSubmission({
  read,
  subscribe,
  timeoutMs,
}: RestartSubmissionObserverOptions): RestartSubmissionObserver {
  const pending = deferred<RestartSubmissionSnapshot>();
  const settled = deferred<RestartSubmissionSnapshot>();
  let clientMutationId: string | undefined;
  let readRequested = false;
  let reading = false;
  let finished = false;
  let unsubscribe = (): void => {};

  const timer = globalThis.setTimeout(() => {
    fail(new Error("retirementharness: restart-gated submission did not settle in time"));
  }, timeoutMs);

  function finish(): void {
    finished = true;
    globalThis.clearTimeout(timer);
    unsubscribe();
  }

  function fail(error: Error): void {
    if (finished) return;
    finish();
    pending.reject(error);
    settled.reject(error);
  }

  function inspect(snapshot: RestartSubmissionSnapshot): void {
    const recovery = clientMutationId
      ? snapshot.recovery.find((record) => record.clientMutationId === clientMutationId)
      : snapshot.recovery[0];
    if (recovery) {
      fail(new Error("retirementharness: restart-gated submission entered recovery"));
      return;
    }

    if (!clientMutationId) {
      if (snapshot.outbox.length === 0) return;
      if (snapshot.outbox.length !== 1) {
        fail(new Error(`retirementharness: found ${snapshot.outbox.length} restart-gated submissions`));
        return;
      }
      const record = snapshot.outbox.at(0);
      if (!record) return;
      clientMutationId = record.clientMutationId;
      pending.resolve(snapshot);
      return;
    }

    if (snapshot.outbox.some((record) => record.clientMutationId === clientMutationId)) return;
    if (readRequested) return;
    finish();
    settled.resolve(snapshot);
  }

  async function drainReads(): Promise<void> {
    reading = true;
    try {
      while (readRequested && !finished) {
        readRequested = false;
        inspect(await read());
      }
    } catch (error) {
      fail(error instanceof Error ? error : new Error(String(error)));
    } finally {
      reading = false;
    }
  }

  function requestRead(): void {
    if (finished) return;
    readRequested = true;
    if (!reading) void drainReads();
  }

  unsubscribe = subscribe(requestRead);
  requestRead();

  return { pending: pending.promise, settled: settled.promise };
}
