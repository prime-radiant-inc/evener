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
  type CredentialInstancesStore,
  type CredentialListing,
  listingChanged,
  listingOf,
} from "@evener/appwire-client/state/credentials";

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

/** Provider data and operations for one hub's provider list: the credential
 * store it is handed, projected into the snapshot the list renders. The
 * core owns the listing, its ordering, the evener/auth/updated refetch and the
 * post-write refresh; the list's own rules stay here - one write at a time
 * (`busy`), a reconciling read after a write whose reply was lost, and a
 * credential test that never echoes the wire. */
export class ProviderInstances {
  private state: ProviderState = {
    credentialTest: null,
    data: null,
    loading: false,
    busy: false,
    error: null,
  };
  private listeners = new Set<() => void>();
  private stopProjecting?: () => void;
  private disposed = false;
  private started = false;
  private testRevision = 0;
  constructor(private readonly core: CredentialInstancesStore) {}
  // The projection subscribes on first use, not in the constructor: an
  // instance a render built and discarded without start() must not keep
  // projecting a store it will never publish for.
  private connect() {
    if (this.stopProjecting || this.disposed) return;
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
    if (listingChanged(state, previous)) {
      change.data = listingOf(state);
      // The rows a credential test was checked against are gone with the
      // listing, whoever changed it - this screen, another client, the TUI -
      // so a shown result comes down and a probe still in flight is discarded.
      Object.assign(change, this.invalidateTest());
    }
    if (state.loading !== previous.loading) change.loading = state.loading;
    if (state.error !== previous.error)
      change.error = state.error === null ? null : sessionActionError("Could not load providers", state.error);
    if (Object.keys(change).length > 0) this.publish(change);
  }
  // invalidateTest retires any credential test - a shown result, or a probe
  // still in flight - and returns the state change that takes it down.
  private invalidateTest(): Pick<ProviderState, "credentialTest"> {
    this.testRevision += 1;
    return { credentialTest: null };
  }
  start() {
    if (this.disposed || this.started) return;
    this.started = true;
    this.connect();
    // Rows the store already holds for this connection - a list remounted
    // after a sign-in, say - are published as they are: the store's own
    // refresh keeps them current, and a read here would only repeat it. Rows
    // that belong to a replaced connection are read past, as ever.
    const held = this.core.getState();
    const rows = held.instances.length > 0 || held.availableProviders.length > 0;
    if (rows && !held.listingFromPreviousConnection) {
      this.publish({ data: listingOf(held) });
      return;
    }
    void this.refresh();
  }
  refresh = async (): Promise<void> => {
    if (this.disposed) return;
    this.connect();
    this.publish(this.invalidateTest());
    if (this.state.busy) return;
    await this.core.getState().fetch();
  };
  private async mutate(action: () => Promise<unknown>, configuration: boolean) {
    if (this.disposed) throw new Error("Provider screen is closed");
    if (this.state.busy) throw new Error("A provider operation is in progress");
    if (configuration && (!this.state.data || this.state.data.writesRefused))
      throw new Error("Provider configuration is unavailable for editing");
    this.connect();
    this.publish({ busy: true, ...this.invalidateTest() });
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
    this.connect();
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
    this.stopProjecting?.();
    this.listeners.clear();
  }
}
