import { isUpgradeRequiredError, mutationErrorData } from "./errors";

export interface HistoryPagingState {
  readonly loading: boolean;
  readonly pending: boolean;
  readonly error: unknown;
  readonly permanent: boolean;
}

/** Older-page demand survives failures and inactive readers. The existing
 * history store owns cursors, merging and anchors; this owner only paces reads. */
export class HistoryPaging {
  private state: HistoryPagingState = { loading: false, pending: false, error: null, permanent: false };
  private listeners = new Set<() => void>();
  private activeConsumers = new Map<string, number>();
  private consumers = new Set<string>();
  private failures = 0;
  private retry: ReturnType<typeof setTimeout> | null = null;
  private inFlight: Promise<void> | null = null;

  constructor(
    private readonly pageKey: () => string | null | undefined,
    private readonly loadPage: () => Promise<void>,
    private readonly onIdle: () => void = () => {},
  ) {}

  getSnapshot = (): HistoryPagingState => this.state;

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
      this.releaseIdle();
    };
  };

  /** Only a consumer with both demand and an active reader runs timers. */
  activate(consumer: string): () => void {
    this.activeConsumers.set(consumer, (this.activeConsumers.get(consumer) ?? 0) + 1);
    this.schedule();
    let released = false;
    return () => {
      if (released) return;
      released = true;
      const remaining = (this.activeConsumers.get(consumer) ?? 1) - 1;
      if (remaining > 0) this.activeConsumers.set(consumer, remaining);
      else this.activeConsumers.delete(consumer);
      if (this.eligibleConsumer() === undefined) this.cancelRetry();
      this.releaseIdle();
    };
  }

  request = (consumer = "reader"): Promise<void> => {
    this.consumers.add(consumer);
    if (this.inFlight !== null) {
      if (!this.state.pending) this.publish({ ...this.state, pending: true });
      return this.inFlight;
    }
    if (this.state.permanent || this.retry !== null) return Promise.resolve();
    if (this.eligibleConsumer() === undefined) {
      if (!this.state.pending) this.publish({ ...this.state, pending: true });
      return Promise.resolve();
    }
    // Defer the read until inFlight is assigned: store publishes can invoke
    // another paging affordance synchronously.
    this.inFlight = Promise.resolve().then(() => this.read());
    this.publish({ loading: true, pending: true, error: null, permanent: false });
    return this.inFlight;
  };

  /** Explicitly leaving history cancels only that view or Find demand.
   * An already-started page can still merge; its failure cannot restart demand. */
  cancel = (consumer = "reader"): void => {
    this.consumers.delete(consumer);
    if (this.eligibleConsumer() === undefined) this.cancelRetry();
    if (this.consumers.size > 0) return;
    this.cancelRetry();
    this.failures = 0;
    if (this.state.pending) this.publish({ ...this.state, pending: false });
    this.releaseIdle();
  };

  retryNow = (consumer = "reader"): Promise<void> => {
    this.consumers.add(consumer);
    if (this.inFlight !== null) return this.request(consumer);
    this.cancelRetry();
    this.failures = 0;
    this.publish({ ...this.state, permanent: false });
    return this.request(consumer);
  };

  private async read(): Promise<void> {
    let settled: HistoryPagingState;
    let failure: { error: unknown } | undefined;
    try {
      const page = this.pageKey();
      if (page !== undefined && page !== null) await this.loadPage();
      const nextPage = this.pageKey();
      if (page === undefined || nextPage === undefined || (page !== null && nextPage === page)) {
        // An unavailable or non-advancing page leaves demand unresolved, but
        // only an actual rejected read belongs in the user-facing error state.
        this.failures += 1;
        settled = { loading: false, pending: this.consumers.size > 0, error: null, permanent: false };
      } else {
        this.failures = 0;
        this.consumers.clear();
        settled = { loading: false, pending: false, error: null, permanent: false };
      }
    } catch (error) {
      this.failures += 1;
      const permanent = isUpgradeRequiredError(error) || mutationErrorData(error)?.mutationOutcome === "targetDeleted";
      settled = { loading: false, pending: this.consumers.size > 0 && !permanent, error, permanent };
      failure = { error };
    }
    // Release the request and arm failure pacing before notifying readers:
    // their next demand must either read the next cursor or respect backoff.
    this.inFlight = null;
    this.state = settled;
    this.schedule();
    this.publish(settled);
    this.releaseIdle();
    if (failure) throw failure.error;
  }

  private releaseIdle(): void {
    if (this.activeConsumers.size === 0 && this.listeners.size === 0 && !this.state.pending && !this.state.loading)
      this.onIdle();
  }

  private publish(state: HistoryPagingState): void {
    this.state = state;
    for (const listener of this.listeners) listener();
  }

  private cancelRetry(): void {
    if (this.retry !== null) clearTimeout(this.retry);
    this.retry = null;
  }

  private schedule(): void {
    if (this.eligibleConsumer() === undefined || !this.state.pending || this.inFlight !== null || this.retry !== null)
      return;
    // Limit request rate, never the number or duration of attempts.
    const delay = this.failures === 0 ? 0 : Math.min(1000 * 2 ** Math.min(this.failures - 1, 5), 30_000);
    this.retry = setTimeout(() => {
      this.retry = null;
      const consumer = this.eligibleConsumer();
      if (consumer !== undefined) void this.request(consumer).catch(() => {});
    }, delay);
  }

  private eligibleConsumer(): string | undefined {
    for (const consumer of this.consumers) {
      if ((this.activeConsumers.get(consumer) ?? 0) > 0) return consumer;
    }
    return undefined;
  }
}
