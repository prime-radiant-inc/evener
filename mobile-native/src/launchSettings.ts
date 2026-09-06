import type {
  LaunchConfigLayer,
  LaunchConfigResolved,
  LaunchOption,
} from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";

function equal(a: unknown, b: unknown): boolean {
  if (a === b) return true;
  if (!a || !b || typeof a !== "object" || typeof b !== "object") return false;
  if (Array.isArray(a) !== Array.isArray(b)) return false;
  const left = a as Record<string, unknown>;
  const right = b as Record<string, unknown>;
  const keys = Object.keys(left);
  return (
    keys.length === Object.keys(right).length &&
    keys.every(
      (key) => Object.hasOwn(right, key) && equal(left[key], right[key]),
    )
  );
}
interface LaunchState {
  options: LaunchOption[] | null;
  current: LaunchConfigLayer | null;
  draft: LaunchConfigLayer | null;
  resolved: LaunchConfigResolved | null;
  dirty: boolean;
  loading: boolean;
  saving: boolean;
  changedElsewhere: boolean;
  error: string | null;
  resolveError: string | null;
}

/** One hub/cwd/layer editor. Whole-layer writes require a fresh baseline check. */
export class LaunchSettings {
  private state: LaunchState = {
    options: null,
    current: null,
    draft: null,
    resolved: null,
    dirty: false,
    loading: false,
    saving: false,
    changedElsewhere: false,
    error: null,
    resolveError: null,
  };
  private version = 0;
  private disposed = false;
  private unsubscribe?: () => void;
  private listeners = new Set<() => void>();
  constructor(
    private client: ConversationClientLike | null,
    readonly cwd: string,
    readonly layer: "global" | "project",
  ) {}
  getSnapshot = () => this.state;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  private publish(change: Partial<LaunchState>) {
    if (this.disposed) return;
    this.state = { ...this.state, ...change };
    for (const listener of this.listeners) listener();
  }
  start() {
    if (this.disposed || this.unsubscribe || !this.client) return;
    this.unsubscribe = this.client.onNotification((event) => {
      if (event.method !== "evener/launch/updated") return;
      // A global update can change a project's inherited values too.
      if (event.params.layer !== "global" && event.params.cwd !== this.cwd)
        return;
      if (this.state.saving) return; // The post-write read reconciles notifications.
      if (this.state.dirty) {
        this.version += 1;
        this.publish({ changedElsewhere: true, loading: false });
      } else void this.refresh();
    });
  }
  /** Rebind only within the same hub. A different hub needs a different editor. */
  async setConnection(client: ConversationClientLike | null) {
    if (this.disposed || this.client === client) return;
    this.version += 1;
    this.unsubscribe?.();
    this.unsubscribe = undefined;
    this.client = client;
    this.publish({ loading: false, saving: false });
    if (client) {
      this.start();
      await this.load(false, true);
    }
  }
  refresh = (discardDraft = false) => this.load(discardDraft, false);
  private load = async (discardDraft: boolean, reconcile: boolean) => {
    if (
      this.disposed ||
      !this.client ||
      this.state.saving ||
      (this.state.dirty && !discardDraft && !reconcile)
    )
      return;
    const client = this.client;
    const version = ++this.version;
    this.publish({ loading: true, error: null });
    try {
      const [schema, current, resolved] = await Promise.all([
        client.request("evener/launch/schema", {}),
        client.request("evener/launch/getLayer", {
          cwd: this.cwd,
          layer: this.layer,
        }),
        client
          .request("evener/launch/resolve", { cwd: this.cwd })
          .catch(() => null),
      ]);
      if (version !== this.version || this.disposed) return;
      const preserve = reconcile && this.state.dirty && this.state.draft;
      const draft = preserve ? this.state.draft : current;
      const dirty = !equal(draft, current);
      this.publish({
        options: schema.options,
        current,
        draft,
        resolved,
        dirty,
        changedElsewhere:
          !!preserve &&
          dirty &&
          (this.state.changedElsewhere || !equal(current, this.state.current)),
        resolveError: resolved
          ? null
          : "Effective launch values could not be loaded.",
      });
    } catch {
      if (version === this.version)
        this.publish({
          error: "Could not load launch settings. Try again when connected.",
        });
    } finally {
      if (version === this.version) this.publish({ loading: false });
    }
  };
  edit<K extends keyof LaunchConfigLayer>(
    field: K,
    value: LaunchConfigLayer[K],
  ) {
    if (
      this.disposed ||
      this.state.saving ||
      this.state.loading ||
      !this.state.draft
    )
      throw Error("Launch settings are not editable");
    const option = this.state.options?.find((item) => item.wireField === field);
    if (!option?.defaultableLayers?.includes(this.layer))
      throw Error("Field is not editable in this layer");
    const draft = { ...this.state.draft };
    if (value === undefined) delete draft[field];
    else draft[field] = value;
    this.publish({
      draft,
      dirty: !equal(draft, this.state.current),
      error: null,
    });
  }
  save = async (): Promise<boolean> => {
    if (
      this.disposed ||
      !this.client ||
      this.state.saving ||
      this.state.loading ||
      !this.state.dirty ||
      !this.state.current ||
      !this.state.draft
    )
      return false;
    if (this.state.changedElsewhere) {
      this.publish({
        error: "Launch settings changed elsewhere. Reload before saving.",
      });
      return false;
    }
    const draft = this.state.draft;
    const baseline = this.state.current;
    const client = this.client;
    const version = ++this.version;
    const active = () => !this.disposed && version === this.version;
    this.publish({ saving: true, error: null });
    let sent = false;
    let confirmed = false;
    try {
      const latest = await client.request("evener/launch/getLayer", {
        cwd: this.cwd,
        layer: this.layer,
      });
      if (!active()) return false;
      if (!equal(latest, baseline)) {
        this.publish({
          changedElsewhere: true,
          error: "Launch settings changed elsewhere. Reload before saving.",
        });
        return false;
      }
      sent = true;
      const resolved = await client.request("evener/launch/setLayer", {
        cwd: this.cwd,
        layer: this.layer,
        config: draft,
      });
      if (!active()) return false;
      confirmed = true;
      this.publish({ resolved, resolveError: null });
    } catch {
      if (!active()) return false;
      this.publish({
        error: sent
          ? "Could not confirm the save. Review the current layer before trying again."
          : "Could not check the current layer. Nothing was sent; try again when connected.",
      });
    } finally {
      if (sent && active()) {
        try {
          const current = await client.request("evener/launch/getLayer", {
            cwd: this.cwd,
            layer: this.layer,
          });
          if (active())
            this.publish({
              current,
              dirty: !equal(current, draft),
              changedElsewhere: !equal(current, draft),
            });
        } catch {
          if (active())
            this.publish({
              changedElsewhere: true,
              error:
                "Could not read the layer after saving. Reload to confirm its state.",
            });
          confirmed = false;
        }
      }
      if (active()) this.publish({ saving: false });
    }
    return confirmed && active() && !this.state.changedElsewhere;
  };
  dispose() {
    this.disposed = true;
    this.version += 1;
    this.unsubscribe?.();
    this.listeners.clear();
  }
}
