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
  type CredentialInstancesState,
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
  // Null until a listing has landed: the core's empty listing and "never
  // read" look the same from its state, and the screen shows a spinner for
  // one and an empty list for the other.
  data: CredentialListing | null;
  loading: boolean;
  busy: boolean;
  error: string | null;
}

const LISTING_FIELDS = ["instances", "availableProviders", "diagnostics", "userLayer", "writesRefused"] as const;

// listingMoved reports whether a core transition replaced the listing: every
// applied read or write installs fresh rows, while a loading or error patch
// leaves the same arrays in place.
function listingMoved(state: CredentialInstancesState, previous: CredentialInstancesState): boolean {
  return LISTING_FIELDS.some((field) => state[field] !== previous[field]);
}

/** Provider data and operations owned by one connected hub's screen lifetime:
 * the package's credential instances store core, driven for one client and
 * projected into the snapshot the Providers screen renders. The core owns
 * the listing, its ordering, the evener/auth/updated refetch and the
 * post-write refresh; the screen's own rules stay here - one write at a time
 * (`busy`), a reconciling read after a write whose reply was lost, and a
 * credential test that never echoes the wire. */
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
  private stopProjecting: () => void;
  private disposed = false;
  private started = false;
  private testRevision = 0;
  constructor(client: ConversationClientLike) {
    this.core.connectionChanged(client, "ready");
    this.stopProjecting = this.core.subscribe((state, previous) => this.project(state, previous));
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
  private project(state: CredentialInstancesState, previous: CredentialInstancesState) {
    const change: Partial<ProviderState> = {};
    if (listingMoved(state, previous)) change.data = listingOf(state);
    if (state.loading !== previous.loading) change.loading = state.loading;
    if (state.error !== previous.error)
      change.error = state.error === null ? null : sessionActionError("Could not load providers", state.error);
    if (Object.keys(change).length > 0) this.publish(change);
  }
  start() {
    if (this.disposed || this.started) return;
    this.started = true;
    void this.refresh();
  }
  refresh = (): Promise<void> => {
    if (this.disposed) return Promise.resolve();
    this.testRevision += 1;
    this.publish({ credentialTest: null });
    if (this.state.busy) return Promise.resolve();
    return this.core
      .getState()
      .fetch()
      .then(() => undefined);
  };
  private async mutate(action: () => Promise<unknown>, configuration: boolean) {
    if (this.disposed) throw new Error("Provider screen is closed");
    if (this.state.busy) throw new Error("A provider operation is in progress");
    if (configuration && (!this.state.data || this.state.data.writesRefused))
      throw new Error("Provider configuration is unavailable for editing");
    this.testRevision += 1;
    this.publish({ busy: true, credentialTest: null });
    try {
      await action();
    } catch (error) {
      // A failed reply can still follow a successful server write. Read to
      // reconcile either outcome; never retry a credential or instance mutation.
      if (!this.disposed) await this.core.getState().fetch();
      throw error;
    } finally {
      this.publish({ busy: false });
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
    this.mutate(() => this.core.getState().setApiKey(provider, value), false);
  setCredentialJson = (provider: string, value: string) =>
    this.mutate(() => this.core.getState().setCredentialJson(provider, value), false);
  clearStoredKey = (provider: string) =>
    this.mutate(() => this.core.getState().clearStoredKey(provider), false);
  logout = (provider: string) =>
    this.mutate(() => this.core.getState().logout(provider), false);
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
        await this.core.getState().testCredentials(provider),
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
    this.stopProjecting();
    this.listeners.clear();
    // Nothing more may go out on this client for a screen that is gone: this
    // detaches the core's evener/auth/updated listener, cancels its pending
    // refetch and restore-on-ready read, and drops whatever in-flight answer
    // was still its to apply.
    this.core.connectionChanged(null, "closed");
  }
}
