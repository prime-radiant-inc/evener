import type {
  MarketplaceAddParams,
  MarketplaceBrowseResponse,
  MarketplaceEntry,
  MarketplaceListResponse,
} from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";

interface MarketplaceState {
  marketplaces: MarketplaceEntry[] | null;
  selected: string | null;
  catalog: MarketplaceBrowseResponse | null;
  loading: boolean;
  browsing: boolean;
  busy: boolean;
  listError: string | null;
  catalogError: string | null;
}

/** A single hub's marketplace list and the catalog currently being browsed. */
export class Marketplaces {
  private state: MarketplaceState = {
    marketplaces: null,
    selected: null,
    catalog: null,
    loading: false,
    browsing: false,
    busy: false,
    listError: null,
    catalogError: null,
  };
  private listeners = new Set<() => void>();
  private unsubscribe?: () => void;
  private disposed = false;
  private listVersion = 0;
  private catalogVersion = 0;
  constructor(private client: ConversationClientLike) {}
  getSnapshot = () => this.state;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  private publish(change: Partial<MarketplaceState>) {
    if (this.disposed) return;
    this.state = { ...this.state, ...change };
    for (const listener of this.listeners) listener();
  }
  start() {
    if (this.disposed || this.unsubscribe) return;
    this.unsubscribe = this.client.onNotification((event) => {
      if (event.method === "evener/marketplace/updated") void this.refresh();
    });
    void this.refresh();
  }
  refresh = async () => {
    if (this.disposed || this.state.busy) return;
    const version = ++this.listVersion;
    this.catalogVersion += 1;
    this.publish({ loading: true, browsing: false, listError: null });
    try {
      const response = await this.client.request("evener/marketplace/list", {});
      if (version !== this.listVersion || this.disposed) return;
      this.publish({ marketplaces: response.marketplaces });
      if (
        this.state.selected &&
        !response.marketplaces.some((item) => item.name === this.state.selected)
      )
        await this.select(null);
    } catch {
      if (version === this.listVersion)
        this.publish({
          listError: "Could not load marketplaces. Try again when connected.",
        });
    } finally {
      if (version === this.listVersion) this.publish({ loading: false });
    }
    if (version === this.listVersion) await this.browse();
  };
  select = async (name: string | null) => {
    if (this.disposed) return;
    this.catalogVersion += 1;
    this.publish({
      selected: name,
      catalog: null,
      catalogError: null,
      browsing: false,
    });
    await this.browse();
  };
  browse = async () => {
    const name = this.state.selected;
    if (this.disposed || this.state.busy || !name) return;
    const version = ++this.catalogVersion;
    this.publish({ browsing: true, catalogError: null });
    try {
      const catalog = await this.client.request("evener/marketplace/browse", {
        name,
      });
      if (version === this.catalogVersion) this.publish({ catalog });
    } catch {
      if (version === this.catalogVersion)
        this.publish({
          catalogError:
            "Could not load this catalog. Try again when connected.",
        });
    } finally {
      if (version === this.catalogVersion) this.publish({ browsing: false });
    }
  };
  private async mutate(action: () => Promise<MarketplaceListResponse>) {
    if (this.disposed) throw Error("Marketplace screen is closed");
    if (this.state.busy) throw Error("A marketplace operation is in progress");
    this.listVersion += 1;
    this.catalogVersion += 1;
    this.publish({ busy: true, loading: false, browsing: false });
    try {
      await action();
    } finally {
      this.publish({ busy: false });
      // Reconcile uncertain writes and invalidate catalog data through real reads.
      // A reconnect must not replay an add, removal or source refresh.
      await this.refresh();
    }
  }
  add = (params: MarketplaceAddParams) =>
    this.mutate(() => this.client.request("evener/marketplace/add", params));
  remove = (name: string) =>
    this.mutate(() =>
      this.client.request("evener/marketplace/remove", { name }),
    );
  refreshSource = (name: string) =>
    this.mutate(() =>
      this.client.request("evener/marketplace/refresh", { name }),
    );
  dispose() {
    this.disposed = true;
    this.listVersion += 1;
    this.catalogVersion += 1;
    this.unsubscribe?.();
    this.listeners.clear();
  }
}
