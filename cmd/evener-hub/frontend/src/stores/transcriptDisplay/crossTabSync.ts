import {
  configFingerprint,
  decodeLocalConfig,
  type TranscriptDisplayConfigV1,
  type ViewportClass,
} from "@evener/appwire-client";
import { createVersionedChannel, makeSourceId, type VersionedChannelMessage } from "../versionedChannel";

export { makeSourceId } from "../versionedChannel";

type LocalConfigByLayout = Partial<Record<ViewportClass, TranscriptDisplayConfigV1>>;

interface SyncState {
  local: LocalConfigByLayout;
}

interface LocalMessage extends VersionedChannelMessage {
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

export function createBrowserSync(options: BrowserSyncOptions): BrowserSync {
  let sourceId = "";
  const channel = createVersionedChannel<LocalMessage>({
    name: options.channelName,
    getSourceId: () => sourceId,
    isMessage: isLocalMessage,
    onMessage: applyIncomingLocal,
  });

  function isLayout(value: unknown): value is ViewportClass {
    return value === "desktop" || value === "mobile";
  }

  function isLayoutKey(key: string | null): key is string {
    return key === options.localKeys.desktop || key === options.localKeys.mobile;
  }

  function isLocalMessage(value: VersionedChannelMessage): value is LocalMessage {
    if (Array.isArray(value)) return false;
    const candidate = value as Partial<LocalMessage>;
    if (
      Object.keys(candidate).length !== 5 ||
      typeof candidate.sourceId !== "string" ||
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
      channel.connect();
      if (typeof window !== "undefined") window.addEventListener("storage", onStorage);
    },
    detach() {
      channel.close();
      if (typeof window !== "undefined") window.removeEventListener("storage", onStorage);
      options.onDetach();
    },
    broadcastLocal(layout, encoded) {
      const decoded = encoded === null ? undefined : decodeLocalConfig(encoded);
      if (encoded !== null && decoded === undefined) return;
      channel.postMessage({
        layout,
        config: encoded,
        fingerprint: decoded === undefined ? null : configFingerprint(decoded),
      });
    },
  };
}
