import {
  safeCredentialTestResult,
  sessionActionError,
} from "@evener/appwire-client";
import type {
  AuthTestResponse,
  InstanceCreateParams,
  InstanceEditParams,
} from "@evener/appwire-client";
import {
  type CredentialListing,
  createCredentialInstancesStore,
  listingOf,
} from "@evener/appwire-client/state/credentials";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";

interface ProviderState {
  credentialTest: {
    provider: string;
    pending: boolean;
    result?: AuthTestResponse;
  } | null;
  // Null until a read has landed: the core's empty listing and "never read"
  // look the same from its state, and the screen shows a spinner for one and
  // an empty list for the other.
  data: CredentialListing | null;
  loading: boolean;
  busy: boolean;
  error: string | null;
}

/** Provider data and operations owned by one connected hub's screen lifetime:
 * the package's credential instances listing core, driven for one client and
 * projected into the snapshot the Providers screen renders. The screen's own
 * rules stay here - one write at a time (`busy`), a re-read after every write
 * whatever its reply, and a credential test that never echoes the wire. */
export class ProviderInstances {
  private core = createCredentialInstancesStore();
  private state: ProviderState = {
    credentialTest: null,
    data: null,
    loading: false,
    busy: false,
    error: null,
  };
  private listeners = new Set<() => void>();
  private unsubscribe?: () => void;
  private disposed = false;
  private dirty = false;
  private testRevision = 0;
  private inFlight?: Promise<void>;
  constructor(private client: ConversationClientLike) {
    this.core.connectionChanged(client, "ready");
  }
  getSnapshot = () => this.state;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  private publish(change: Partial<ProviderState>) {
    if (this.disposed) return;
    this.state = { ...this.state, ...change };
    for (const listener of this.listeners) listener();
  }
  start() {
    if (this.disposed || this.unsubscribe) return;
    this.unsubscribe = this.client.onNotification((notification) => {
      if (notification.method === "evener/auth/updated") void this.refresh();
    });
    void this.refresh();
  }
  refresh = (): Promise<void> => {
    if (this.disposed) return Promise.resolve();
    this.testRevision += 1;
    this.publish({ credentialTest: null });
    this.dirty = true;
    if (this.inFlight) return this.inFlight;
    if (this.state.busy) return Promise.resolve();
    this.inFlight = this.load().finally(() => {
      this.inFlight = undefined;
    });
    return this.inFlight;
  };
  private async load() {
    this.publish({ loading: true, error: null });
    do {
      this.dirty = false;
      // The core drops a read a newer request outran (fetch resolves false
      // with no error), so only an applied answer reaches the screen; a read
      // asked for while this one was out re-runs below instead.
      const applied = await this.core.getState().fetch();
      if (this.dirty) continue;
      const state = this.core.getState();
      if (applied) this.publish({ data: listingOf(state) });
      else if (state.error !== null)
        this.publish({
          error: sessionActionError("Could not load providers", state.error),
        });
    } while (this.dirty && !this.disposed && !this.state.busy);
    this.publish({ loading: false });
  }
  private async mutate(action: () => Promise<unknown>, configuration: boolean) {
    if (this.disposed) throw new Error("Provider screen is closed");
    if (this.state.busy) throw new Error("A provider operation is in progress");
    if (configuration && (!this.state.data || this.state.data.writesRefused))
      throw new Error("Provider configuration is unavailable for editing");
    this.testRevision += 1;
    this.publish({ busy: true, credentialTest: null });
    try {
      await action();
    } finally {
      this.publish({ busy: false });
      // A failed reply can still follow a successful server write. Read to
      // reconcile either outcome; never retry a credential or instance mutation.
      await this.refresh();
    }
  }
  create = (params: InstanceCreateParams) =>
    this.mutate(() => this.core.getState().create(params), true);
  edit = (params: InstanceEditParams) =>
    this.mutate(() => this.core.getState().edit(params), true);
  remove = (name: string) =>
    this.mutate(() => this.core.getState().remove(name), true);
  setDefault = (name: string) =>
    this.mutate(() => this.core.getState().setDefault(name), true);
  setApiKey = (provider: string, value: string) =>
    this.mutate(
      () => this.client.request("evener/auth/apiKey/set", { provider, value }),
      false,
    );
  setCredentialJson = (provider: string, value: string) =>
    this.mutate(
      () => this.client.request("evener/auth/credentialJson/set", { provider, value }),
      false,
    );
  clearStoredKey = (provider: string) =>
    this.mutate(
      () => this.client.request("evener/auth/apiKey/clear", { provider }),
      false,
    );
  logout = (provider: string) =>
    this.mutate(
      () => this.client.request("evener/auth/logout", { provider }),
      false,
    );
  testCredentials = async (provider: string): Promise<void> => {
    if (
      this.disposed ||
      this.state.busy ||
      this.state.loading ||
      this.state.credentialTest?.pending
    )
      return;
    const revision = ++this.testRevision;
    this.publish({ credentialTest: { provider, pending: true } });
    let result: AuthTestResponse;
    try {
      result = safeCredentialTestResult(
        provider,
        await this.client.request("evener/auth/test", { provider }),
      );
    } catch {
      result = safeCredentialTestResult(provider, {
        provider,
        status: "endpoint_failure",
        message: "",
      });
    }
    if (!this.disposed && revision === this.testRevision)
      this.publish({ credentialTest: { provider, pending: false, result } });
  };
  dispose() {
    this.disposed = true;
    this.unsubscribe?.();
    this.listeners.clear();
    // Nothing more may go out on this client for a screen that is gone: this
    // cancels the core's pending coalesced refetch and its restore-on-ready
    // read, and drops whatever in-flight answer was still its to apply.
    this.core.connectionChanged(null, "closed");
  }
}
