import type {
  NavigationInvalidatedPayload,
  NavigationMutation,
  NavigationReadParams,
} from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";

interface PageState<T> {
  loaded: boolean;
  truncated: boolean;
  rows: T[];
  remaining: number;
  loading: boolean;
  error: string | null;
  stale: boolean;
}
/** Keep continuation within one logical navigation resource and revision. */
export class NavigationPages<T> {
  private state: PageState<T> = {
    loaded: false,
    truncated: false,
    rows: [],
    remaining: 0,
    loading: false,
    error: null,
    stale: false,
  };
  private listeners = new Set<() => void>();
  private request = 0;
  private offset = 0;
  private version: string | null = null;
  private generation = "";
  private revision = 0;
  private notifiedGeneration = "";
  private requiredRevision = 0;
  private sequence = 0;
  private uncertain = 0;
  private notificationEpoch = 0;
  constructor(
    private client: ConversationClientLike,
    private params: NavigationReadParams,
    private field: "projects" | "sessions",
    private key: (row: T) => string,
    private limit = 50,
  ) {}
  watch() {
    return this.client.onNotification((event) => {
      if (event.method === "evener/navigation/invalidated")
        this.invalidate(event.params);
    });
  }
  private markStale() {
    this.publish({
      stale: true,
      error: "This list changed while you were browsing. Refresh to continue.",
    });
  }
  private invalidate(payload: NavigationInvalidatedPayload) {
    const changedGeneration = this.notifiedGeneration !== payload.generationId;
    if (changedGeneration) {
      this.notifiedGeneration = payload.generationId;
      this.sequence = 0;
      this.requiredRevision = 0;
    }
    if (payload.sequence <= this.sequence) return;
    const gap = payload.sequence > this.sequence + 1;
    this.sequence = payload.sequence;
    this.notificationEpoch += 1;
    const targets = payload.targets.filter(
      (target) =>
        (this.params.resource === "catalog" &&
          target.kind === "catalog" &&
          target.catalog === this.params.catalog) ||
        (this.params.resource === "project_page" &&
          (target.kind === "all_loaded_projects" ||
            (target.kind === "project" &&
              target.projectKey === this.params.projectKey))),
    );
    const unknown =
      gap || targets.some((target) => target.revision === undefined);
    if (unknown) this.uncertain += 1;
    for (const target of targets)
      this.requiredRevision = Math.max(
        this.requiredRevision,
        target.revision ?? 0,
      );
    if (
      this.state.loaded &&
      (unknown ||
        payload.generationId !== this.generation ||
        this.requiredRevision > this.revision)
    )
      this.markStale();
  }
  getSnapshot = () => this.state;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  private publish(state: Partial<PageState<T>>) {
    this.state = { ...this.state, ...state };
    for (const listener of this.listeners) listener();
  }
  cancel() {
    this.request += 1;
    this.publish({ loading: false });
  }
  refresh() {
    return this.load(true);
  }
  more() {
    return this.load(false);
  }
  async refreshAfter(receipt: NavigationMutation) {
    const notificationEpoch = this.notificationEpoch;
    let verified = await this.load(true, receipt);
    // A notification during verification invalidates that read. Read once more
    // after the event, retaining the receipt floor and never replaying the write.
    if (verified === false && notificationEpoch !== this.notificationEpoch)
      verified = await this.load(true, receipt);
    if (!verified)
      throw new Error(
        "The change was accepted, but the updated list could not be loaded. Refresh the list.",
      );
  }
  private async load(reset: boolean, receipt?: NavigationMutation) {
    if (
      !reset &&
      (this.state.loading || this.state.stale || !this.state.remaining)
    )
      return;
    const request = ++this.request;
    const uncertain = this.uncertain;
    const notificationEpoch = this.notificationEpoch;
    this.publish({ loading: true, error: null });
    try {
      const response = await this.client.request("evener/navigation/read", {
        ...this.params,
        offset: reset ? 0 : this.offset,
        limit: this.limit,
      });
      if (request !== this.request) return;
      if (
        response.status !== "ok" ||
        !response.data ||
        typeof response.data !== "object"
      )
        throw new Error(
          "Could not read this navigation page. Refresh to try again.",
        );
      const data = response.data as Record<string, unknown>;
      const raw = data[this.field];
      if (
        !Array.isArray(raw) ||
        !Number.isSafeInteger(data.remaining) ||
        Number(data.remaining) < 0 ||
        (!raw.length && Number(data.remaining) > 0)
      )
        throw new Error(
          "The hub returned an invalid navigation page. Refresh to try again.",
        );
      if (receipt) {
        const targets = receipt.targets.filter(
          (target) =>
            (this.params.resource === "catalog" &&
              target.kind === "catalog" &&
              target.catalog === this.params.catalog) ||
            (this.params.resource === "project_page" &&
              (target.kind === "all_loaded_projects" ||
                (target.kind === "project" &&
                  target.projectKey === this.params.projectKey))),
        );
        const revision = Math.max(
          0,
          ...targets.map((target) => target.revision ?? 0),
        );
        if (
          response.generationId !== receipt.generation_id ||
          response.revision < revision
        ) {
          this.publish({ loading: false });
          this.markStale();
          return;
        }
      }
      // A reset read can establish a restarted hub unless a newer event raced it.
      if (
        reset &&
        notificationEpoch === this.notificationEpoch &&
        response.generationId !== this.notifiedGeneration
      ) {
        this.notifiedGeneration = response.generationId;
        this.requiredRevision = 0;
        this.sequence = 0;
      }
      if (
        (this.notifiedGeneration &&
          response.generationId !== this.notifiedGeneration) ||
        response.revision < this.requiredRevision ||
        uncertain !== this.uncertain
      ) {
        this.publish({ loading: false });
        this.markStale();
        return false;
      }
      const version = JSON.stringify([
        response.generationId,
        response.revision,
      ]);
      if (!reset && this.version !== version) {
        this.publish({
          loading: false,
          stale: true,
          error:
            "This list changed while you were browsing. Refresh to continue.",
        });
        return;
      }
      const unique = new Map(
        (reset ? [] : this.state.rows).map((row) => [this.key(row), row]),
      );
      for (const row of raw) {
        const key = this.key(row as T);
        if (typeof key !== "string" || !key)
          throw new Error(
            "The hub returned an invalid destination. Refresh to try again.",
          );
        unique.set(key, row as T);
      }
      this.offset = (reset ? 0 : this.offset) + raw.length;
      this.version = version;
      this.generation = response.generationId;
      this.revision = response.revision;
      this.publish({
        loaded: true,
        truncated: data.truncated === true || (!reset && this.state.truncated),
        rows: [...unique.values()],
        remaining: Number(data.remaining),
        loading: false,
        stale: false,
        error: null,
      });
      return true;
    } catch (cause) {
      if (request !== this.request) return;
      this.publish({
        loading: false,
        error:
          cause instanceof Error
            ? cause.message
            : "Could not load this page. Try again.",
      });
    }
  }
}
