import type {
  AuthDeviceStartResponse,
  AuthLoginStartResponse,
} from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";

interface SignInState {
  phase:
    | "idle"
    | "starting"
    | "device"
    | "browser"
    | "authorized"
    | "expired"
    | "error";
  device?: AuthDeviceStartResponse;
  browser?: AuthLoginStartResponse;
  busy: boolean;
  error: string | null;
}

/** One sign-in flow bound to one provider instance on one hub connection. */
export class ProviderSignIn {
  private state: SignInState = { phase: "idle", busy: false, error: null };
  private listeners = new Set<() => void>();
  private disposed = false;
  private active = true;
  private generation = 0;
  private timer?: ReturnType<typeof setTimeout>;
  constructor(
    private client: ConversationClientLike,
    private provider: string,
  ) {}
  getSnapshot = () => this.state;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  private publish(change: Partial<SignInState>) {
    if (this.disposed) return;
    this.state = { ...this.state, ...change };
    for (const listener of this.listeners) listener();
  }
  private current(generation: number) {
    return !this.disposed && generation === this.generation;
  }
  private clearTimer() {
    clearTimeout(this.timer);
    this.timer = undefined;
  }
  private schedule() {
    this.clearTimer();
    if (
      this.disposed ||
      !this.active ||
      this.state.phase !== "device" ||
      this.state.busy ||
      this.state.error
    )
      return;
    const interval = this.state.device?.intervalSeconds ?? 5;
    const delay = Number.isFinite(interval) ? Math.max(1, interval || 5) : 5;
    this.timer = setTimeout(() => {
      void this.poll();
    }, delay * 1000);
  }
  setActive(active: boolean) {
    this.active = active;
    if (active) this.schedule();
    else this.clearTimer();
  }
  start = async (): Promise<void> => {
    if (this.disposed || this.state.busy) return;
    const generation = ++this.generation;
    this.clearTimer();
    this.publish({
      phase: "starting",
      busy: true,
      error: null,
      device: undefined,
      browser: undefined,
    });
    try {
      const device = await this.client.request("evener/auth/device/start", {
        provider: this.provider,
      });
      if (!this.current(generation)) return;
      if (device.fallback) {
        const browser = await this.client.request("evener/auth/login/start", {
          provider: this.provider,
        });
        if (!this.current(generation)) return;
        this.publish({ phase: "browser", browser, busy: false });
      } else {
        this.publish({ phase: "device", device, busy: false });
        this.schedule();
      }
    } catch {
      if (this.current(generation))
        this.publish({
          phase: "error",
          busy: false,
          error:
            "Sign-in could not be started. Check the connection before trying again.",
        });
    }
  };
  private poll = async (): Promise<void> => {
    if (
      this.disposed ||
      !this.active ||
      this.state.busy ||
      this.state.phase !== "device" ||
      !this.state.device
    )
      return;
    this.clearTimer();
    const generation = this.generation;
    const flowId = this.state.device.flowId;
    this.publish({ busy: true, error: null });
    try {
      const response = await this.client.request("evener/auth/device/poll", {
        provider: this.provider,
        flowId,
      });
      if (!this.current(generation)) return;
      if (response.state === "authorized") {
        this.publish({ phase: "authorized", busy: false, device: undefined });
      } else if (response.state === "expired") {
        this.publish({
          phase: "expired",
          busy: false,
          device: undefined,
          error: "The code expired. Start again to get a new code.",
        });
      } else if (response.state === "pending") {
        this.publish({ busy: false });
        this.schedule();
      } else {
        this.publish({
          busy: false,
          error:
            "Authorization has not been confirmed. Check its status before starting again.",
        });
      }
    } catch {
      if (this.current(generation))
        this.publish({
          busy: false,
          error:
            "Could not check authorization. Retry the check when connected.",
        });
    }
  };
  retryPoll = () => this.poll();
  complete = async (value: string): Promise<void> => {
    if (
      this.disposed ||
      this.state.busy ||
      this.state.phase !== "browser" ||
      !this.state.browser
    )
      return;
    const redirectUrl = value.trim();
    if (!redirectUrl) {
      this.publish({ error: "Paste the full redirect URL." });
      return;
    }
    const generation = this.generation;
    const flowId = this.state.browser.flowId;
    this.publish({ busy: true, error: null });
    try {
      await this.client.request("evener/auth/login/complete", {
        provider: this.provider,
        flowId,
        redirectUrl,
      });
      if (this.current(generation))
        this.publish({ phase: "authorized", busy: false, browser: undefined });
    } catch {
      if (this.current(generation))
        this.publish({
          busy: false,
          error:
            "Sign-in completion could not be confirmed. Check credential status before submitting again.",
        });
    }
  };
  dispose() {
    this.disposed = true;
    this.generation += 1;
    this.clearTimer();
    this.listeners.clear();
  }
}
