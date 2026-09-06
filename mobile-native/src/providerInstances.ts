import { safeCredentialTestResult } from "../../cmd/evener-hub/frontend/src/panes/settings/sections/credentials/credentialLabels";
import { sessionActionError } from "../../cmd/evener-hub/frontend/src/protocol/errors";
import type {
  AuthTestResponse,
  InstanceCreateParams,
  InstanceEditParams,
  InstanceListResponse,
} from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";

interface ProviderState {
  credentialTest: {
    provider: string;
    pending: boolean;
    result?: AuthTestResponse;
  } | null;
  data: InstanceListResponse | null;
  loading: boolean;
  busy: boolean;
  error: string | null;
}

/** Provider data and operations owned by one connected hub's screen lifetime. */
export class ProviderInstances {
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
  private revision = 0;
  private testRevision = 0;
  private inFlight?: Promise<void>;
  constructor(private client: ConversationClientLike) {}
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
      const revision = this.revision;
      try {
        const data = await this.client.request("evener/instance/list", {});
        if (!this.dirty && revision === this.revision) this.publish({ data });
      } catch (error) {
        if (!this.dirty && revision === this.revision)
          this.publish({
            error: sessionActionError("Could not load providers", error),
          });
      }
    } while (this.dirty && !this.disposed && !this.state.busy);
    this.publish({ loading: false });
  }
  private async mutate(action: () => Promise<unknown>, configuration: boolean) {
    if (this.disposed) throw new Error("Provider screen is closed");
    if (this.state.busy) throw new Error("A provider operation is in progress");
    if (configuration && (!this.state.data || this.state.data.writesRefused))
      throw new Error("Provider configuration is unavailable for editing");
    this.revision += 1;
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
    this.mutate(
      () => this.client.request("evener/instance/create", params),
      true,
    );
  edit = (params: InstanceEditParams) =>
    this.mutate(
      () => this.client.request("evener/instance/edit", params),
      true,
    );
  remove = (name: string) =>
    this.mutate(
      () => this.client.request("evener/instance/remove", { name }),
      true,
    );
  setDefault = (name: string) =>
    this.mutate(
      () => this.client.request("evener/instance/setDefault", { name }),
      true,
    );
  setApiKey = (provider: string, value: string) =>
    this.mutate(
      () => this.client.request("evener/auth/apiKey/set", { provider, value }),
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
  }
}
