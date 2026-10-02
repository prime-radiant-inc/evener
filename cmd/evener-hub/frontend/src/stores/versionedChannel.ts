// The outbox's sibling-wakeup channel (appwire-client's
// state/mutation/outbox.ts) deliberately does not use this helper: its wire is
// source-less — a sourceId floor would be a wire change — and its transport is
// host-injected and structural. See that module's channel comment and #3619.
export interface VersionedChannelMessage {
  version: 1;
  sourceId: string;
}

// Distribute over unions so each message kind keeps its own payload fields.
type MessagePayload<Message> = Message extends VersionedChannelMessage
  ? Omit<Message, keyof VersionedChannelMessage>
  : never;

interface VersionedChannelOptions<Message extends VersionedChannelMessage> {
  name: string;
  getSourceId: () => string;
  isMessage: (value: VersionedChannelMessage) => value is Message;
  onMessage: (message: Message) => void;
  createChannel?: (name: string) => BroadcastChannel | null;
}

/** Browser randomness, including privacy modes that expose crypto but deny
 * randomUUID. Also used for draft checkpoint record ids. */
export function makeSourceId(): string {
  try {
    if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") return crypto.randomUUID();
  } catch {
    // Some privacy modes expose crypto but deny randomUUID.
  }
  return `${Math.random().toString(36).slice(2)}-${Date.now().toString(36)}`;
}

function createBrowserChannel(name: string): BroadcastChannel | null {
  return typeof BroadcastChannel === "undefined" ? null : new BroadcastChannel(name);
}

/** Best-effort transport for the v1 source-id envelope. Callers own source
 * lifetimes, payload validation and any durable epoch or storage fallback.
 * connect() is explicit: the cache retries it before sends, while display
 * settings connect only on attach and otherwise rely on storage events. */
export function createVersionedChannel<Message extends VersionedChannelMessage>(
  options: VersionedChannelOptions<Message>,
) {
  let channel: BroadcastChannel | null = null;

  function onMessage(event: MessageEvent<unknown>): void {
    if (typeof event.data !== "object" || event.data === null) return;
    const message = event.data as VersionedChannelMessage;
    // This is deliberately the cache's existing envelope floor, not a
    // stronger schema: display settings additionally require a string source,
    // reject arrays and unknown keys, and validate their encoded fingerprint.
    if (message.version !== 1 || message.sourceId === undefined || message.sourceId === "") return;
    if (!options.isMessage(message) || message.sourceId === options.getSourceId()) return;
    options.onMessage(message);
  }

  return {
    connect(): void {
      if (channel !== null) return;
      try {
        channel = (options.createChannel ?? createBrowserChannel)(options.name);
        channel?.addEventListener("message", onMessage);
      } catch {
        channel = null;
      }
    },
    close(): void {
      if (channel === null) return;
      channel.removeEventListener("message", onMessage);
      channel.close();
      channel = null;
    },
    postMessage(payload: MessagePayload<Message>): void {
      if (channel === null) return;
      try {
        channel.postMessage({ version: 1, sourceId: options.getSourceId(), ...payload });
      } catch {
        // A closed or denied channel cannot change the authoritative local work.
      }
    },
  };
}
