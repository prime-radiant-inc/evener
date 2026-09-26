import {
  configFingerprint,
  decodeLocalConfig,
  type TranscriptDisplayConfigV1,
  type ViewportClass,
} from "@evener/appwire-client";

type LocalConfigByLayout = Partial<Record<ViewportClass, TranscriptDisplayConfigV1>>;

interface SyncState {
  local: LocalConfigByLayout;
}

interface LocalMessage {
  version: 1;
  sourceId: string;
  layout: ViewportClass;
  config: string | null;
  fingerprint: string | null;
}

interface BrowserSyncOptions {
  channelName: string;
  localKeys: Record<ViewportClass, string>;
  getState: () => SyncState;
  applyLocal: (layout: ViewportClass, config: TranscriptDisplayConfigV1 | undefined) => void;
  onDetach: () => void;
}

export interface BrowserSync {
  attach(): void;
  detach(): void;
  broadcastLocal(layout: ViewportClass, encoded: string | null): void;
}

/** A random id from the browser's own randomness source, with a fallback for
 * the privacy modes that expose crypto but deny randomUUID - shared by the
 * cross-tab source id and the draft checkpoint port's record ids. */
export function makeSourceId(): string {
  try {
    if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") return crypto.randomUUID();
  } catch {
    // Some privacy modes expose crypto but deny randomUUID.
  }
  return `${Math.random().toString(36).slice(2)}-${Date.now().toString(36)}`;
}

export function createBrowserSync(options: BrowserSyncOptions): BrowserSync {
  let channel: BroadcastChannel | null = null;
  let sourceId = "";

  function isLayout(value: unknown): value is ViewportClass {
    return value === "desktop" || value === "mobile";
  }

  function isLayoutKey(key: string | null): key is string {
    return key === options.localKeys.desktop || key === options.localKeys.mobile;
  }

  function isLocalMessage(value: unknown): value is LocalMessage {
    if (typeof value !== "object" || value === null || Array.isArray(value)) return false;
    const candidate = value as Record<string, unknown>;
    if (
      Object.keys(candidate).length !== 5 ||
      candidate.version !== 1 ||
      typeof candidate.sourceId !== "string" ||
      candidate.sourceId === "" ||
      !isLayout(candidate.layout) ||
      !(candidate.config === null || typeof candidate.config === "string") ||
      !(candidate.fingerprint === null || typeof candidate.fingerprint === "string")
    )
      return false;
    if (candidate.config === null) return candidate.fingerprint === null;
    const config = decodeLocalConfig(candidate.config);
    return config !== undefined && candidate.fingerprint === configFingerprint(config);
  }

  function applyIncomingLocal(message: LocalMessage): void {
    if (message.sourceId === sourceId) return;
    const current = options.getState().local[message.layout];
    if (message.config === null) {
      if (current === undefined) return;
      options.applyLocal(message.layout, undefined);
      return;
    }
    const config = decodeLocalConfig(message.config);
    if (config === undefined || (current !== undefined && configFingerprint(current) === message.fingerprint)) return;
    options.applyLocal(message.layout, config);
  }

  function onChannelMessage(event: MessageEvent<unknown>): void {
    if (!isLocalMessage(event.data)) return;
    applyIncomingLocal(event.data);
  }

  function onStorage(event: StorageEvent): void {
    if (!isLayoutKey(event.key)) return;
    const layout = event.key.endsWith(".mobile") ? "mobile" : "desktop";
    if (event.newValue === null) {
      if (options.getState().local[layout] === undefined) return;
      options.applyLocal(layout, undefined);
      return;
    }
    const config = decodeLocalConfig(event.newValue);
    if (config === undefined) return;
    const current = options.getState().local[layout];
    if (current !== undefined && configFingerprint(current) === configFingerprint(config)) return;
    options.applyLocal(layout, config);
  }

  return {
    attach() {
      sourceId = makeSourceId();
      if (typeof BroadcastChannel !== "undefined") {
        try {
          channel = new BroadcastChannel(options.channelName);
          channel.addEventListener("message", onChannelMessage);
        } catch {
          channel = null;
        }
      }
      if (typeof window !== "undefined") window.addEventListener("storage", onStorage);
    },
    detach() {
      if (channel !== null) {
        channel.removeEventListener("message", onChannelMessage);
        channel.close();
        channel = null;
      }
      if (typeof window !== "undefined") window.removeEventListener("storage", onStorage);
      options.onDetach();
    },
    broadcastLocal(layout, encoded) {
      if (channel === null) return;
      const decoded = encoded === null ? undefined : decodeLocalConfig(encoded);
      if (encoded !== null && decoded === undefined) return;
      const message: LocalMessage = {
        version: 1,
        sourceId,
        layout,
        config: encoded,
        fingerprint: decoded === undefined ? null : configFingerprint(decoded),
      };
      try {
        channel.postMessage(message);
      } catch {
        // BroadcastChannel is an enhancement; storage and the origin tab remain
        // authoritative when a browser closes it or refuses a message.
      }
    },
  };
}
