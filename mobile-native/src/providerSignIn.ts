import type {
  AuthDeviceStartResponse,
  AuthLoginStartResponse,
  AuthStatusResponse,
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
  credentialState: "configured" | "unconfigured" | "unknown";
}

/** One sign-in flow bound to one provider instance on one hub connection. */
export class ProviderSignIn {
  private state: SignInState = {
    phase: "idle",
    busy: false,
    error: null,
    credentialState: "unknown",
  };
  private listeners = new Set<() => void>();
  private disposed = false;
  private active = true;
  private generation = 0;
  private timer?: ReturnType<typeof setTimeout>;
  private uncertain = false;
  private pendingOperation: "devicePoll" | "completion" | "status" | null =
    null;
  constructor(
    private client: ConversationClientLike | null,
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
      !this.client ||
      !this.active ||
      this.state.phase !== "device" ||
      this.state.busy ||
      this.state.error ||
      this.uncertain
    )
      return;
    const interval = this.state.device?.intervalSeconds ?? 5;
    const delay = Number.isFinite(interval) ? Math.max(1, interval || 5) : 5;
    this.timer = setTimeout(() => {
      void this.poll();
    }, delay * 1000);
  }
  /** Replace only the transport for the same hub. Dispose when changing hubs. */
  setConnection(client: ConversationClientLike | null) {
    if (this.disposed || this.client === client) return;
    this.clearTimer();
    this.generation += 1;
    this.client = client;
    const interruptedWrite =
      this.state.busy &&
      (this.state.phase === "starting" ||
        (this.state.phase === "browser" &&
          this.pendingOperation === "completion"));
    const interruptedPoll =
      this.pendingOperation === "devicePoll" && this.state.phase === "device";
    this.pendingOperation = null;
    this.uncertain ||= interruptedPoll;
    this.publish({
      busy: false,
      ...(this.state.phase === "starting" ? { phase: "error" as const } : {}),
      ...(interruptedWrite || interruptedPoll
        ? {
            error:
              "Sign-in could not be confirmed before the connection changed. Check credential status before trying again.",
          }
        : {}),
    });
    this.schedule();
  }
  setActive(active: boolean) {
    this.active = active;
    if (active) this.schedule();
    else this.clearTimer();
  }
  start = async (): Promise<void> => {
    if (this.disposed || !this.client || this.state.busy) return;
    const generation = ++this.generation;
    this.uncertain = false;
    this.pendingOperation = null;
    this.clearTimer();
    this.publish({
      phase: "starting",
      busy: true,
      error: null,
      device: undefined,
      browser: undefined,
      credentialState: "unknown",
    });
    try {
      const device = await this.client.request("evener/auth/device/start", {
        provider: this.provider,
      });
      if (!this.current(generation)) return;
      if (
        !device ||
        typeof device !== "object" ||
        (device.fallback !== undefined &&
          typeof device.fallback !== "boolean") ||
        device.provider !== this.provider
      )
        throw new Error();
      if (device.fallback === true) {
        const browser = await this.client.request("evener/auth/login/start", {
          provider: this.provider,
        });
        if (!this.current(generation)) return;
        if (
          !browser ||
          typeof browser !== "object" ||
          browser.provider !== this.provider ||
          typeof browser.flowId !== "string" ||
          !browser.flowId.trim() ||
          typeof browser.url !== "string" ||
          !this.safeURL(browser.url)
        )
          throw new Error();
        this.publish({ phase: "browser", browser, busy: false });
      } else {
        if (
          typeof device.flowId !== "string" ||
          !device.flowId.trim() ||
          typeof device.userCode !== "string" ||
          !device.userCode.trim() ||
          typeof device.verificationUrl !== "string" ||
          !this.safeURL(device.verificationUrl) ||
          typeof device.intervalSeconds !== "number" ||
          !Number.isSafeInteger(device.intervalSeconds) ||
          device.intervalSeconds < 0
        )
          throw new Error();
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
      !this.client ||
      !this.active ||
      this.state.busy ||
      this.state.phase !== "device" ||
      !this.state.device
    )
      return;
    this.clearTimer();
    const generation = this.generation;
    const flowId = this.state.device.flowId;
    this.pendingOperation = "devicePoll";
    this.publish({ busy: true, error: null });
    try {
      const response = await this.client.request("evener/auth/device/poll", {
        provider: this.provider,
        flowId,
      });
      if (!this.current(generation)) return;
      this.pendingOperation = null;
      if (response.state === "authorized") {
        if (!this.validAuthorizedStatus(response.status)) throw new Error();
        this.publish({
          phase: "authorized",
          busy: false,
          device: undefined,
          credentialState: "configured",
        });
      } else if (response.state === "expired") {
        this.publish({
          phase: "expired",
          busy: false,
          device: undefined,
          error:
            "Sign-in status could not be confirmed. Check credential status before trying again.",
        });
      } else if (response.state === "pending") {
        this.publish({ busy: false });
        this.schedule();
      } else {
        this.uncertain = true;
        this.publish({
          busy: false,
          error:
            "Authorization has not been confirmed. Check its status before starting again.",
        });
      }
    } catch {
      if (this.current(generation)) {
        this.uncertain = true;
        this.publish({
          busy: false,
          error:
            "Could not check authorization. Retry the check when connected.",
        });
        this.pendingOperation = null;
      }
    }
  };
  retryPoll = () => {
    if (
      this.disposed ||
      !this.client ||
      !this.active ||
      this.state.busy ||
      this.state.phase !== "device" ||
      !this.state.device
    )
      return Promise.resolve();
    this.uncertain = false;
    return this.poll();
  };
  complete = async (value: string): Promise<void> => {
    if (
      this.disposed ||
      !this.client ||
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
    this.pendingOperation = "completion";
    const flowId = this.state.browser.flowId;
    this.publish({ busy: true, error: null });
    try {
      const response = await this.client.request("evener/auth/login/complete", {
        provider: this.provider,
        flowId,
        redirectUrl,
      });
      if (this.current(generation)) {
        if (!this.validAuthorizedStatus(response.status)) throw new Error();
        this.pendingOperation = null;
        this.publish({
          phase: "authorized",
          busy: false,
          browser: undefined,
          credentialState: "configured",
        });
      }
    } catch {
      if (this.current(generation)) {
        this.publish({
          busy: false,
          error:
            "Sign-in completion could not be confirmed. Check credential status before submitting again.",
        });
        this.pendingOperation = null;
      }
    }
  };
  checkStatus = async (): Promise<void> => {
    if (this.disposed || !this.client || this.state.busy) return;
    const generation = this.generation;
    this.clearTimer();
    this.pendingOperation = "status";
    this.publish({ busy: true, error: null });
    try {
      const status = await this.client.request("evener/auth/status", {
        provider: this.provider,
      });
      if (!this.current(generation) || !this.validStatus(status))
        throw new Error();
      this.pendingOperation = null;
      this.publish({
        busy: false,
        error: this.uncertain
          ? "Sign-in status could not be confirmed. Check credential status before trying again."
          : null,
        credentialState:
          status.signedIn && status.hasStoredOAuth
            ? "configured"
            : "unconfigured",
      });
      if (!this.state.error && !this.uncertain) this.schedule();
    } catch {
      if (this.current(generation)) {
        this.pendingOperation = null;
        this.publish({
          busy: false,
          error: "Could not check credential status.",
        });
      }
    }
  };
  private safeURL(value: string) {
    try {
      const url = new URL(value);
      return (
        ["https:", "http:"].includes(url.protocol) &&
        !url.username &&
        !url.password
      );
    } catch {
      return false;
    }
  }
  private validStatus(value: unknown): value is AuthStatusResponse {
    if (!value || typeof value !== "object") return false;
    const status = value as Record<string, unknown>;
    return (
      status.provider === this.provider &&
      typeof status.supported === "boolean" &&
      typeof status.signedIn === "boolean" &&
      typeof status.hasStoredOAuth === "boolean" &&
      typeof status.activeSource === "string" &&
      status.activeSource.trim().length > 0
    );
  }
  private validAuthorizedStatus(value: unknown): value is AuthStatusResponse {
    return (
      this.validStatus(value) &&
      value.supported === true &&
      value.signedIn === true &&
      value.hasStoredOAuth === true
    );
  }
  dispose() {
    this.disposed = true;
    this.generation += 1;
    this.clearTimer();
    this.listeners.clear();
  }
}
